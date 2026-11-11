package pipeline

import "context"

type CanonicalRoundStore interface {
	LoadCanonical(context.Context, *Document) (sealed bool, total int, err error)
	SealCanonical(context.Context, *Document) error
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
