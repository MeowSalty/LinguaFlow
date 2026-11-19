package glossary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"strings"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

const ResponseReceiptVersion = 2
const ResponseReceiptLimit = 2 << 10

type ResponseTarget struct {
	Outcome responsecontract.MemberOutcome
	Ordinal int
	ID      string
	Text    string
	Failure string
}
type ResponseReceipt struct {
	Version        int    `json:"version"`
	RequestID      string `json:"request_id"`
	InputDigest    string `json:"input_digest"`
	OutcomeDigest  string `json:"outcome_digest"`
	Added, Skipped int
}
type ResponseProcess struct {
	CaptureEpoch                       int64
	RequestID, InputDigest, TargetLang string
	Entries                            []Entry
	EntriesPayload                     []byte
	Rewrite                            bool
	ItemLimit, BusinessLimit           int64
	Validate                           func(context.Context, ResponseTarget) error
	Build                              func(context.Context, func() error) error
	VisitTargets                       func(context.Context, func(ResponseTarget) error) error
	WriteTarget                        func(context.Context, ResponseTarget) error
	Publish                            func(context.Context, ResponseBusiness, *responsecontract.PreparedPublication) error
}
type ResponseBusiness struct {
	Receipt           []byte
	ReceiptAllocation *backend.OwnedAllocation `json:"-"`
}

// Release follows the last borrower of the returned immutable buffers.
func (out *ResponseBusiness) Release() {
	receipt := out.ReceiptAllocation
	*out = ResponseBusiness{}
	receipt.Release()
}

type ResponseProcessor interface {
	ResponsePublicationDomain(responsecontract.PublicationDomain) (responsecontract.PublicationDomain, error)
	ProcessResponse(context.Context, ResponseProcess) (ResponseBusiness, error)
}

// SafeReplaceBounded counts independent matches before allocating replacement
// text. Both passes use the same strings and boundary predicate.
func SafeReplaceBounded(b *backend.BuildBounds, s, from, to, lang string, limit int64) (string, bool, error) {
	if from == "" || from == to {
		return s, false, nil
	}
	cjk := IsCJKTarget(lang)
	count := int64(0)
	for pos := 0; pos < len(s); {
		j := strings.Index(s[pos:], from)
		if j < 0 {
			break
		}
		start := pos + j
		end := start + len(from)
		if cjk || isIndependentMatch(s, start, end) {
			count++
		}
		pos = end
	}
	if count == 0 {
		return s, false, nil
	}
	size := int64(len(s))
	delta := int64(len(to)) - int64(len(from))
	if delta > 0 && (count > (limit-size)/delta || size > limit) {
		return "", false, &responsecontract.MemberContentError{Ordinal: -1, Kind: responsecontract.MemberTooLarge, Limit: limit, Observed: limit + 1}
	}
	size += count * delta
	if size < 0 || size > limit {
		return "", false, &responsecontract.MemberContentError{Ordinal: -1, Kind: responsecontract.MemberTooLarge, Limit: limit, Observed: limit + 1}
	}
	w := backend.NewBoundedWriter(b, size)
	defer w.Release()
	for pos := 0; pos < len(s); {
		j := strings.Index(s[pos:], from)
		if j < 0 {
			if _, err := w.WriteString(s[pos:]); err != nil {
				return "", false, err
			}
			break
		}
		start := pos + j
		end := start + len(from)
		if _, err := w.WriteString(s[pos:start]); err != nil {
			return "", false, err
		}
		text := s[start:end]
		if cjk || isIndependentMatch(s, start, end) {
			text = to
		}
		if _, err := w.WriteString(text); err != nil {
			return "", false, err
		}
		pos = end
	}
	if err := b.ReserveText(size, size); err != nil {
		return "", false, err
	}
	return string(w.Bytes()), true, nil
}

// BuildResponseBusiness visits each model member once. The sink takes a private
// immutable member before the iterator releases its borrowed frame.
func BuildResponseBusiness(ctx context.Context, in ResponseProcess, result AddResult) (out ResponseBusiness, err error) {
	return buildResponseBusiness(ctx, in, result.Skipped, len(result.Added), len(result.Skipped))
}

func buildResponseBusiness(ctx context.Context, in ResponseProcess, decisions []SkippedEntry, added, skipped int) (out ResponseBusiness, err error) {
	defer func() {
		if err != nil {
			out.Release()
		}
	}()
	h := sha256.New()
	var total int64
	ordinal := 0
	if in.VisitTargets != nil {
		if in.WriteTarget == nil {
			return out, fmt.Errorf("missing response member sink")
		}
		err = in.VisitTargets(ctx, func(target ResponseTarget) error {
			if target.Ordinal != ordinal {
				return fmt.Errorf("invalid response member ordinal: got %d want %d", target.Ordinal, ordinal)
			}
			ordinal++
			if e := ctx.Err(); e != nil {
				return e
			}
			b, e := backend.NewBuildBoundsFromContext(ctx, in.ItemLimit, int64(len(target.Text)))
			if e != nil {
				return e
			}
			defer b.CloseRetired()
			build := func() error {
				for _, sk := range decisions {
					if !in.Rewrite || target.Failure != "" || sk.Reason != SkipReasonExists {
						continue
					}
					text, _, replaceErr := SafeReplaceBounded(b, target.Text, sk.Proposed.Target, sk.Existing.Target, in.TargetLang, in.ItemLimit)
					if replaceErr != nil {
						var content *responsecontract.MemberContentError
						if !errors.As(replaceErr, &content) {
							return replaceErr
						}
						content.Ordinal = target.Ordinal
						target.Failure, target.Text = content.Error(), ""
						target.Outcome = responsecontract.MemberOutcome{Key: responsecontract.MemberKey{RequestID: in.RequestID, Ordinal: target.Ordinal, CaptureEpoch: in.CaptureEpoch}, Reason: content.Kind, Limit: content.Limit, Observed: content.Observed}
						break
					}
					target.Text = text
				}
				if target.Failure == "" && in.Validate != nil {
					if validateErr := in.Validate(ctx, target); validateErr != nil {
						var content *responsecontract.MemberContentError
						if !errors.As(validateErr, &content) {
							return validateErr
						}
						target.Failure, target.Text = validateErr.Error(), ""
						target.Outcome = responsecontract.MemberOutcome{Key: responsecontract.MemberKey{RequestID: in.RequestID, Ordinal: target.Ordinal, CaptureEpoch: in.CaptureEpoch}, Reason: content.Kind, Limit: content.Limit, Observed: content.Observed}
					}
				}
				return nil
			}
			if in.Build != nil {
				e = in.Build(ctx, build)
			} else {
				e = build()
			}
			if e != nil {
				return e
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			if int64(len(target.Text)) > in.ItemLimit {
				return backend.ErrBuildCapacity
			}
			if int64(len(target.Text)) > in.BusinessLimit-total {
				return backend.ErrBuildCapacity
			}
			total += int64(len(target.Text))
			if e = in.WriteTarget(ctx, target); e != nil {
				return e
			}
			// The digest borrows immutable text; it must not allocate a second
			// full member byte backing outside the explicit ownership ledger.
			_, e = h.Write(unsafe.Slice(unsafe.StringData(target.Text), len(target.Text)))
			return e
		})
		if err != nil {
			return out, err
		}
	}
	buildReceipt := func() error {
		b, e := backend.NewBuildBoundsFromContext(ctx, ResponseReceiptLimit, 0)
		if e != nil {
			return e
		}
		defer b.CloseRetired()
		// SHA256 is a fixed local value; only the owned hex string and encoded
		// receipt acquire backing, independently of the business text limit.
		var sum [sha256.Size]byte
		h.Sum(sum[:0])
		if e = b.ReserveText(sha256.Size*2, sha256.Size*2); e != nil {
			return e
		}
		r := ResponseReceipt{Version: ResponseReceiptVersion, RequestID: in.RequestID, InputDigest: in.InputDigest, OutcomeDigest: hex.EncodeToString(sum[:]), Added: added, Skipped: skipped}
		out.Receipt, e = b.EncodeJSON(r, ResponseReceiptLimit)
		if e != nil {
			return e
		}
		out.ReceiptAllocation, e = b.RetainedAllocation(int64(cap(out.Receipt)))
		if e == nil {
			e = out.ReceiptAllocation.DetachBuild()
		}
		return e
	}
	if in.Build != nil {
		err = in.Build(ctx, buildReceipt)
	} else {
		err = buildReceipt()
	}
	return out, err
}
