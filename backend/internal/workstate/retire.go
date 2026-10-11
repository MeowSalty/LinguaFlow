package workstate

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
)

// RetireCandidate persists a rejected/stale candidate's successor cursor and
// removes its payload atomically. Repeating a retirement cannot rewind work
// that has already moved on to a new candidate, pool or accepted result.
func (s *Store) RetireCandidate(ctx context.Context, scope Scope, id string, version int64, successor Cursor) error {
	if id == "" || version < 1 {
		return ErrCandidateVersion
	}
	return Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, scope, false); err != nil {
			return err
		}
		candidate, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(id)).Only(ctx)
		if ent.IsNotFound(err) {
			return ErrCandidateVersion
		}
		if err != nil {
			return err
		}
		if candidate.Version != version {
			return ErrCandidateVersion
		}
		w, err := tx.WorkItem.Get(ctx, candidate.WorkItemID)
		if err != nil {
			return err
		}
		if w.JobID != scope.JobID || w.JobRoundID != scope.RoundID || w.ResourceID != scope.ResourceID || w.RetryEpoch != scope.RetryEpoch {
			return ErrManifest
		}
		terminal := candidate.State != "pending_alignment" && candidate.State != "ready_to_commit"
		if w.State == "resolved" || candidate.State == "completed" {
			return nil
		}
		if w.CandidateID != id {
			if terminal {
				return nil
			}
			return ErrCandidateVersion
		}
		if successor.State == "" {
			successor.State = "unresolved"
		}
		if err := saveCursor(ctx, tx, scope, w.SegmentID, successor); err != nil {
			return err
		}
		state := "rejected"
		if successor.State == "stale" {
			state = "stale"
		}
		if err := tx.WorkCandidate.UpdateOneID(candidate.ID).SetState(state).ClearPayload().SetPayloadBytes(0).Exec(ctx); err != nil {
			return err
		}
		return tx.WorkItem.UpdateOneID(w.ID).SetCandidateID("").Exec(ctx)
	})
}
