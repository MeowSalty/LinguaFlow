package service

import (
	"bytes"
	"context"
	"math"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// responseGlossaryEntryCount scans borrowed JSON without constructing an arena
// or decoded entries. Actual top-level entry count sizes the transaction bitset.
func responseGlossaryEntryCount(ctx context.Context, in glossary.ResponseProcess) (int, error) {
	if in.EntriesPayload == nil {
		return len(in.Entries), nil
	}
	data := bytes.TrimSpace(in.EntriesPayload)
	if bytes.Equal(data, []byte("null")) {
		return 0, nil
	}
	count := 0
	build := func() error {
		b, err := backend.NewBuildBoundsFromContext(ctx, 0, int64(len(data)))
		if err != nil {
			return err
		}
		if len(data) < 2 || data[0] != '[' || data[len(data)-1] != ']' {
			return workstate.ErrManifest
		}
		rest := bytes.TrimSpace(data[1 : len(data)-1])
		for len(rest) > 0 {
			if err := b.CheckContext(); err != nil {
				return err
			}
			if count == math.MaxInt {
				return backend.ErrBuildCapacity
			}
			_, consumed, err := backend.FirstJSONRaw(b, rest)
			if err != nil || consumed <= 0 {
				return workstate.ErrManifest
			}
			count++
			rest = bytes.TrimSpace(rest[consumed:])
			if len(rest) == 0 {
				break
			}
			if rest[0] != ',' {
				return workstate.ErrManifest
			}
			rest = bytes.TrimSpace(rest[1:])
			if len(rest) == 0 {
				return workstate.ErrManifest
			}
		}
		return nil
	}
	var err error
	if in.Build != nil {
		err = in.Build(ctx, build)
	} else {
		err = build()
	}
	return count, err
}
