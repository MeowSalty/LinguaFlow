package glossary

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"math"
	"strings"
)

type EntryVisitor interface {
	VisitMatchesBounded(context.Context, string, string, string, *backend.BuildBounds, func(context.Context, func() error) error, func(Entry) error) error
}

// VisitEntrySnapshotMatches keeps only one fixed selection record. Selecting
// the next original-order/longest-first match needs no private array of hits.
func VisitEntrySnapshotMatches(ctx context.Context, b *backend.BuildBounds, head *EntrySnapshot, text string, longest bool, run func(context.Context, func() error) error, yield func(Entry) error) error {
	lastOrder, lastLen := -1, math.MaxInt
	if err := b.ReserveNodes(1, 128); err != nil {
		return err
	}
	var best *EntrySnapshot
	selectNext := func() error {
		for n := head; n != nil; n = n.Previous {
			if err := ctx.Err(); err != nil {
				return err
			}
			size := len(n.Entry.Source)
			if longest {
				if size > lastLen || size == lastLen && n.Order <= lastOrder {
					continue
				}
			} else if n.Order <= lastOrder {
				continue
			}
			if err := b.ReserveNodes(1, 0); err != nil {
				return err
			}
			source := strings.TrimSpace(n.Entry.Source)
			if source == "" {
				continue
			}
			match, err := sourceContainsBounded(ctx, b, text, source, !n.Entry.CaseSensitive)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			if best == nil || longest && size > len(best.Entry.Source) || (!longest || size == len(best.Entry.Source)) && n.Order < best.Order {
				best = n
			}
		}
		return nil
	}
	for {
		best = nil
		var err error
		if run != nil {
			err = run(ctx, selectNext)
		} else {
			err = selectNext()
		}
		if err != nil {
			return err
		}
		if best == nil {
			return nil
		}
		lastOrder, lastLen = best.Order, len(best.Entry.Source)
		if err = yield(best.Entry); err != nil {
			return err
		}
	}
}
func (g *FileGlossary) VisitMatchesBounded(ctx context.Context, text, _, _ string, b *backend.BuildBounds, run func(context.Context, func() error) error, yield func(Entry) error) error {
	return VisitEntrySnapshotMatches(ctx, b, g.entrySnapshot.Load(), text, true, run, yield)
}
func (Nop) VisitMatchesBounded(context.Context, string, string, string, *backend.BuildBounds, func(context.Context, func() error) error, func(Entry) error) error {
	return nil
}
