package glossary

import (
	"bytes"
	"context"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// VisitResponseEntries carries only a single fixed entry across a storage call.
// The encoded array stays opaque outside the caller's short build permit.
func VisitResponseEntries(ctx context.Context, in ResponseProcess, visit func(int, Entry) error) error {
	if in.EntriesPayload == nil {
		for i, e := range in.Entries {
			if err := visit(i, e); err != nil {
				return err
			}
		}
		return nil
	}
	payload := in.EntriesPayload
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return nil
	}
	pos := 0
	space := func() {
		for pos < len(payload) && (payload[pos] == ' ' || payload[pos] == '\n' || payload[pos] == '\r' || payload[pos] == '\t') {
			pos++
		}
	}
	space()
	if pos == len(payload) || payload[pos] != '[' {
		return fmt.Errorf("invalid response glossary array")
	}
	pos++
	for ordinal := 0; ; ordinal++ {
		space()
		if pos < len(payload) && payload[pos] == ']' {
			pos++
			space()
			if pos != len(payload) {
				return fmt.Errorf("trailing response glossary")
			}
			return nil
		}
		if ordinal >= 65536 {
			return backend.ErrBuildCapacity
		}
		var entry Entry
		var n int
		decode := func() error {
			b, err := backend.NewBuildBounds(max(int64(2048), in.ItemLimit), int64(len(payload)))
			if err != nil {
				return err
			}
			raw, consumed, err := backend.FirstJSONRaw(b, payload[pos:])
			if err != nil {
				return err
			}
			n = consumed
			return b.DecodeJSONLimited(raw, &entry, int64(len(payload)))
		}
		var err error
		if in.Build != nil {
			err = in.Build(ctx, decode)
		} else {
			err = decode()
		}
		if err != nil {
			return err
		}
		if err = visit(ordinal, entry); err != nil {
			return err
		}
		entry = Entry{}
		pos += n
		space()
		if pos < len(payload) && payload[pos] == ',' {
			pos++
			continue
		}
		if pos < len(payload) && payload[pos] == ']' {
			continue
		}
		return fmt.Errorf("invalid response glossary separator")
	}
}

// ApplyResponseConflict consumes one projected target, then drops it before
// querying the next glossary row. Corrected member texts remain the J business
// owner; no decoded AddResult tree accompanies the transaction.
func ApplyResponseConflict(ctx context.Context, in ResponseProcess, from, to string) error {
	if !in.Rewrite || from == to {
		return nil
	}
	build := func() error {
		for i := range in.Targets {
			if in.Targets[i].Failure != "" {
				continue
			}
			b, err := backend.NewBuildBounds(in.ItemLimit, int64(len(in.Targets[i].Text))+int64(len(from))+int64(len(to)))
			if err != nil {
				return err
			}
			text, _, err := SafeReplaceBounded(b, in.Targets[i].Text, from, to, in.TargetLang, in.ItemLimit)
			if err != nil {
				in.Targets[i].Failure = fmt.Sprintf("capacity: %v", err)
				in.Targets[i].Text = ""
				continue
			}
			in.Targets[i].Text = text
		}
		return nil
	}
	if in.Build != nil {
		return in.Build(ctx, build)
	}
	return build()
}

func BuildResponseBusinessCounts(ctx context.Context, in ResponseProcess, added, skipped int) (ResponseBusiness, error) {
	out, err := BuildResponseBusiness(ctx, in, AddResult{})
	if err != nil {
		return out, err
	}
	build := func() error {
		b, err := backend.NewBuildBounds(32<<10, 0)
		if err != nil {
			return err
		}
		var receipt ResponseReceipt
		if err = b.DecodeJSON(out.Receipt, &receipt); err != nil {
			return err
		}
		receipt.Added = added
		receipt.Skipped = skipped
		out.Receipt, err = b.EncodeJSON(receipt, 32<<10)
		return err
	}
	if in.Build != nil {
		err = in.Build(ctx, build)
	} else {
		err = build()
	}
	return out, err
}
