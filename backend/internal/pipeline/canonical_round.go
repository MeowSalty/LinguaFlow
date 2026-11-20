package pipeline

import "context"

type CanonicalRoundStore interface {
	LoadCanonical(context.Context, *Document) (sealed bool, total int, err error)
	SealCanonical(context.Context, *Document) error
}

type canonicalIndexLifetimeKey struct{}
type canonicalIndexLifetime struct{ close func() }

// The single cleanup slot follows the Load context through RoundStore wrappers,
// whose interface embedding can hide a concrete adapter's optional methods.
func withCanonicalIndexLifetime(ctx context.Context) (context.Context, func()) {
	lifetime := &canonicalIndexLifetime{}
	return context.WithValue(ctx, canonicalIndexLifetimeKey{}, lifetime), func() {
		close := lifetime.close
		lifetime.close = nil
		if close != nil {
			close()
		}
	}
}

// RetainCanonicalIndex binds an actual adapter's index to the current round.
// Direct Load callers have no round scope and close their adapter explicitly.
func RetainCanonicalIndex(ctx context.Context, close func()) {
	if lifetime, _ := ctx.Value(canonicalIndexLifetimeKey{}).(*canonicalIndexLifetime); lifetime != nil {
		lifetime.close = close
	}
}

func canonicalMembers(doc *Document, yield func(int) bool) {
	for i := range doc.Segments {
		if doc.Segments[i].RoundWork.Member && !yield(i) {
			return
		}
	}
}
func canonicalRemaining(doc *Document, yield func(int) bool) {
	for i := range doc.Segments {
		if w := doc.Segments[i].RoundWork; w.Member && !w.Completed && !yield(i) {
			return
		}
	}
}
func canonicalCompleted(doc *Document, yield func(int) bool) {
	for i := range doc.Segments {
		if w := doc.Segments[i].RoundWork; w.Member && w.Completed && !yield(i) {
			return
		}
	}
}
