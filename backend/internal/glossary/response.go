package glossary

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

const ResponseReceiptVersion = 2

type ResponseTarget struct {
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
	RequestID, InputDigest, TargetLang string
	Entries                            []Entry
	EntriesPayload                     []byte
	Targets                            []ResponseTarget
	Rewrite                            bool
	ItemLimit, BusinessLimit           int64
	Validate                           func(context.Context, ResponseTarget) error
	Build                              func(context.Context, func() error) error
}
type ResponseBusiness struct{ Body, Index, Receipt []byte }
type ResponseProcessor interface {
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
		return "", false, backend.ErrBuildCapacity
	}
	size += count * delta
	if size < 0 || size > limit {
		return "", false, backend.ErrBuildCapacity
	}
	w := backend.NewBoundedWriter(b, size)
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

func EncodeResponseBusiness(targets []ResponseTarget, limit int64) ([]byte, []byte, error) {
	b, err := backend.NewBuildBounds(limit, 0)
	if err != nil {
		return nil, nil, err
	}
	size := int64(0)
	for _, target := range targets {
		if int64(len(target.Text)) > limit-size {
			return nil, nil, backend.ErrBuildCapacity
		}
		size += int64(len(target.Text))
	}
	if err := b.ReserveText(size, size); err != nil {
		return nil, nil, err
	}
	if err := b.ReserveNodes(int64(len(targets)), 16); err != nil {
		return nil, nil, err
	}
	body := make([]byte, int(size))
	index := make([]byte, len(targets)*16)
	offset := 0
	for i, t := range targets {
		binary.LittleEndian.PutUint64(index[i*16:], uint64(offset))
		binary.LittleEndian.PutUint64(index[i*16+8:], uint64(len(t.Text)))
		copy(body[offset:], t.Text)
		offset += len(t.Text)
	}
	return body, index, nil
}

func RewriteResponse(ctx context.Context, in ResponseProcess, decisions []SkippedEntry) ([]ResponseTarget, error) {
	targets := append([]ResponseTarget(nil), in.Targets...)
	for i := range targets {
		b, err := backend.NewBuildBounds(in.ItemLimit, int64(len(targets[i].Text)))
		if err != nil {
			return nil, err
		}
		for _, sk := range decisions {
			if !in.Rewrite || sk.Reason != SkipReasonExists {
				continue
			}
			text, _, err := SafeReplaceBounded(b, targets[i].Text, sk.Proposed.Target, sk.Existing.Target, in.TargetLang, in.ItemLimit)
			if err != nil {
				targets[i].Failure = fmt.Sprintf("capacity: %v", err)
				targets[i].Text = ""
				break
			}
			targets[i].Text = text
		}
		if targets[i].Failure == "" && in.Validate != nil {
			if err := in.Validate(ctx, targets[i]); err != nil {
				targets[i].Failure = err.Error()
				targets[i].Text = ""
			}
		}
	}
	return targets, nil
}

func BuildResponseBusiness(ctx context.Context, in ResponseProcess, result AddResult) (out ResponseBusiness, err error) {
	if in.Build != nil {
		run := in.Build
		in.Build = nil
		err = run(ctx, func() error { var e error; out, e = BuildResponseBusiness(ctx, in, result); return e })
		return out, err
	}

	targets, err := RewriteResponse(ctx, in, result.Skipped)
	if err != nil {
		return ResponseBusiness{}, err
	}
	if in.Rewrite {
		out.Body, out.Index, err = EncodeResponseBusiness(targets, in.BusinessLimit)
		if err != nil {
			return out, err
		}
	} else {
		out.Index = make([]byte, len(in.Targets)*16)
	}
	h := sha256.Sum256(out.Body)
	r := ResponseReceipt{Version: 2, RequestID: in.RequestID, InputDigest: in.InputDigest, OutcomeDigest: hex.EncodeToString(h[:]), Added: len(result.Added), Skipped: len(result.Skipped)}
	b, err := backend.NewBuildBounds(32<<10, 0)
	if err != nil {
		return out, err
	}
	out.Receipt, err = b.EncodeJSON(r, 32<<10)
	return out, err
}

// ProcessResponse stages FileGlossary additions under its existing mutation
// lock. A failed bounded rewrite leaves the glossary unchanged.
func (g *FileGlossary) ProcessResponse(ctx context.Context, in ResponseProcess) (ResponseBusiness, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if in.EntriesPayload != nil {
		var out ResponseBusiness
		build := func() error {
			b, err := backend.NewBuildBounds(int64(len(in.EntriesPayload))+in.BusinessLimit, int64(len(in.EntriesPayload)))
			if err != nil {
				return err
			}
			if err = b.DecodeJSON(in.EntriesPayload, &in.Entries); err != nil {
				return err
			}
			if err = b.ReserveNodes(int64(len(in.Entries))*6, 256); err != nil {
				return err
			}
			in.Build = nil
			in.EntriesPayload = nil
			out, err = g.processResponseLocked(ctx, in)
			return err
		}
		var err error
		if in.Build != nil {
			err = in.Build(ctx, build)
		} else {
			err = build()
		}
		return out, err
	}
	return g.processResponseLocked(ctx, in)
}
func (g *FileGlossary) processResponseLocked(ctx context.Context, in ResponseProcess) (ResponseBusiness, error) {
	var result AddResult
	pending := map[string]Entry{}
	for _, entry := range in.Entries {
		if entry.Source == "" || entry.Target == "" {
			result.Skipped = append(result.Skipped, SkippedEntry{Proposed: entry, Reason: SkipReasonEmpty})
			continue
		}
		key := normKey(entry)
		existing, ok := pending[key]
		if !ok {
			if idx, found := g.bySource[key]; found {
				existing, ok = g.entries[idx], true
			}
		}
		if ok {
			if existing.Target != entry.Target {
				result.Skipped = append(result.Skipped, SkippedEntry{Proposed: entry, Existing: Entry{Target: existing.Target}, Reason: SkipReasonExists})
			}
			continue
		}
		pending[key] = entry
		result.Added = append(result.Added, entry)
	}
	out, err := BuildResponseBusiness(ctx, in, result)
	if err != nil {
		return out, err
	}
	for _, entry := range result.Added {
		g.addLocked(entry)
	}
	if len(result.Added) > 0 {
		g.sortLocked()
		g.dirty = true
	}
	return out, nil
}

// RemoveResponseTarget retires one final UTF-8 slice while preserving fixed slot identity.
func RemoveResponseTarget(body, index []byte, member int) ([]byte, []byte, error) {
	if member < 0 || len(index)%16 != 0 || member >= len(index)/16 {
		return nil, nil, fmt.Errorf("invalid response target index")
	}
	start := binary.LittleEndian.Uint64(index[member*16:])
	n := binary.LittleEndian.Uint64(index[member*16+8:])
	if start > uint64(len(body)) || n > uint64(len(body))-start {
		return nil, nil, fmt.Errorf("invalid response target range")
	}
	b, err := backend.NewBuildBounds(int64(len(body))+int64(len(index)), 0)
	if err != nil {
		return nil, nil, err
	}
	if err = b.ReserveText(int64(len(body))-int64(n), int64(len(body))-int64(n)); err != nil {
		return nil, nil, err
	}
	if err = b.ReserveText(int64(len(index)), int64(len(index))); err != nil {
		return nil, nil, err
	}
	out := make([]byte, len(body)-int(n))
	copy(out, body[:int(start)])
	copy(out[int(start):], body[int(start+n):])
	slots := make([]byte, len(index))
	copy(slots, index)
	binary.LittleEndian.PutUint64(slots[member*16+8:], 0)
	for i := member + 1; i < len(slots)/16; i++ {
		offset := binary.LittleEndian.Uint64(slots[i*16:])
		if offset < n {
			return nil, nil, fmt.Errorf("invalid response target offset")
		}
		binary.LittleEndian.PutUint64(slots[i*16:], offset-n)
	}
	return out, slots, nil
}
