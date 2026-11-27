package pipeline

func newCandidate(seg Segment, index int, mode, format string) *Candidate {
	c, err := newCandidateChecked(seg, index, mode, format, "")
	if err != nil {
		panic(err)
	}
	return c
}
