package workstate

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"sort"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

type Store struct{ client *ent.Client }

var candidateSaveFields = []string{workcandidate.FieldID, workcandidate.FieldIdentity, workcandidate.FieldParentRequestID, workcandidate.FieldVersion, workcandidate.FieldWorkItemID, workcandidate.FieldDtoVersion, workcandidate.FieldSnapshotDigest, workcandidate.FieldMode, workcandidate.FieldSourceGeneration, workcandidate.FieldSourceRevisionID, workcandidate.FieldBaselineVersion, workcandidate.FieldBaselineStatus, workcandidate.FieldState, workcandidate.FieldPayloadBytes, workcandidate.FieldPipelineHeader}

func candidatePayloadLimit(c Candidate) int64 {
	if c.PayloadLimit > 0 {
		return c.PayloadLimit
	}
	return 1 << 20
}

// candidatePayloadMatches verifies a retained version without copying its body
// into the response owner. PostgreSQL JSONB compares semantic values directly;
// SQLite's canonical writer permits its JSON whitespace normalization.
func candidatePayloadMatches(ctx context.Context, tx *ent.Client, old *ent.WorkCandidate, payload []byte, limit int64) (bool, error) {
	if old.PayloadBytes < 0 || old.PayloadBytes > limit || int64(len(payload)) > limit {
		return false, backend.ErrBuildCapacity
	}
	return tx.WorkCandidate.Query().Where(workcandidate.IDEQ(old.ID), workcandidate.VersionEQ(old.Version), func(sel *sql.Selector) {
		sel.Where(candidateJSONEquality(sel, payload))
	}).Exist(ctx)
}

func candidateJSONEquality(sel *sql.Selector, payload []byte) *sql.Predicate {
	return storageJSONEquality(sel, workcandidate.FieldPayload, payload, false)
}

func storageJSONEquality(sel *sql.Selector, field string, payload []byte, nullable bool) *sql.Predicate {
	column := sel.C(field)
	if sel.Dialect() == "postgres" {
		if nullable {
			column = "COALESCE(" + column + ",'null'::jsonb)"
		}
		return sql.P(func(b *sql.Builder) {
			b.WriteString(column).WriteString(" = CAST(").Arg(string(payload)).WriteString(" AS JSONB)")
		})
	}
	if nullable {
		column = "COALESCE(" + column + ",'null')"
	}
	return sql.P(func(b *sql.Builder) {
		b.WriteString("json(" + column + ") = json(").Arg(string(payload)).WriteString(")")
	})
}

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
	ctx = operationContext(ctx)
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
		count := 0
		for start := 0; start < len(ids); start += bulkIDs {
			n, err := tx.Segment.Query().Where(segment.IDIn(ids[start:min(start+bulkIDs, len(ids))]...), segment.ResourceIDEQ(scope.ResourceID)).Count(ctx)
			if err != nil {
				return err
			}
			count += n
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
		for start := 0; start < len(ids); start += bulkRows {
			end := min(start+bulkRows, len(ids))
			builders := make([]*ent.WorkItemCreate, 0, end-start)
			for _, id := range ids[start:end] {
				state := "pending"
				if done[id] {
					state = "resolved"
				}
				builders = append(builders, tx.WorkItem.Create().SetJobID(scope.JobID).SetResourceID(scope.ResourceID).SetJobRoundID(scope.RoundID).SetSegmentID(id).SetRetryEpoch(scope.RetryEpoch).SetState(state))
			}
			if err := tx.WorkItem.CreateBulk(builders...).Exec(ctx); err != nil {
				return err
			}
		}
		n, err := tx.JobRound.Update().Where(jobround.IDEQ(scope.RoundID), jobround.ManifestSealedEQ(false)).SetManifestVersion(1).SetManifestSealed(true).SetSegmentTotal(len(ids)).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrManifest
		}
		return Calibrate(ctx, tx, scope.JobID)
	})
	err = s.finishUnknown(ctx, scope.JobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) {
		round, err := tx.JobRound.Get(ctx, scope.RoundID)
		if err != nil {
			return false, err
		}
		if !round.ManifestSealed {
			return false, nil
		}
		if round.JobID != scope.JobID || round.SegmentTotal != len(ids) {
			return false, ErrManifest
		}
		rows, err := tx.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID)).Select(workitem.FieldSegmentID).All(ctx)
		if err != nil {
			return false, err
		}
		if len(rows) != len(ids) {
			return false, ErrManifest
		}
		for _, row := range rows {
			if _, ok := unique[row.SegmentID]; !ok {
				return false, ErrManifest
			}
		}
		return true, nil
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
	return s.SaveCursors(ctx, scope, []CursorTransition{{SegmentID: segmentID, Cursor: cursor}})
}

func saveCursor(ctx context.Context, tx *ent.Client, scope Scope, segmentID int, cursor Cursor, expected ...map[int]attemptCounters) error {
	row, err := member(ctx, tx, scope, segmentID)
	if err != nil {
		return err
	}
	return saveCursorRows(ctx, tx, scope, []CursorTransition{{SegmentID: segmentID, Cursor: cursor}}, map[int]*ent.WorkItem{segmentID: row}, expected...)
}

func (s *Store) SavePool(ctx context.Context, scope Scope, pool int) error {
	ctx = operationContext(ctx)
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
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
	return s.finishUnknown(ctx, scope.JobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) {
		row, err := tx.JobRound.Get(ctx, scope.RoundID)
		if err != nil {
			return false, err
		}
		if row.JobID != scope.JobID {
			return false, ErrManifest
		}
		if row.PoolIndex > pool {
			return false, errUnverifiable
		}
		return row.PoolIndex == pool, nil
	})
}

func lockSegment(ctx context.Context, tx *ent.Client, scope Scope, id int, version int64, factsOnly ...bool) (*ent.Segment, error) {
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
	if len(factsOnly) > 0 && factsOnly[0] {
		return tx.Segment.Query().Where(segment.IDEQ(id)).Select(segment.FieldID, segment.FieldContentVersion, segment.FieldTargetText, segment.FieldStatus).Only(ctx)
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
	if int64(len(c.Payload)) > candidatePayloadLimit(c) {
		return backend.ErrBuildCapacity
	}
	if c.ID == "" || len(c.ID) > 128 || len(c.WorkID) > 128 || len(c.ParentRequestID) > 128 || c.Version < 1 || c.DTOVersion < 1 || c.SnapshotDigest == "" || !json.Valid(c.Payload) {
		return fmt.Errorf("invalid candidate")
	}
	if c.State == "" {
		c.State = "pending_alignment"
	}
	ctx = operationContext(ctx)
	expected := map[int]attemptCounters{}
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := saveCandidate(ctx, tx, c); err != nil {
			return err
		}
		if cursor != nil {
			cursor.LastAlignmentRequestID = c.LastAlignmentRequestID
			cursor.CandidateVersion = c.Version
			cursor.WorkID = c.WorkID
			return saveCursor(ctx, tx, c.Scope, c.SegmentID, *cursor, expected)
		}
		return nil
	})
	return s.finishUnknown(ctx, c.Scope.JobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) {
		w, err := member(ctx, tx, c.Scope, c.SegmentID)
		if err != nil {
			return false, err
		}
		row, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(c.ID), workcandidate.PayloadBytesLTE(candidatePayloadLimit(c))).Select(candidateSaveFields...).Only(ctx)
		if ent.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		equal, equalErr := candidatePayloadMatches(ctx, tx, row, c.Payload, candidatePayloadLimit(c))
		if equalErr != nil {
			return false, equalErr
		}
		if row.WorkItemID != w.ID || row.Version != c.Version || row.SnapshotDigest != c.SnapshotDigest || row.DtoVersion != c.DTOVersion || row.Mode != c.Mode || row.BaselineVersion != c.BaselineVersion || row.State != c.State || !equal {
			if row.WorkItemID == w.ID && row.Version < c.Version {
				return false, nil
			}
			return false, errUnverifiable
		}
		if w.CandidateID != c.ID || cursor != nil && (!cursorSaved(w, *cursor) || attemptsFromRow(w) != expected[c.SegmentID]) {
			return false, errUnverifiable
		}
		return true, nil
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
	old, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(c.ID)).Select(candidateSaveFields...).Only(ctx)
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
	row, err := lockSegment(ctx, tx, c.Scope, c.SegmentID, c.BaselineVersion, true)
	if err != nil {
		return err
	}
	if !candidateBaselineMatches(c, row.TargetText) || string(row.Status) != c.BaselineStatus {
		return ErrStale
	}
	if old != nil {
		validDTO := old.DtoVersion == c.DTOVersion
		if old.WorkItemID != w.ID || old.BaselineVersion != c.BaselineVersion || old.SnapshotDigest != c.SnapshotDigest || !validDTO || old.Mode != c.Mode || old.SourceGeneration != c.Scope.SourceGeneration || !sameInt(old.SourceRevisionID, c.Scope.SourceRevisionID) {
			return ErrCandidateVersion
		}
		if old.State == "completed" || old.State == "stale" || old.State == "rejected" {
			return ErrCandidateVersion
		}
		if c.Version < old.Version {
			return ErrCandidateVersion
		}
		if c.Version == old.Version {
			equal, equalErr := candidatePayloadMatches(ctx, tx, old, c.Payload, candidatePayloadLimit(c))
			if equalErr != nil {
				return equalErr
			}
			if !equal || c.State != old.State {
				return ErrCandidateVersion
			}
			return nil
		}
		if c.Version != old.Version+1 || w.CandidateID != c.ID {
			return ErrCandidateVersion
		}
		if err := tx.WorkCandidate.UpdateOneID(old.ID).SetVersion(c.Version).SetDtoVersion(c.DTOVersion).SetState(c.State).SetPayload(c.Payload).SetPayloadBytes(int64(len(c.Payload))).SetPipelineHeader(c.PipelineHeader).Exec(ctx); err != nil {
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
		if err := tx.WorkCandidate.Create().SetIdentity(c.ID).SetParentRequestID(c.ParentRequestID).SetVersion(c.Version).SetWorkItemID(w.ID).SetDtoVersion(c.DTOVersion).SetSnapshotDigest(c.SnapshotDigest).SetMode(c.Mode).SetSourceGeneration(c.Scope.SourceGeneration).SetNillableSourceRevisionID(c.Scope.SourceRevisionID).SetBaselineVersion(c.BaselineVersion).SetNillableBaselineTarget(c.BaselineTarget).SetBaselineDigest(c.BaselineDigest).SetBaselineTargetIsNull(c.BaselineTargetIsNull).SetBaselineStatus(c.BaselineStatus).SetState(c.State).SetPayload(c.Payload).SetPayloadBytes(int64(len(c.Payload))).SetPipelineHeader(c.PipelineHeader).Exec(ctx); err != nil {
			return err
		}
	}
	return tx.WorkItem.UpdateOneID(w.ID).SetMemberOwned(true).SetCandidateID(c.ID).SetState("candidate").Exec(ctx)
}

// guardCandidateHandoff permits saves backed by a request admitted in the
// current retry epoch. A retained candidate keeps its original main parent;
// after Retry, its admitted alignment request supplies the new epoch's proof.
func guardCandidateHandoff(ctx context.Context, tx *ent.Client, c Candidate, old *ent.WorkCandidate) error {
	if old == nil {
		ok, err := guardPromotedResponse(ctx, tx, c)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	if old != nil {
		var saved struct {
			RetryEpoch             int64
			LastAlignmentRequestID string
		}
		validSaved := false
		lastAlignmentMatches := false
		if len(old.PipelineHeader) > 0 && len(old.PipelineHeader) <= 2048 {
			b, e := backend.NewBuildBounds(2048, 0)
			validSaved = e == nil && b.DecodeJSON(old.PipelineHeader, &saved) == nil && saved.RetryEpoch == c.Scope.RetryEpoch
			lastAlignmentMatches = saved.LastAlignmentRequestID == c.LastAlignmentRequestID
		} else {
			// Legacy lifecycle proof is also scalar: do not read the old Q body
			// after an already paid request merely to inspect two fields.
			proof := tx.WorkCandidate.Query().Where(workcandidate.IDEQ(old.ID), workcandidate.VersionEQ(old.Version), func(sel *sql.Selector) {
				sel.Where(sqljson.ValueEQ(sel.C(workcandidate.FieldPayload), c.Scope.RetryEpoch, sqljson.Path("retry_epoch")))
			})
			var err error
			validSaved, err = proof.Clone().Exist(ctx)
			if err != nil {
				return err
			}
			lastAlignmentMatches, err = proof.Where(func(sel *sql.Selector) {
				sel.Where(sqljson.ValueEQ(sel.C(workcandidate.FieldPayload), c.LastAlignmentRequestID, sqljson.Path("last_alignment_request_id")))
			}).Exist(ctx)
			if err != nil {
				return err
			}
		}
		if validSaved && old.Version == c.Version && old.State == c.State {
			equal, equalErr := candidatePayloadMatches(ctx, tx, old, c.Payload, candidatePayloadLimit(c))
			if equalErr != nil {
				return equalErr
			}
			if equal {
				return nil
			}
		}
		w, err := member(ctx, tx, c.Scope, c.SegmentID)
		if err != nil {
			return err
		}
		ok, err := alignmentProof(ctx, tx, c.Scope, w, c.LastAlignmentRequestID, c.WorkID, c.Version, false)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if c.LastAlignmentRequestID != "" && c.State == "ready_to_commit" && c.Version == old.Version+1 {
			// A locally exhausted candidate may be finalized without another
			// invocation. Its already saved response supplies the authorization;
			// it does not authorize clearing any newer network debit.
			if validSaved && lastAlignmentMatches {
				ok, err = alignmentProof(ctx, tx, c.Scope, w, c.LastAlignmentRequestID, c.WorkID, old.Version, false)
				if err != nil {
					return err
				}
				if ok {
					return nil
				}
			}
		}
		// A named alignment request must prove this exact successor. A main
		// parent cannot authorize a stale or unrelated batch response.
		if c.LastAlignmentRequestID != "" {
			return ErrStopped
		}
	}
	parentID := c.ParentRequestID
	if old != nil {
		parentID = old.ParentRequestID
	}
	requests, err := tx.WorkRequest.Query().Where(
		workrequest.JobIDEQ(c.Scope.JobID),
		workrequest.ResourceIDEQ(c.Scope.ResourceID),
		workrequest.JobRoundIDEQ(c.Scope.RoundID),
		workrequest.RetryEpochEQ(c.Scope.RetryEpoch),
		workrequest.StateIn("sent", "received", "completed", "failed"),
		workrequest.IdentityEQ(parentID), workrequest.StageEQ("main"),
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

func candidateBaselineMatches(c Candidate, target *string) bool {
	if c.BaselineDigest != "" {
		return c.BaselineTargetIsNull == (target == nil) && c.BaselineDigest == stringDigest(target)
	}
	return sameString(target, c.BaselineTarget)
}
func promotionCandidateBaselineMatches(m PromotionMember, c Candidate) bool {
	if c.BaselineDigest != "" {
		return m.BaselineDigest == c.BaselineDigest && m.BaselineTargetIsNull == c.BaselineTargetIsNull
	}
	return samePromotionBaseline(m, c.BaselineTarget)
}
