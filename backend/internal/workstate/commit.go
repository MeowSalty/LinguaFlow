package workstate

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
)

func (s *Store) LookupCommit(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	return s.client.JobRoundSegment.Query().Where(jobroundsegment.CommitIDEQ(id)).Exist(ctx)
}

// Commit applies a single accepted result and its completed fact in one
// transaction. A storage failure never authorizes another model request.
func (s *Store) Commit(ctx context.Context, in CommitInput) (CommitResult, error) {
	if in.CommitID == "" || in.SegmentID < 1 || in.BaselineVersion < 1 {
		return CommitResult{Outcome: Fatal}, fmt.Errorf("invalid result identity or baseline")
	}
	var result CommitResult
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		result = CommitResult{}
		if err := LockJob(ctx, tx, in.Scope.JobID); err != nil {
			return err
		}
		previous, err := tx.JobRoundSegment.Query().Where(jobroundsegment.CommitIDEQ(in.CommitID)).Only(ctx)
		if err == nil {
			if previous.JobRoundID != in.Scope.RoundID || previous.SegmentID != in.SegmentID {
				return fmt.Errorf("commit identity belongs to another work item")
			}
			result.Outcome = AlreadyCommitted
			return nil
		}
		if !ent.IsNotFound(err) {
			return err
		}
		if err := guardScope(ctx, tx, in.Scope, false); err != nil {
			return err
		}
		w, err := member(ctx, tx, in.Scope, in.SegmentID)
		if err != nil {
			return err
		}
		round, err := tx.JobRound.Get(ctx, in.Scope.RoundID)
		if err != nil {
			return err
		}
		if !round.ManifestSealed {
			return ErrManifest
		}
		terminal := func(outcome Outcome) error {
			result.Outcome = outcome
			if err := tx.WorkItem.UpdateOneID(w.ID).SetState(string(outcome)).Exec(ctx); err != nil {
				return err
			}
			if in.CandidateID != "" {
				_, err := tx.WorkCandidate.Update().Where(workcandidate.IdentityEQ(in.CandidateID), workcandidate.WorkItemIDEQ(w.ID), workcandidate.VersionEQ(in.CandidateVersion)).SetState(string(outcome)).ClearPayload().SetPayloadBytes(0).Save(ctx)
				return err
			}
			return nil
		}
		if w.State == "resolved" {
			result.Outcome = Stale
			return nil
		}
		if in.CandidateID != "" {
			current, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(in.CandidateID), workcandidate.WorkItemIDEQ(w.ID)).Only(ctx)
			if ent.IsNotFound(err) || w.CandidateID != in.CandidateID || (err == nil && current.Version != in.CandidateVersion) {
				result.Outcome = Stale
				return nil
			}
			if err != nil {
				return err
			}
		}
		row, err := lockSegment(ctx, tx, in.Scope, in.SegmentID, in.BaselineVersion)
		if errors.Is(err, ErrStale) {
			return terminal(Stale)
		}
		if err != nil {
			return err
		}
		if !sameString(row.TargetText, in.BaselineTarget) || string(row.Status) != in.BaselineStatus {
			return terminal(Stale)
		}
		if in.CandidateID != "" {
			c, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(in.CandidateID), workcandidate.WorkItemIDEQ(w.ID)).Only(ctx)
			if ent.IsNotFound(err) {
				return terminal(Stale)
			}
			if err != nil {
				return err
			}
			if w.CandidateID != in.CandidateID || c.Version != in.CandidateVersion || c.BaselineVersion != in.BaselineVersion || c.State != "ready_to_commit" {
				return terminal(Stale)
			}
		}
		outcome := Committed
		if in.Noop {
			outcome = ConfirmedNoop
		} else {
			status := string(row.Status)
			if in.Status != "" {
				status = in.Status
			}
			changed := row.TargetText == nil || *row.TargetText != in.Target || string(row.Status) != status || !reflect.DeepEqual(row.QualityIssues, in.Issues) || (in.ClearReview && row.ReviewComment != nil)
			if changed {
				u := tx.Segment.UpdateOneID(row.ID).Where(segment.ContentVersionEQ(in.BaselineVersion)).SetTargetText(in.Target).SetStatus(segment.Status(status))
				if len(in.Issues) > 0 {
					u.SetQualityIssues(in.Issues)
				} else {
					u.ClearQualityIssues()
				}
				if in.ClearReview {
					u.ClearReviewComment()
				}
				updated, err := u.Save(ctx)
				if err != nil {
					return err
				}
				result.ContentVersion = updated.ContentVersion
				if err := tx.Resource.UpdateOneID(in.Scope.ResourceID).AddTranslationGeneration(1).Exec(ctx); err != nil {
					return err
				}
			} else {
				outcome = ConfirmedNoop
			}
		}
		if result.ContentVersion == 0 {
			result.ContentVersion = row.ContentVersion
		}
		if _, err := Confirm(ctx, tx, in.Scope.JobID, in.Scope.RoundID, []Confirmation{{SegmentID: in.SegmentID, CommitID: in.CommitID, CandidateID: in.CandidateID, Outcome: outcome}}); err != nil {
			return err
		}
		if err := tx.WorkItem.UpdateOneID(w.ID).SetState("resolved").ClearNextAttemptAt().Exec(ctx); err != nil {
			return err
		}
		if in.CandidateID != "" {
			if _, err := tx.WorkCandidate.Update().Where(workcandidate.IdentityEQ(in.CandidateID)).SetState("completed").ClearPayload().SetPayloadBytes(0).Save(ctx); err != nil {
				return err
			}
		}
		result.Outcome = outcome
		return nil
	})
	if err == nil {
		return result, nil
	}
	// A lost commit acknowledgement can be verified even if the job was
	// cancelled or a human changed the segment after the successful commit.
	if ctx.Err() == nil {
		if ok, lookupErr := s.LookupCommit(ctx, in.CommitID); lookupErr == nil && ok {
			return CommitResult{Outcome: AlreadyCommitted}, nil
		}
	}
	if errors.Is(err, ErrStale) {
		return CommitResult{Outcome: Stale}, err
	}
	if errors.Is(err, ErrStopped) || errors.Is(err, ErrManifest) {
		return CommitResult{Outcome: Fatal}, err
	}
	return CommitResult{Outcome: RetryableStorage}, err
}
