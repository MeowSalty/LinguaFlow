package workstate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

type Store struct{ client *ent.Client }

func NewStore(client *ent.Client) *Store { return &Store{client: client} }

func guardScope(ctx context.Context, tx *ent.Client, scope Scope, dispatch bool) error {
	if err := LockJob(ctx, tx, scope.JobID); err != nil {
		return err
	}
	j, err := tx.Job.Get(ctx, scope.JobID)
	if err != nil {
		return err
	}
	if j.RetryEpoch != scope.RetryEpoch || (j.Status != "running" && j.Status != "pausing" && j.Status != "pending") {
		return ErrStopped
	}
	if dispatch && (j.PauseRequested || j.Status == "pausing") {
		return ErrStopped
	}
	n, err := tx.Resource.Update().Where(resource.IDEQ(scope.ResourceID), resource.SourceGenerationEQ(scope.SourceGeneration)).AddTranslationGeneration(0).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStale
	}
	r, err := tx.Resource.Get(ctx, scope.ResourceID)
	if err != nil {
		return err
	}
	if !sameInt(r.CurrentSourceRevisionID, scope.SourceRevisionID) {
		return ErrStale
	}
	round, err := tx.JobRound.Get(ctx, scope.RoundID)
	if err != nil {
		return err
	}
	if round.JobID != scope.JobID || (scope.JobResourceID != 0 && round.JobResourceID != scope.JobResourceID) {
		return ErrManifest
	}
	jr, err := tx.JobResource.Get(ctx, round.JobResourceID)
	if err != nil {
		return err
	}
	belongs, err := jr.QueryResource().Where(resource.IDEQ(scope.ResourceID)).Exist(ctx)
	if err != nil {
		return err
	}
	if !belongs {
		return ErrManifest
	}
	return nil
}

func sameInt(a, b *int) bool       { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func sameString(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// SealRound freezes original membership before dispatch. A resumed caller may
// pass the same original set; it cannot redefine the manifest as remaining work.
func (s *Store) SealRound(ctx context.Context, scope Scope, ids []int) (RoundState, error) {
	unique := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return RoundState{}, ErrManifest
		}
		unique[id] = struct{}{}
	}
	ids = make([]int, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, scope, true); err != nil {
			return err
		}
		round, err := tx.JobRound.Get(ctx, scope.RoundID)
		if err != nil {
			return err
		}
		if round.ManifestSealed {
			rows, err := tx.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID)).Select(workitem.FieldSegmentID).All(ctx)
			if err != nil {
				return err
			}
			if len(rows) != len(ids) {
				return ErrManifest
			}
			for _, row := range rows {
				if _, ok := unique[row.SegmentID]; !ok {
					return ErrManifest
				}
			}
			return nil
		}
		count, err := tx.Segment.Query().Where(segment.IDIn(ids...), segment.ResourceIDEQ(scope.ResourceID)).Count(ctx)
		if err != nil {
			return err
		}
		if count != len(ids) {
			return ErrManifest
		}
		// Legacy totals cannot be silently replaced by a remaining-only selection.
		if round.SegmentTotal > 0 && round.SegmentTotal != len(ids) {
			return fmt.Errorf("%w: legacy round total %d, recovered members %d", ErrManifest, round.SegmentTotal, len(ids))
		}
		completed, err := tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(scope.RoundID)).All(ctx)
		if err != nil {
			return err
		}
		done := make(map[int]bool, len(completed))
		for _, row := range completed {
			if _, ok := unique[row.SegmentID]; !ok {
				return ErrManifest
			}
			done[row.SegmentID] = true
		}
		for _, id := range ids {
			state := "pending"
			if done[id] {
				state = "resolved"
			}
			if err := tx.WorkItem.Create().SetJobID(scope.JobID).SetResourceID(scope.ResourceID).SetJobRoundID(scope.RoundID).SetSegmentID(id).SetRetryEpoch(scope.RetryEpoch).SetState(state).Exec(ctx); err != nil {
				return err
			}
		}
		if err := tx.JobRound.UpdateOneID(scope.RoundID).SetManifestVersion(1).SetManifestSealed(true).SetSegmentTotal(len(ids)).Exec(ctx); err != nil {
			return err
		}
		return Calibrate(ctx, tx, scope.JobID)
	})
	if err != nil {
		return RoundState{}, err
	}
	return s.LoadRound(ctx, scope)
}

func (s *Store) LoadRound(ctx context.Context, scope Scope) (RoundState, error) {
	round, err := s.client.JobRound.Get(ctx, scope.RoundID)
	if err != nil {
		return RoundState{}, err
	}
	if round.JobID != scope.JobID {
		return RoundState{}, ErrManifest
	}
	state := RoundState{Sealed: round.ManifestSealed, ManifestVersion: round.ManifestVersion, Total: round.SegmentTotal, PoolIndex: round.PoolIndex}
	rows, err := s.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID)).Order(ent.Asc(workitem.FieldSegmentID)).All(ctx)
	if err != nil {
		return RoundState{}, err
	}
	for _, row := range rows {
		state.Members = append(state.Members, row.SegmentID)
		state.Work = append(state.Work, Work{SegmentID: row.SegmentID, RetryEpoch: row.RetryEpoch, CandidateID: row.CandidateID, Cursor: cursorFromRow(row)})
	}
	links, err := s.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(scope.RoundID)).All(ctx)
	if err != nil {
		return RoundState{}, err
	}
	for _, row := range links {
		state.Completed = append(state.Completed, row.SegmentID)
	}
	return state, nil
}

func cursorFromRow(row *ent.WorkItem) Cursor {
	return Cursor{State: row.State, PoolIndex: row.PoolIndex, MainAttempts: row.MainAttempts, AlignmentAttempts: row.AlignmentAttempts, NetworkAttempts: row.NetworkAttempts, MainNetworkAttempts: row.MainNetworkAttempts, AlignmentNetworkAttempts: row.AlignmentNetworkAttempts, PromptPhase: row.PromptPhase, NextAttemptAt: row.NextAttemptAt, LastError: row.LastError, Data: row.Cursor}
}

func member(ctx context.Context, tx *ent.Client, scope Scope, id int) (*ent.WorkItem, error) {
	row, err := tx.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID), workitem.SegmentIDEQ(id), workitem.JobIDEQ(scope.JobID), workitem.ResourceIDEQ(scope.ResourceID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrManifest
	}
	if err != nil {
		return nil, err
	}
	if row.RetryEpoch != scope.RetryEpoch {
		return nil, ErrStopped
	}
	return row, nil
}

func (s *Store) SaveCursor(ctx context.Context, scope Scope, segmentID int, cursor Cursor) error {
	return Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, scope, false); err != nil {
			return err
		}
		return saveCursor(ctx, tx, scope, segmentID, cursor)
	})
}

func saveCursor(ctx context.Context, tx *ent.Client, scope Scope, segmentID int, cursor Cursor) error {
	row, err := member(ctx, tx, scope, segmentID)
	if err != nil {
		return err
	}
	if row.State == "resolved" {
		return nil
	}
	newPool := cursor.PoolIndex > row.PoolIndex
	newAlignment := cursor.AlignmentAttempts > row.AlignmentAttempts
	alignmentCompleted := false
	if cursor.PromptPhase == "alignment_complete" && cursor.AlignmentAttempts == row.AlignmentAttempts && row.CandidateID != "" {
		alignmentCompleted, err = tx.WorkRequest.Query().Where(workrequest.JobRoundIDEQ(scope.RoundID), workrequest.RetryEpochEQ(scope.RetryEpoch), workrequest.CandidateIDEQ(row.CandidateID), workrequest.LogicalAttemptEQ(cursor.AlignmentAttempts), workrequest.StageIn("ruby_alignment", "alignment"), workrequest.StateIn("received", "completed")).Exist(ctx)
		if err != nil {
			return err
		}
	}
	if cursor.PoolIndex < row.PoolIndex || (!newPool && (cursor.MainAttempts < row.MainAttempts || cursor.AlignmentAttempts < row.AlignmentAttempts)) || (!newPool && !newAlignment && !alignmentCompleted && cursor.NetworkAttempts < row.NetworkAttempts) {
		return ErrStale
	}
	u := tx.WorkItem.UpdateOneID(row.ID).SetPoolIndex(cursor.PoolIndex).SetMainAttempts(cursor.MainAttempts).SetAlignmentAttempts(cursor.AlignmentAttempts).SetNetworkAttempts(cursor.NetworkAttempts).SetLastError(cursor.LastError)
	if newPool {
		u.SetMainNetworkAttempts(0).SetAlignmentNetworkAttempts(0)
	} else if newAlignment || alignmentCompleted {
		u.SetAlignmentNetworkAttempts(0)
	}
	if cursor.State != "" {
		u.SetState(cursor.State)
	}
	if cursor.PromptPhase != "" {
		u.SetPromptPhase(cursor.PromptPhase)
	}
	if cursor.NextAttemptAt == nil {
		u.ClearNextAttemptAt()
	} else {
		u.SetNextAttemptAt(*cursor.NextAttemptAt)
	}
	if len(cursor.Data) > 0 {
		u.SetCursor(cursor.Data)
	}
	if err := u.Exec(ctx); err != nil {
		return err
	}
	if cursor.State == "rejected" || cursor.State == "stale" || cursor.State == "unresolved" {
		if _, err := tx.WorkCandidate.Update().Where(workcandidate.WorkItemIDEQ(row.ID)).SetState(cursor.State).ClearPayload().SetPayloadBytes(0).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SavePool(ctx context.Context, scope Scope, pool int) error {
	return Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, scope, false); err != nil {
			return err
		}
		row, err := tx.JobRound.Get(ctx, scope.RoundID)
		if err != nil {
			return err
		}
		if pool < row.PoolIndex {
			return ErrStale
		}
		return tx.JobRound.UpdateOneID(row.ID).SetPoolIndex(pool).Exec(ctx)
	})
}

func lockSegment(ctx context.Context, tx *ent.Client, scope Scope, id int, version int64) (*ent.Segment, error) {
	if version < 1 {
		return nil, ErrStale
	}
	n, err := tx.Segment.Update().Where(segment.IDEQ(id), segment.ResourceIDEQ(scope.ResourceID), segment.ContentVersionEQ(version)).AddContentVersion(0).Save(ctx)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrStale
	}
	return tx.Segment.Get(ctx, id)
}

func (s *Store) SaveCandidate(ctx context.Context, c Candidate) error {
	return s.saveCandidate(ctx, c, nil)
}

// SaveCandidateWithCursor durably hands off both the candidate payload and its
// attempt cursor. An identical candidate replay still checks and saves the
// cursor, so retries can never acknowledge a partially completed handoff.
func (s *Store) SaveCandidateWithCursor(ctx context.Context, c Candidate, cursor Cursor) error {
	return s.saveCandidate(ctx, c, &cursor)
}

func (s *Store) saveCandidate(ctx context.Context, c Candidate, cursor *Cursor) error {
	if c.ID == "" || c.Version < 1 || c.DTOVersion < 1 || c.SnapshotDigest == "" || !json.Valid(c.Payload) {
		return fmt.Errorf("invalid candidate")
	}
	if c.State == "" {
		c.State = "pending_alignment"
	}
	return Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := saveCandidate(ctx, tx, c); err != nil {
			return err
		}
		if cursor != nil {
			return saveCursor(ctx, tx, c.Scope, c.SegmentID, *cursor)
		}
		return nil
	})
}

func saveCandidate(ctx context.Context, tx *ent.Client, c Candidate) error {
	if err := guardScope(ctx, tx, c.Scope, false); err != nil {
		return err
	}
	w, err := member(ctx, tx, c.Scope, c.SegmentID)
	if err != nil {
		return err
	}
	if w.State == "resolved" {
		return ErrStale
	}
	old, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(c.ID)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	j, err := tx.Job.Get(ctx, c.Scope.JobID)
	if err != nil {
		return err
	}
	if j.PauseRequested || j.Status == "pausing" {
		if err := guardCandidateHandoff(ctx, tx, c, old); err != nil {
			return err
		}
	}
	row, err := lockSegment(ctx, tx, c.Scope, c.SegmentID, c.BaselineVersion)
	if err != nil {
		return err
	}
	if !sameString(row.TargetText, c.BaselineTarget) || string(row.Status) != c.BaselineStatus {
		return ErrStale
	}
	if old != nil {
		if old.WorkItemID != w.ID || old.BaselineVersion != c.BaselineVersion || old.SnapshotDigest != c.SnapshotDigest || old.DtoVersion != c.DTOVersion || old.Mode != c.Mode || old.SourceGeneration != c.Scope.SourceGeneration || !sameInt(old.SourceRevisionID, c.Scope.SourceRevisionID) {
			return ErrCandidateVersion
		}
		if old.State == "completed" || old.State == "stale" || old.State == "rejected" {
			return ErrCandidateVersion
		}
		if c.Version < old.Version {
			return ErrCandidateVersion
		}
		if c.Version == old.Version {
			if string(c.Payload) != string(old.Payload) || c.State != old.State {
				return ErrCandidateVersion
			}
			return nil
		}
		if c.Version != old.Version+1 || w.CandidateID != c.ID {
			return ErrCandidateVersion
		}
		if err := tx.WorkCandidate.UpdateOneID(old.ID).SetVersion(c.Version).SetState(c.State).SetPayload(c.Payload).SetPayloadBytes(int64(len(c.Payload))).Exec(ctx); err != nil {
			return err
		}
	} else {
		if c.Version != 1 {
			return ErrCandidateVersion
		}
		if w.CandidateID != "" && w.CandidateID != c.ID {
			active, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(w.CandidateID), workcandidate.StateIn("pending_alignment", "ready_to_commit")).Exist(ctx)
			if err != nil {
				return err
			}
			if active {
				return ErrCandidateVersion
			}
		}
		if err := tx.WorkCandidate.Create().SetIdentity(c.ID).SetParentRequestID(c.ParentRequestID).SetVersion(c.Version).SetWorkItemID(w.ID).SetDtoVersion(c.DTOVersion).SetSnapshotDigest(c.SnapshotDigest).SetMode(c.Mode).SetSourceGeneration(c.Scope.SourceGeneration).SetNillableSourceRevisionID(c.Scope.SourceRevisionID).SetBaselineVersion(c.BaselineVersion).SetNillableBaselineTarget(c.BaselineTarget).SetBaselineStatus(c.BaselineStatus).SetState(c.State).SetPayload(c.Payload).SetPayloadBytes(int64(len(c.Payload))).Exec(ctx); err != nil {
			return err
		}
	}
	return tx.WorkItem.UpdateOneID(w.ID).SetCandidateID(c.ID).SetState("candidate").Exec(ctx)
}

// guardCandidateHandoff permits saves backed by a request admitted in the
// current retry epoch. A retained candidate keeps its original main parent;
// after Retry, its admitted alignment request supplies the new epoch's proof.
func guardCandidateHandoff(ctx context.Context, tx *ent.Client, c Candidate, old *ent.WorkCandidate) error {
	parentID := c.ParentRequestID
	if old != nil {
		parentID = old.ParentRequestID
	}
	proof := workrequest.And(workrequest.IdentityEQ(parentID), workrequest.StageEQ("main"))
	if old != nil {
		proof = workrequest.Or(proof, workrequest.And(workrequest.CandidateIDEQ(c.ID), workrequest.StageIn("ruby_alignment", "alignment")))
	}
	requests, err := tx.WorkRequest.Query().Where(
		workrequest.JobIDEQ(c.Scope.JobID),
		workrequest.ResourceIDEQ(c.Scope.ResourceID),
		workrequest.JobRoundIDEQ(c.Scope.RoundID),
		workrequest.RetryEpochEQ(c.Scope.RetryEpoch),
		workrequest.StateIn("sent", "received", "completed", "failed"),
		proof,
	).Select(workrequest.FieldSegmentIds).All(ctx)
	if err != nil {
		return err
	}
	for _, request := range requests {
		for _, id := range request.SegmentIds {
			if id == c.SegmentID {
				return nil
			}
		}
	}
	return ErrStopped
}

// LoadCandidates pages by database identity and never reads completed payloads.
// The returned cursor is independent of user-visible candidate identities.
func (s *Store) LoadCandidates(ctx context.Context, scope Scope, afterID, limit int) ([]Candidate, int, error) {
	if limit < 1 || limit > 256 {
		return nil, afterID, fmt.Errorf("invalid candidate page size")
	}
	q := s.client.WorkCandidate.Query().Where(workcandidate.IDGT(afterID), workcandidate.StateIn("pending_alignment", "ready_to_commit"), workcandidate.HasWorkItemWith(workitem.JobIDEQ(scope.JobID))).Order(ent.Asc(workcandidate.FieldID)).Limit(limit).WithWorkItem()
	if scope.RoundID > 0 {
		q.Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(scope.RoundID)))
	}
	if scope.ResourceID > 0 {
		q.Where(workcandidate.HasWorkItemWith(workitem.ResourceIDEQ(scope.ResourceID)))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, afterID, err
	}
	result := make([]Candidate, 0, len(rows))
	for _, row := range rows {
		w := row.Edges.WorkItem
		if w == nil {
			return nil, afterID, ErrManifest
		}
		sc := scope
		sc.JobID = w.JobID
		sc.ResourceID = w.ResourceID
		sc.RoundID = w.JobRoundID
		sc.RetryEpoch = w.RetryEpoch
		sc.SourceGeneration = row.SourceGeneration
		sc.SourceRevisionID = row.SourceRevisionID
		result = append(result, Candidate{ID: row.Identity, ParentRequestID: row.ParentRequestID, Version: row.Version, Scope: sc, SegmentID: w.SegmentID, DTOVersion: row.DtoVersion, Mode: row.Mode, SnapshotDigest: row.SnapshotDigest, BaselineVersion: row.BaselineVersion, BaselineTarget: row.BaselineTarget, BaselineStatus: row.BaselineStatus, State: row.State, Payload: row.Payload})
		afterID = row.ID
	}
	return result, afterID, nil
}

// ValidateScope is useful before rebuilding any request admission budget.
func (s *Store) ValidateScope(ctx context.Context, scope Scope) error {
	return Transaction(ctx, s.client, func(tx *ent.Client) error { return guardScope(ctx, tx, scope, true) })
}

// PauseRequested returns persisted intent independently of the current process.
func (s *Store) PauseRequested(ctx context.Context, jobID int) (bool, error) {
	row, err := s.client.Job.Query().Where(job.IDEQ(jobID)).Only(ctx)
	if err != nil {
		return false, err
	}
	return row.PauseRequested || row.Status == "pausing" || row.Status == "paused", nil
}
