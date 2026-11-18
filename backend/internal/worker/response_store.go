package worker

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func pipelineResponseHeader(h workstate.ResponseHeader) pipeline.MainResponseHeader {
	return pipeline.MainResponseHeader{FrozenContext: pipeline.FrozenContext{Pool: h.MemberPool, MainAttempt: h.MemberAttempt, Mode: h.MemberMode, Format: h.MemberFormat, SourceLang: h.SourceLang, TargetLang: h.TargetLang, TextMode: h.TextMode}, CaptureEpoch: h.Scope.RetryEpoch, FormatVersion: h.FormatVersion, ArchiveCursor: h.ArchiveCursor, ArchiveDigest: h.ArchiveDigest, InputMemberBytes: h.InputMemberBytes, OutputMemberBytes: h.OutputMemberBytes, PublicBytes: h.PublicBytes, MetadataBytes: h.MetadataBytes, ID: h.RequestID, DatabaseID: h.DatabaseID, ResourceID: h.Scope.ResourceID, RoundID: h.Scope.RoundID, SnapshotDigest: h.SnapshotDigest, FreezeDigest: h.FreezeDigest, ResponseDigest: h.ResponseDigest, FreezeBytes: h.FreezeBytes, ResponseBytes: h.ResponseBytes, CorrectedBytes: h.CorrectedBytes, BusinessBound: h.BusinessBound, MemberCount: h.MemberCount, Cursor: h.Cursor, Phase: h.Phase, Truncated: h.Truncated}
}

func (s *roundStore) ResponseHeaders(ctx context.Context, after, limit int) ([]pipeline.MainResponseHeader, int, error) {
	for {
		rows, next, err := s.store.LoadResponseHeaders(ctx, s.scope.JobID, after, limit)
		if err != nil {
			return nil, after, s.classify(err)
		}
		var out []pipeline.MainResponseHeader
		for _, h := range rows {
			if h.Scope.ResourceID == s.scope.ResourceID && h.Scope.RoundID == s.scope.RoundID {
				if h.SnapshotDigest != s.digest {
					return nil, after, permanentStoreError{workstate.ErrManifest}
				}
				out = append(out, pipelineResponseHeader(h))
			}
		}
		if len(out) > 0 || len(rows) == 0 {
			return out, next, nil
		}
		after = next
	}
}

func (s *roundStore) PromoteMember(ctx context.Context, key pipeline.MemberKey, c *pipeline.Candidate, next pipeline.WorkCursor, failure string) error {
	id, cursor := key.RequestID, key.Ordinal
	var candidate *workstate.Candidate
	var idx int
	if c != nil {
		idx = c.Index
		payload, err := c.Encode()
		if err != nil {
			return permanentStoreError{err}
		}
		c.StoredBytes = int64(len(payload))
		header, err := c.EncodeHeader()
		if err != nil {
			return permanentStoreError{err}
		}
		state := "pending_alignment"
		if c.Ready {
			state = "ready_to_commit"
		}
		limit := pipeline.DefaultCandidateLimits().ItemBytes
		if runtime := pipeline.ExecutionRuntimeFromContext(ctx); runtime != nil {
			limit = runtime.BudgetLimits().Item
		}
		candidate = &workstate.Candidate{PayloadLimit: limit, PipelineHeader: header, ID: c.ID, WorkID: s.WorkIdentity(c.Index), ParentRequestID: id, Version: c.Version, Scope: s.scope, SegmentID: c.Segment.DBID, DTOVersion: c.DTOVersion, Mode: c.Mode, SnapshotDigest: s.digest, BaselineVersion: c.Segment.ContentVersion, BaselineTarget: candidateBaseline(c), BaselineDigest: c.BaselineDigest, BaselineTargetIsNull: c.Segment.TargetIsNull, BaselineStatus: c.BaselineStatus, State: state, Payload: payload}
	} else {
		member, err := s.store.InputMemberIdentity(ctx, id, cursor)
		if err != nil {
			return s.classify(err)
		}
		defer func() {
			release := member.ReleaseOwned
			member = workstate.PromotionMember{}
			if release != nil {
				release()
			}
		}()
		var ok bool
		idx, ok = s.segmentIndex(member.SegmentID)
		if !ok {
			return permanentStoreError{workstate.ErrManifest}
		}
	}
	cur, err := s.current(ctx, idx)
	if err != nil {
		return s.classify(err)
	}
	cur, err = cursorTransition(cur, next)
	if err != nil {
		return s.classify(err)
	}
	cur.LastError = failure
	err = s.store.PromoteMember(ctx, workstate.MemberPromotion{Scope: s.scope, Key: storageMemberKey(key), Candidate: candidate, Next: cur, Failure: failure})
	if err == nil && c != nil {
		c.StoredBytes = int64(len(candidate.Payload))
	}
	return s.classify(err)
}
