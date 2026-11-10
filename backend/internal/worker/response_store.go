package worker

import (
	"context"
	"errors"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func (s *roundStore) HandoffResponse(ctx context.Context, in pipeline.MainResponse) error {
	members := make([]workstate.PromotionMember, 0, len(in.Members))
	for _, m := range in.Members {
		id, ok := s.segmentID(m.Index)
		if !ok {
			return permanentStoreError{workstate.ErrManifest}
		}
		members = append(members, workstate.PromotionMember{SegmentID: id, WorkID: s.WorkIdentity(m.Index), CandidateID: m.CandidateID, BaselineVersion: m.BaselineVersion, BaselineTarget: m.BaselineTarget, BaselineStatus: m.BaselineStatus, SourceGeneration: s.scope.SourceGeneration, SourceRevisionID: s.scope.SourceRevisionID})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].SegmentID < members[j].SegmentID })
	r := in.Record
	result := workstate.RequestResult{UsageKnown: r.UsageKnown, InputTokens: r.Usage.PromptTokens, OutputTokens: r.Usage.CompletionTokens, DurationMS: r.Duration.Milliseconds(), LastError: r.Error}
	err := s.store.HandoffResponse(ctx, workstate.ResponseHandoff{RequestID: in.ID, Scope: s.scope, FreezePayload: in.FreezePayload, ResponsePayload: in.Raw, SnapshotDigest: s.digest, Members: members, BusinessBound: in.BusinessBound, Truncated: in.Truncated, Result: result})
	if errors.Is(err, workstate.ErrStopped) {
		// A response arriving after Cancel cannot become recoverable work. Its
		// already dispatched request still owns the provider's factual usage.
		result.State, result.LastError = "failed", err.Error()
		if accountingErr := s.store.RecordRequestForScope(ctx, s.scope, in.ID, result); accountingErr != nil {
			err = errors.Join(err, accountingErr)
		}
	}
	return s.classify(err)
}

func pipelineResponseHeader(h workstate.ResponseHeader) pipeline.MainResponseHeader {
	return pipeline.MainResponseHeader{ID: h.RequestID, DatabaseID: h.DatabaseID, ResourceID: h.Scope.ResourceID, RoundID: h.Scope.RoundID, SnapshotDigest: h.SnapshotDigest, FreezeDigest: h.FreezeDigest, ResponseDigest: h.ResponseDigest, FreezeBytes: h.FreezeBytes, ResponseBytes: h.ResponseBytes, CorrectedBytes: h.CorrectedBytes, BusinessBound: h.BusinessBound, MemberCount: h.MemberCount, Cursor: h.Cursor, Phase: h.Phase, Truncated: h.Truncated}
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

func (s *roundStore) Response(ctx context.Context, id string, freeze, raw, business int64) (pipeline.MainResponse, error) {
	in, err := s.store.LoadResponse(ctx, id, freeze, raw, business)
	if err != nil {
		return pipeline.MainResponse{}, s.classify(err)
	}
	if in.Scope.JobID != s.scope.JobID || in.Scope.ResourceID != s.scope.ResourceID || in.Scope.RoundID != s.scope.RoundID || in.SnapshotDigest != s.digest {
		return pipeline.MainResponse{}, permanentStoreError{workstate.ErrManifest}
	}
	out := pipeline.MainResponse{MainResponseHeader: pipelineResponseHeader(in.ResponseHeader), FreezePayload: in.FreezePayload, Raw: in.ResponsePayload, CorrectedTargets: in.CorrectedTargets, CorrectedIndex: in.CorrectedIndex, Receipt: in.Receipt}
	for _, m := range in.Members {
		idx, ok := s.segmentIndex(m.SegmentID)
		if !ok {
			return pipeline.MainResponse{}, permanentStoreError{workstate.ErrManifest}
		}
		out.Members = append(out.Members, pipeline.ResponseMember{Index: idx, SegmentID: m.SegmentID, WorkID: m.WorkID, CandidateID: m.CandidateID, BaselineVersion: m.BaselineVersion, BaselineTarget: m.BaselineTarget, BaselineStatus: m.BaselineStatus, SourceGeneration: m.SourceGeneration, SourceRevisionID: m.SourceRevisionID})
	}
	return out, nil
}

func (s *roundStore) PromoteMember(ctx context.Context, id string, cursor int, c *pipeline.Candidate, next pipeline.WorkCursor, failure string) error {
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
		member, err := s.store.LoadResponseMember(ctx, id, cursor)
		if err != nil {
			return s.classify(err)
		}
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
	err = s.store.PromoteMember(ctx, workstate.MemberPromotion{Scope: s.scope, RequestID: id, Cursor: cursor, Candidate: candidate, Next: cur, Failure: failure})
	if err == nil && c != nil {
		c.StoredBytes = int64(len(candidate.Payload))
	}
	return s.classify(err)
}
