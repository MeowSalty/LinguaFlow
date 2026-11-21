package pipeline

import (
	"context"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// Memory confirmation inventory survives the previous document and admission.
// Restore each completed field into its current receiver before completing it.
func (s *MemoryRoundStore) LoadCanonical(ctx context.Context, doc *Document) (bool, int, error) {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, 0, backend.ErrAdmissionClosed
	}
	sameOwner := s.canonicalDocument == doc && s.canonicalRuntime == ExecutionRuntimeFromContext(ctx)
	for i := range doc.Segments {
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "canonical_load", Operation: "doc_member_visit", Rows: 1})
		doc.Segments[i].RoundWork.Member = false
		if !sameOwner {
			doc.Segments[i].RoundWork.Completed = false
		}
		doc.Segments[i].RoundWork.Cursor = WorkCursor{}
	}
	for _, idx := range s.state.Members {
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "canonical_load", Operation: "doc_member_visit", Rows: 1})
		if idx < 0 || idx >= len(doc.Segments) {
			return false, 0, fmt.Errorf("memory member outside canonical document")
		}
		doc.Segments[idx].RoundWork.Member = true
		doc.Segments[idx].RoundWork.Cursor = s.state.Cursors[idx]
	}
	for _, idx := range s.state.Completed {
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "canonical_load", Operation: "doc_member_visit", Rows: 1})
		if idx < 0 || idx >= len(doc.Segments) {
			return false, 0, fmt.Errorf("memory completed member outside canonical document")
		}
		if sameOwner && doc.Segments[idx].RoundWork.Completed {
			continue
		}
		record := s.confirmations[idx]
		if record == nil {
			return false, 0, fmt.Errorf("memory completed member has no confirmation")
		}
		s.mu.Unlock()
		err := s.restoreMemoryConfirmation(ctx, doc, idx, record)
		s.mu.Lock()
		if err != nil {
			return false, 0, err
		}
	}
	s.canonicalDocument, s.canonicalRuntime = doc, ExecutionRuntimeFromContext(ctx)
	return s.state.Sealed, len(s.state.Members), nil
}
func (s *MemoryRoundStore) SealCanonical(ctx context.Context, doc *Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return backend.ErrAdmissionClosed
	}
	if s.canonicalDocument == nil {
		s.canonicalDocument = doc
	}
	if s.state.Sealed {
		return nil
	}
	count := 0
	for i := range doc.Segments {
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "canonical_seal", Operation: "doc_member_visit", Rows: 1})
		if doc.Segments[i].RoundWork.Member {
			count++
		}
	}
	s.state.Members = make([]int, 0, count)
	for i := range doc.Segments {
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "canonical_seal", Operation: "doc_member_visit", Rows: 1})
		if doc.Segments[i].RoundWork.Member {
			s.state.Members = append(s.state.Members, i)
		}
	}
	s.state.Sealed = true
	return nil
}
