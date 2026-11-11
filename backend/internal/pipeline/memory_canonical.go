package pipeline

import (
	"context"
	"fmt"
)

// Memory persistence keeps its durable records; execution reads them directly
// into the caller's single canonical document without another recovery tree.
func (s *MemoryRoundStore) LoadCanonical(_ context.Context, doc *Document) (bool, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range doc.Segments {
		doc.Segments[i].RoundWork.Member = false
		doc.Segments[i].RoundWork.Completed = false
		doc.Segments[i].RoundWork.Cursor = WorkCursor{}
	}
	for _, idx := range s.state.Members {
		if idx < 0 || idx >= len(doc.Segments) {
			return false, 0, fmt.Errorf("memory member outside canonical document")
		}
		doc.Segments[idx].RoundWork.Member = true
		doc.Segments[idx].RoundWork.Cursor = s.state.Cursors[idx]
	}
	for _, idx := range s.state.Completed {
		if idx < 0 || idx >= len(doc.Segments) {
			return false, 0, fmt.Errorf("memory completed member outside canonical document")
		}
		doc.Segments[idx].RoundWork.Completed = true
	}
	return s.state.Sealed, len(s.state.Members), nil
}
func (s *MemoryRoundStore) SealCanonical(_ context.Context, doc *Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Sealed {
		return nil
	}
	count := 0
	for i := range doc.Segments {
		if doc.Segments[i].RoundWork.Member {
			count++
		}
	}
	s.state.Members = make([]int, 0, count)
	for i := range doc.Segments {
		if doc.Segments[i].RoundWork.Member {
			s.state.Members = append(s.state.Members, i)
		}
	}
	s.state.Sealed = true
	return nil
}
