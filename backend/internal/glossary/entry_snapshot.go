package glossary

import (
	"context"
	"slices"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// EntrySnapshot is immutable dictionary input. Publishing a new head never
// changes records already borrowed by a CPU builder.
type EntrySnapshot struct {
	Entry    Entry
	Order    int
	Previous *EntrySnapshot
}
type EntriesLookup interface {
	LookupEntriesBounded(context.Context, *backend.BuildBounds, string, string, string) ([]Entry, error)
}

func LookupEntriesBounded(ctx context.Context, b *backend.BuildBounds, g Glossary, text, sourceLang, targetLang string) ([]Entry, error) {
	if lookup, ok := g.(EntriesLookup); ok {
		return lookup.LookupEntriesBounded(ctx, b, text, sourceLang, targetLang)
	}
	return nil, backend.ErrBuildCapacity
}
func LookupEntrySnapshotBounded(b *backend.BuildBounds, head *EntrySnapshot, text string, longestFirst bool) ([]Entry, error) {
	return LookupEntrySnapshotContextBounded(context.Background(), b, head, text, longestFirst)
}
func LookupEntrySnapshotContextBounded(ctx context.Context, b *backend.BuildBounds, head *EntrySnapshot, text string, longestFirst bool) ([]Entry, error) {
	type hit struct {
		e     Entry
		order int
	}
	var hits []hit
	for n := head; n != nil; n = n.Previous {
		source := strings.TrimSpace(n.Entry.Source)
		if source == "" {
			continue
		}
		if err := b.ReserveNodes(1, 0); err != nil {
			return nil, err
		}
		matches, matchErr := sourceContainsBounded(ctx, b, text, source, !n.Entry.CaseSensitive)
		if matchErr != nil {
			return nil, matchErr
		}
		if !matches {
			continue
		}
		var err error
		hits, err = backend.AppendNode(b, hits, hit{n.Entry, n.Order})
		if err != nil {
			return nil, err
		}
	}
	slices.SortStableFunc(hits, func(a, c hit) int {
		if longestFirst && len(a.e.Source) != len(c.e.Source) {
			if len(a.e.Source) > len(c.e.Source) {
				return -1
			}
			return 1
		}
		if a.order < c.order {
			return -1
		}
		if a.order > c.order {
			return 1
		}
		return 0
	})
	var out []Entry
	for _, h := range hits {
		var err error
		out, err = backend.AppendNode(b, out, h.e)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (g *FileGlossary) LookupEntriesBounded(ctx context.Context, b *backend.BuildBounds, text, _, _ string) ([]Entry, error) {
	return LookupEntrySnapshotContextBounded(ctx, b, g.entrySnapshot.Load(), text, true)
}
func (Nop) LookupEntriesBounded(context.Context, *backend.BuildBounds, string, string, string) ([]Entry, error) {
	return nil, nil
}
