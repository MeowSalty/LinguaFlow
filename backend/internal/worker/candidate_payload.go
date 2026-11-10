package worker

import (
	"context"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func (s *roundStore) CandidateHeaders(ctx context.Context, after, limit int) ([]*pipeline.Candidate, int, error) {
	for {
		rows, next, err := s.store.LoadCandidateHeaders(ctx, s.scope.JobID, after, limit)
		if err != nil {
			return nil, after, s.classify(err)
		}
		var out []*pipeline.Candidate
		for _, row := range rows {
			if row.Scope.RoundID != s.scope.RoundID || row.Scope.ResourceID != s.scope.ResourceID {
				continue
			}
			if row.DTOVersion != 2 || row.SnapshotDigest != s.digest {
				return nil, after, permanentStoreError{workstate.ErrManifest}
			}
			c, err := pipeline.DecodeCandidateHeader(row.PipelineHeader)
			if err != nil {
				return nil, after, permanentStoreError{err}
			}
			idx, ok := s.segmentIndex(row.SegmentID)
			if !ok || c.ID != row.ID || c.Version != row.Version {
				return nil, after, permanentStoreError{workstate.ErrManifest}
			}
			c.Index = idx
			c.StoredBytes = row.PayloadBytes
			if err := s.reconcileCandidate(ctx, c); err != nil {
				return nil, after, err
			}
			out = append(out, c)
		}
		if len(out) > 0 || len(rows) == 0 {
			return out, next, nil
		}
		after = next
	}
}
func (s *roundStore) Candidate(ctx context.Context, id string, maxBytes int64) (*pipeline.Candidate, error) {
	row, err := s.store.LoadCandidatePayload(ctx, s.scope, id, maxBytes)
	if err != nil {
		return nil, s.classify(err)
	}
	if row.SnapshotDigest != s.digest {
		return nil, permanentStoreError{workstate.ErrManifest}
	}
	var c *pipeline.Candidate
	decode := func() error {
		var err error
		c, err = pipeline.DecodeCandidateBounded(row.Payload, maxBytes, 0)
		return err
	}
	if runtime := pipeline.ExecutionRuntimeFromContext(ctx); runtime != nil {
		err = runtime.Run(ctx, decode)
	} else {
		err = decode()
	}
	if err != nil {
		return nil, permanentStoreError{err}
	}
	idx, ok := s.segmentIndex(row.SegmentID)
	if !ok || c.ID != row.ID || c.Version != row.Version || c.Segment.DBID != row.SegmentID {
		return nil, permanentStoreError{workstate.ErrManifest}
	}
	c.Index = idx
	c.StoredBytes = int64(len(row.Payload))
	if err := s.reconcileCandidate(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}
func (s *roundStore) reconcileCandidate(ctx context.Context, c *pipeline.Candidate) error {
	if c.WorkID != "" && c.WorkID != s.WorkIdentity(c.Index) {
		return permanentStoreError{workstate.ErrManifest}
	}
	c.WorkID = s.WorkIdentity(c.Index)
	cur, err := s.current(ctx, c.Index)
	if err != nil {
		return s.classify(err)
	}
	if c.RetryEpoch != s.scope.RetryEpoch {
		c.RetryEpoch = s.scope.RetryEpoch
		c.PoolIndex = cur.PoolIndex
		c.MainAttempt = cur.MainAttempts
		c.LogicalAttempt = 0
		c.NetworkAttempt = 0
		c.NextAttemptAt = time.Time{}
		c.LastAlignmentRequestID = ""
	}
	if !c.Ready && cur.AlignmentAttempts > c.LogicalAttempt {
		c.LogicalAttempt = max(c.LogicalAttempt, cur.AlignmentAttempts-1)
		c.NetworkAttempt = max(c.NetworkAttempt, cur.AlignmentNetworkAttempts)
	}
	if cur.NextAttemptAt != nil {
		c.NextAttemptAt = *cur.NextAttemptAt
	}
	return nil
}

func (s *roundStore) CandidateFrame(ctx context.Context, id string, maxBytes int64) (pipeline.CandidateFrame, error) {
	row, err := s.store.LoadCandidatePayload(ctx, s.scope, id, maxBytes)
	if err != nil {
		return pipeline.CandidateFrame{}, s.classify(err)
	}
	if row.SnapshotDigest != s.digest {
		return pipeline.CandidateFrame{}, permanentStoreError{workstate.ErrManifest}
	}
	idx, ok := s.segmentIndex(row.SegmentID)
	if !ok {
		return pipeline.CandidateFrame{}, permanentStoreError{workstate.ErrManifest}
	}
	cur, err := s.current(ctx, idx)
	if err != nil {
		return pipeline.CandidateFrame{}, s.classify(err)
	}
	f := pipeline.CandidateFrame{Payload: row.Payload, ID: row.ID, WorkID: s.WorkIdentity(idx), Version: row.Version, Index: idx, SegmentID: row.SegmentID, Reconcile: true, Epoch: s.scope.RetryEpoch, Pool: cur.PoolIndex, MainAttempt: cur.MainAttempts, LogicalAttempt: cur.AlignmentAttempts, NetworkAttempt: cur.AlignmentNetworkAttempts}
	if cur.NextAttemptAt != nil {
		f.Next = *cur.NextAttemptAt
	}
	return f, nil
}
