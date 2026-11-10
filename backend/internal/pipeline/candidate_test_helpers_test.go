package pipeline

import "github.com/MeowSalty/LinguaFlow/backend/internal/backend"

func newCandidate(seg Segment, index int, mode, format string) *Candidate {
	c, err := newCandidateChecked(seg, index, mode, format, "")
	if err != nil {
		panic(err)
	}
	return c
}
func snapshotDocument(doc *Document, idxs []int) *Document {
	b, _ := backend.NewBuildBounds(64<<20, 0)
	var out Document
	if err := b.CloneInto(*doc, &out); err != nil {
		panic(err)
	}
	return &out
}
