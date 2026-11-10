package glossary

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"unicode"
	"unicode/utf8"
)

// Every 64 comparisons consumes one zero-allocation visit node. This bounds
// unmatched dictionary scans too; no hit allocation is required to stop work.
func sourceContainsBounded(ctx context.Context, b *backend.BuildBounds, text, source string, fold bool) (bool, error) {
	visits := 0
	tick := func() error {
		if visits%64 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := b.ReserveNodes(1, 0); err != nil {
				return err
			}
		}
		visits++
		return nil
	}
	if source == "" {
		return true, nil
	}
	if !fold {
		for start := 0; start <= len(text)-len(source); start++ {
			i := 0
			for i < len(source) {
				if err := tick(); err != nil {
					return false, err
				}
				if text[start+i] != source[i] {
					break
				}
				i++
			}
			if i == len(source) {
				return true, nil
			}
		}
		return false, nil
	}
	for start := 0; start < len(text); {
		t, s := start, 0
		for s < len(source) && t < len(text) {
			if err := tick(); err != nil {
				return false, err
			}
			tr, tn := utf8.DecodeRuneInString(text[t:])
			sr, sn := utf8.DecodeRuneInString(source[s:])
			if fold {
				tr = unicode.ToLower(tr)
				sr = unicode.ToLower(sr)
			}
			if tr != sr {
				break
			}
			t += tn
			s += sn
		}
		if s == len(source) {
			return true, nil
		}
		_, n := utf8.DecodeRuneInString(text[start:])
		start += n
	}
	return false, nil
}
