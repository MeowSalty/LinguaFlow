package worker

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func (s *roundStore) LoadCanonical(ctx context.Context, doc *pipeline.Document) (bool, int, error) {
	header, err := s.store.LoadRoundHeader(ctx, s.scope)
	if err != nil {
		return false, 0, s.classify(err)
	}
	for i := range doc.Segments {
		doc.Segments[i].RoundWork = model.RoundWorkRecord{}
	}
	if !header.Sealed {
		return false, header.Total, nil
	}
	after, count := 0, 0
	for {
		page, next, err := s.store.LoadRoundMembersPage(ctx, s.scope, after, 16)
		if err != nil {
			return true, header.Total, s.classify(err)
		}
		if len(page) == 0 {
			break
		}
		if next <= after {
			return true, header.Total, workstate.ErrManifest
		}
		for _, w := range page {
			idx, ok := s.segmentIndex(w.SegmentID)
			if !ok || idx < 0 || idx >= len(doc.Segments) {
				return true, header.Total, workstate.ErrManifest
			}
			c := w.Cursor
			cursor := pipeline.WorkCursor{Pool: c.PoolIndex, Attempt: c.MainAttempts, LogicalAttempt: c.AlignmentAttempts, NetworkAttempt: c.AlignmentNetworkAttempts, Phase: c.PromptPhase, State: c.State}
			if c.NextAttemptAt != nil {
				cursor.NextAttemptAt = *c.NextAttemptAt
			}
			if c.State == "stale" || c.State == "rejected" {
				cursor.State = "unresolved"
				cursor.Pool = max(1, s.retryAttempts()+1)
			}
			doc.Segments[idx].RoundWork = model.RoundWorkRecord{Member: true, Completed: c.State == "resolved", Cursor: cursor}
			count++
		}
		after = next
	}
	if count != header.Total {
		return true, header.Total, workstate.ErrManifest
	}
	return true, count, nil
}
func (s *roundStore) SealCanonical(ctx context.Context, doc *pipeline.Document) error {
	total := 0
	for i := range doc.Segments {
		if doc.Segments[i].RoundWork.Member {
			total++
		}
	}
	walk := func(yield func(int) error) error {
		for i := range doc.Segments {
			if doc.Segments[i].RoundWork.Member {
				id, ok := s.segmentID(i)
				if !ok {
					return workstate.ErrManifest
				}
				if err := yield(id); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return s.classify(s.store.SealRoundStream(ctx, s.scope, total, walk))
}
func (s *roundStore) segmentIndex(id int) (int, bool) {
	if s.canonical != nil {
		for i := range s.canonical.Segments {
			if s.canonical.Segments[i].DBID == id {
				return i, true
			}
		}
		return 0, false
	}
	for index, value := range s.indexToID {
		if value == id {
			return index, true
		}
	}
	return 0, false
}
func (s *roundStore) segmentID(index int) (int, bool) {
	if s.canonical != nil {
		return canonicalDBIdentity(s.canonical, index)
	}
	id, ok := s.indexToID[index]
	return id, ok
}
func (s *roundStore) bindCanonical(doc *pipeline.Document) { s.canonical = doc; s.indexToID = nil }
