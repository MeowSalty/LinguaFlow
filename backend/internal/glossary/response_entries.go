package glossary

import (
	"bytes"
	"context"
	"fmt"
	"math"

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
		if ordinal == math.MaxInt {
			return backend.ErrBuildCapacity
		}
		var entry Entry
		var n int
		var bounds *backend.BuildBounds
		decode := func() error {
			b, err := backend.NewBuildBoundsFromContext(ctx, in.ItemLimit, int64(len(payload)))
			if err != nil {
				return err
			}
			bounds = b
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
			if bounds != nil {
				bounds.CloseRetired()
			}
			return err
		}
		err = visit(ordinal, entry)
		entry = Entry{}
		bounds.CloseRetired()
		if err != nil {
			return err
		}
		pos += n
		space()
		if pos < len(payload) && payload[pos] == ',' {
			pos++
			space()
			if pos == len(payload) || payload[pos] == ']' {
				return fmt.Errorf("invalid response glossary separator")
			}
			continue
		}
		if pos < len(payload) && payload[pos] == ']' {
			continue
		}
		return fmt.Errorf("invalid response glossary separator")
	}
}
