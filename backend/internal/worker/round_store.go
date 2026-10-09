package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type roundStore struct {
	client     *ent.Client
	store      *workstate.Store
	scope      workstate.Scope
	indexToID  map[int]int
	idToIndex  map[int]int
	snapshot   *service.JobExecutionSnapshot
	round      int
	digest     string
	qa         *qa.Engine
	autoReject bool
	driver     string
	memory     tm.TranslationMemory
	onCommit   func()
}

func newRoundStore(client *ent.Client, scope workstate.Scope, indices map[int]int, snapshot *service.JobExecutionSnapshot, round int, qaEngine *qa.Engine, autoReject bool, driver string, memory tm.TranslationMemory, onCommit func()) *roundStore {
	data, _ := json.Marshal(snapshot)
	hash := sha256.Sum256(data)
	s := &roundStore{client: client, store: workstate.NewStore(client), scope: scope, indexToID: indices, idToIndex: map[int]int{}, snapshot: snapshot, round: round, digest: hex.EncodeToString(hash[:]), qa: qaEngine, autoReject: autoReject, driver: driver, memory: memory, onCommit: onCommit}
	for idx, id := range indices {
		s.idToIndex[id] = idx
	}
	return s
}
func (s *roundStore) ResourceIdentity() int { return s.scope.ResourceID }

type permanentStoreError struct{ error }

func (permanentStoreError) Permanent() bool { return true }
func (e permanentStoreError) Unwrap() error { return e.error }
func (s *roundStore) classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, workstate.ErrStale) || errors.Is(err, workstate.ErrStopped) || errors.Is(err, workstate.ErrManifest) || errors.Is(err, workstate.ErrCandidateVersion) || errors.Is(err, workstate.ErrBudget) || database.Classify(s.driver, err).Category == database.CategoryStructural {
		return permanentStoreError{err}
	}
	return err
}
func (s *roundStore) recovery(state workstate.RoundState) (pipeline.RoundRecovery, error) {
	r := pipeline.RoundRecovery{Sealed: state.Sealed, Cursors: map[int]pipeline.WorkCursor{}}
	if state.Sealed && state.Total != len(state.Members) {
		return r, permanentStoreError{fmt.Errorf("sealed manifest has %d members but total is %d: %w", len(state.Members), state.Total, workstate.ErrManifest)}
	}
	for _, id := range state.Members {
		idx, ok := s.idToIndex[id]
		if !ok {
			return r, permanentStoreError{workstate.ErrManifest}
		}
		r.Members = append(r.Members, idx)
	}
	for _, id := range state.Completed {
		idx, ok := s.idToIndex[id]
		if !ok {
			return r, permanentStoreError{workstate.ErrManifest}
		}
		r.Completed = append(r.Completed, idx)
	}
	for _, w := range state.Work {
		idx, ok := s.idToIndex[w.SegmentID]
		if !ok {
			return r, permanentStoreError{workstate.ErrManifest}
		}
		c := w.Cursor
		out := pipeline.WorkCursor{Pool: c.PoolIndex, Attempt: c.MainAttempts, LogicalAttempt: c.AlignmentAttempts, NetworkAttempt: c.AlignmentNetworkAttempts, Phase: c.PromptPhase, State: c.State}
		if c.NextAttemptAt != nil {
			out.NextAttemptAt = *c.NextAttemptAt
		}
		if c.State == "stale" || c.State == "rejected" {
			out.State = "unresolved"
			out.Pool = max(1, s.retryAttempts()+1)
		}
		r.Cursors[idx] = out
	}
	return r, nil
}
func (s *roundStore) Load(ctx context.Context) (pipeline.RoundRecovery, error) {
	state, err := s.store.LoadRound(ctx, s.scope)
	if err != nil {
		return pipeline.RoundRecovery{}, s.classify(err)
	}
	return s.recovery(state)
}
func (s *roundStore) Seal(ctx context.Context, indices []int) (pipeline.RoundRecovery, error) {
	ids := make([]int, len(indices))
	for i, idx := range indices {
		var ok bool
		ids[i], ok = s.indexToID[idx]
		if !ok {
			return pipeline.RoundRecovery{}, permanentStoreError{workstate.ErrManifest}
		}
	}
	state, err := s.store.SealRound(ctx, s.scope, ids)
	if err != nil {
		return pipeline.RoundRecovery{}, s.classify(err)
	}
	return s.recovery(state)
}
func (s *roundStore) Candidates(ctx context.Context, after, limit int) ([]*pipeline.Candidate, int, error) {
	rows, next, err := s.store.LoadCandidates(ctx, s.scope, after, limit)
	if err != nil {
		return nil, after, s.classify(err)
	}
	var candidates []*pipeline.Candidate
	for _, row := range rows {
		if row.SnapshotDigest != s.digest {
			return nil, after, permanentStoreError{fmt.Errorf("saved execution snapshot digest mismatch")}
		}
		c, err := pipeline.DecodeCandidate(row.Payload)
		if err != nil {
			return nil, after, permanentStoreError{err}
		}
		idx, ok := s.idToIndex[row.SegmentID]
		if !ok || c.Segment.DBID != row.SegmentID || c.ID != row.ID || c.Version != row.Version {
			return nil, after, permanentStoreError{workstate.ErrManifest}
		}
		c.Index = idx
		c.StoredBytes = int64(len(row.Payload))
		candidates = append(candidates, c)
		cur, curErr := s.current(ctx, idx)
		if curErr != nil {
			return nil, after, s.classify(curErr)
		}
		if c.RetryEpoch != s.scope.RetryEpoch {
			c.RetryEpoch = s.scope.RetryEpoch
			c.PoolIndex = cur.PoolIndex
			c.MainAttempt = cur.MainAttempts
			c.LogicalAttempt = 0
			c.NetworkAttempt = 0
			c.NextAttemptAt = time.Time{}
		}
		if !c.Ready && cur.AlignmentAttempts > c.LogicalAttempt {
			// An intent without a saved response is an unknown network attempt,
			// never a fresh logical round with a new network budget. Reconcile
			// after rebasing an old DTO too: Retry may have admitted a request
			// in the new epoch without saving an updated candidate yet.
			c.LogicalAttempt = max(c.LogicalAttempt, cur.AlignmentAttempts-1)
			c.NetworkAttempt = max(c.NetworkAttempt, cur.AlignmentNetworkAttempts)
		}
		if cur.NextAttemptAt != nil {
			c.NextAttemptAt = *cur.NextAttemptAt
		}
	}
	return candidates, next, nil
}
func candidateBaseline(c *pipeline.Candidate) *string {
	if c.Segment.TargetIsNull {
		return nil
	}
	target := c.BaselineTarget
	return &target
}
func (s *roundStore) Save(ctx context.Context, c *pipeline.Candidate) error {
	c.RetryEpoch = s.scope.RetryEpoch
	payload, err := c.Encode()
	if err != nil {
		return permanentStoreError{err}
	}
	state := "pending_alignment"
	if c.Ready {
		state = "ready_to_commit"
	}
	cur, err := s.current(ctx, c.Index)
	if err != nil {
		return s.classify(err)
	}
	cur.State = "candidate"
	cur.NextAttemptAt = nil
	if !c.NextAttemptAt.IsZero() {
		at := c.NextAttemptAt
		cur.NextAttemptAt = &at
	}
	if c.LogicalAttempt > 0 && c.NetworkAttempt == 0 && cur.AlignmentNetworkAttempts > 0 {
		cur.PromptPhase = "alignment_complete"
		cur.AlignmentNetworkAttempts = 0
		cur.NetworkAttempts = 0
	}
	// The payload and its successor budget/deadline form one handoff. A
	// failure must leave neither a hidden draft nor unaccounted window bytes.
	err = s.store.SaveCandidateWithCursor(ctx, workstate.Candidate{ID: c.ID, ParentRequestID: c.ParentRequestID, Version: c.Version, Scope: s.scope, SegmentID: c.Segment.DBID, DTOVersion: c.DTOVersion, Mode: c.Mode, SnapshotDigest: s.digest, BaselineVersion: c.Segment.ContentVersion, BaselineTarget: candidateBaseline(c), BaselineStatus: c.BaselineStatus, State: state, Payload: payload}, cur)
	if err == nil {
		c.StoredBytes = int64(len(payload))
	}
	return s.classify(err)
}

func (s *roundStore) retryAttempts() int {
	round := s.snapshot.Rounds[s.round]
	if round.Translate != nil {
		return round.Translate.Retry.MaxAttempts
	}
	if round.Revise != nil {
		return round.Revise.Retry.MaxAttempts
	}
	if round.Extract != nil {
		return round.Extract.Retry.MaxAttempts
	}
	if round.Adjudicate != nil {
		return round.Adjudicate.Retry.MaxAttempts
	}
	if round.SemanticQA != nil {
		return round.SemanticQA.Retry.MaxAttempts
	}
	return 0
}
func (s *roundStore) current(ctx context.Context, index int) (workstate.Cursor, error) {
	return s.store.LoadCursor(ctx, s.scope, s.indexToID[index])
}
func (s *roundStore) Cursor(ctx context.Context, index int, next pipeline.WorkCursor) error {
	cur, err := s.current(ctx, index)
	if err != nil {
		return s.classify(err)
	}
	if next.Pool > cur.PoolIndex {
		cur.PoolIndex = next.Pool
		cur.MainAttempts = 0
		cur.MainNetworkAttempts = 0
		cur.AlignmentAttempts = 0
		cur.AlignmentNetworkAttempts = 0
		cur.NetworkAttempts = 0
	}
	cur.MainAttempts = max(cur.MainAttempts, next.Attempt)
	cur.State = next.State
	cur.PromptPhase = next.Phase
	cur.NextAttemptAt = nil
	if !next.NextAttemptAt.IsZero() {
		at := next.NextAttemptAt
		cur.NextAttemptAt = &at
	}
	cur.Data, _ = json.Marshal(next)
	return s.classify(s.store.SaveCursor(ctx, s.scope, s.indexToID[index], cur))
}
func (s *roundStore) Reserve(ctx context.Context, in pipeline.RequestIntent) error {
	ids := make([]int, len(in.Indices))
	for i, idx := range in.Indices {
		id, ok := s.indexToID[idx]
		if !ok {
			return permanentStoreError{workstate.ErrManifest}
		}
		ids[i] = id
	}
	rubyBudget := 0
	if s.snapshot.RubyRetry != nil {
		rubyBudget = s.snapshot.RubyRetry.MaxAttempts
	}
	retry := s.retryAttempts()
	// Cursor advances are durable before a newly grouped request is reserved.
	for _, idx := range in.Indices {
		cur, err := s.current(ctx, idx)
		if err != nil {
			return s.classify(err)
		}
		if in.Pool > cur.PoolIndex {
			if err := s.Cursor(ctx, idx, pipeline.WorkCursor{Pool: in.Pool, State: "pending"}); err != nil {
				return err
			}
		}
	}
	model := "legacy_round_shared"
	if s.snapshot.SchemaVersion >= 2 {
		model = "stage_separated"
	}
	req := workstate.Request{ID: in.ID, CandidateID: in.CandidateID, Scope: s.scope, SegmentIDs: ids, Stage: string(in.Stage), BackendID: in.BackendID, BudgetModel: model, InputDigest: in.InputDigest,
		MainAttempt: in.Stage == backend.RequestStageMain && in.Phase != "prompt_upgrade", AlignmentAttempt: in.Stage == backend.RequestStageAlignment && in.NetworkAttempt == 0,
		MaxMainAttempts: min(max(1, retry+1), 3), MaxAlignmentAttempts: max(1, rubyBudget)}
	if in.Stage == backend.RequestStageAlignment {
		req.MaxNetworkAttempts = max(1, retry)
	}
	err := s.store.ReserveRequest(ctx, req)
	if errors.Is(err, workstate.ErrStopped) {
		row, readErr := s.client.Job.Get(ctx, s.scope.JobID)
		if readErr == nil && row.RetryEpoch == s.scope.RetryEpoch && row.Status != "cancelled" && (row.PauseRequested || row.Status == "pausing" || row.Status == "paused") {
			return permanentStoreError{backend.ErrDispatchPaused}
		}
	}
	return s.classify(err)
}
func (s *roundStore) Record(ctx context.Context, id string, r pipeline.RequestRecord) error {
	return s.classify(s.store.RecordRequest(ctx, id, workstate.RequestResult{State: r.State, UsageKnown: r.UsageKnown, InputTokens: r.Usage.PromptTokens, OutputTokens: r.Usage.CompletionTokens, DurationMS: r.Duration.Milliseconds(), LastError: r.Error}))
}
func (s *roundStore) AbortRequest(ctx context.Context, id string) error {
	return s.classify(s.store.AbortRequest(ctx, id))
}
func (s *roundStore) Retire(ctx context.Context, c *pipeline.Candidate, next pipeline.WorkCursor) error {
	cur, err := s.current(ctx, c.Index)
	if err != nil {
		return s.classify(err)
	}
	if next.Pool > cur.PoolIndex {
		cur = workstate.Cursor{PoolIndex: next.Pool}
	}
	cur.State = next.State
	return s.classify(s.store.RetireCandidate(ctx, s.scope, c.ID, c.Version, cur))
}
func (s *roundStore) ValidateCandidate(ctx context.Context, c *pipeline.Candidate) (bool, error) {
	row, err := s.client.Segment.Query().Where(segment.IDEQ(c.Segment.DBID)).Select(segment.FieldID, segment.FieldContentVersion, segment.FieldTargetText, segment.FieldStatus).Only(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, s.classify(err)
	}
	sameTarget := (row.TargetText == nil && c.Segment.TargetIsNull) || (row.TargetText != nil && !c.Segment.TargetIsNull && *row.TargetText == c.BaselineTarget)
	return row.ContentVersion == c.Segment.ContentVersion && string(row.Status) == c.BaselineStatus && sameTarget, nil
}
func (s *roundStore) Commit(ctx context.Context, c *pipeline.Candidate, result pipeline.TranslatedSegment) (pipeline.CommitOutcome, error) {
	issues := append([]qa.QualityIssue(nil), result.Issues...)
	status := string(service.SegmentStatusTranslated)
	noop := false
	clearReview := false
	if c.Mode == pipeline.RoundModeTranslate {
		if s.qa != nil {
			issues = append(issues, s.qa.Run(ctx, buildQACheckInputs(pipeline.BatchResult{Segments: []pipeline.TranslatedSegment{result}}))...)
		}
		if s.snapshot.AutoApprove {
			status = string(service.SegmentStatusApproved)
			clearReview = true
		}
		if qa.HasErrors(issues) && s.autoReject {
			status = string(service.SegmentStatusRejected)
		}
	} else {
		status = c.BaselineStatus
		noop = result.TargetText == c.BaselineTarget
		if !noop {
			var fresh []qa.QualityIssue
			if s.qa != nil {
				fresh = s.qa.Run(ctx, buildQACheckInputs(pipeline.BatchResult{Segments: []pipeline.TranslatedSegment{result}}))
			}
			issues = qa.ReviseFinalIssues(c.Segment.Issues, fresh, s.snapshot.Rounds[s.round].Revise.IssueCodes, s.qa != nil)
		} else {
			issues = c.Segment.Issues
		}
	}
	committed, err := s.store.Commit(ctx, workstate.CommitInput{Scope: s.scope, SegmentID: c.Segment.DBID, CommitID: c.ID, CandidateID: c.ID, CandidateVersion: c.Version, BaselineVersion: c.Segment.ContentVersion, BaselineTarget: candidateBaseline(c), BaselineStatus: c.BaselineStatus, Target: result.TargetText, Status: status, Issues: issues, ClearReview: clearReview, Noop: noop})
	outcome := pipeline.CommitOutcome(committed.Outcome)
	if err != nil {
		return outcome, s.classify(err)
	}
	if outcome == pipeline.CommitAccepted || outcome == pipeline.CommitNoop || outcome == pipeline.CommitExisting {
		c.Segment.Target = result.TargetText
		c.Segment.Issues = issues
		c.Segment.Status = status
		if committed.ContentVersion > 0 {
			c.Segment.ContentVersion = committed.ContentVersion
		}
	}
	if outcome == pipeline.CommitAccepted || outcome == pipeline.CommitNoop {
		if s.onCommit != nil {
			s.onCommit()
		}
		if s.memory != nil && c.Mode == pipeline.RoundModeTranslate {
			tmCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			_ = s.memory.Add(tmCtx, result.SourceText, result.TargetText, s.snapshot.SourceLang, s.snapshot.TargetLang)
		}
	}
	return outcome, nil
}
