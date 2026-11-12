package workstate

import (
	"context"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
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
	ctx = operationContext(ctx)
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
			round, err := tx.JobRound.Get(ctx, previous.JobRoundID)
			if err != nil {
				return err
			}
			if round.JobID != in.Scope.JobID {
				return ErrManifest
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
			if err := tx.WorkItem.UpdateOneID(w.ID).SetMemberOwned(false).SetState(string(outcome)).Exec(ctx); err != nil {
				return err
			}
			if in.CandidateID != "" {
				_, err := tx.WorkCandidate.Update().Where(workcandidate.IdentityEQ(in.CandidateID), workcandidate.WorkItemIDEQ(w.ID), workcandidate.VersionEQ(in.CandidateVersion)).SetState(string(outcome)).ClearPayload().SetGrowthReserved(false).SetPayloadBytes(0).Save(ctx)
				if err != nil {
					return err
				}
				return clearRuntimeBindingTx(ctx, tx, in.Scope.JobID, in.CandidateID, false)
			}
			return nil
		}
		if w.State == "resolved" {
			result.Outcome = Stale
			return nil
		}
		if in.CandidateID != "" {
			current, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(in.CandidateID), workcandidate.WorkItemIDEQ(w.ID)).Select(workcandidate.FieldID, workcandidate.FieldIdentity, workcandidate.FieldVersion, workcandidate.FieldBaselineVersion, workcandidate.FieldState).Only(ctx)
			if ent.IsNotFound(err) || w.CandidateID != in.CandidateID || (err == nil && current.Version != in.CandidateVersion) {
				result.Outcome = Stale
				return nil
			}
			if err != nil {
				return err
			}
		}
		row, err := lockSegment(ctx, tx, in.Scope, in.SegmentID, in.BaselineVersion, in.IssuesPayload != nil)
		if errors.Is(err, ErrStale) {
			return terminal(Stale)
		}
		if err != nil {
			return err
		}
		if !candidateBaselineMatches(Candidate{BaselineTarget: in.BaselineTarget, BaselineDigest: in.BaselineDigest, BaselineTargetIsNull: in.BaselineTargetIsNull}, row.TargetText) || string(row.Status) != in.BaselineStatus {
			return terminal(Stale)
		}
		if in.CandidateID != "" {
			c, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(in.CandidateID), workcandidate.WorkItemIDEQ(w.ID)).Select(workcandidate.FieldID, workcandidate.FieldIdentity, workcandidate.FieldVersion, workcandidate.FieldBaselineVersion, workcandidate.FieldState).Only(ctx)
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
			if in.IssuesPayload != nil {
				same, err := tx.Segment.Query().Where(segment.IDEQ(row.ID), func(s *sql.Selector) {
					s.Where(storageJSONEquality(s, segment.FieldQualityIssues, in.IssuesPayload, true))
				}).Exist(ctx)
				if err != nil {
					return err
				}
				review := false
				if in.ClearReview {
					var err error
					review, err = tx.Segment.Query().Where(segment.IDEQ(row.ID), segment.ReviewCommentNotNil()).Exist(ctx)
					if err != nil {
						return err
					}
				}
				changed = row.TargetText == nil || *row.TargetText != in.Target || string(row.Status) != status || !same || review
			}
			if changed {
				if in.IssuesPayload != nil {
					u := tx.Segment.Update().Where(segment.IDEQ(row.ID), segment.ContentVersionEQ(in.BaselineVersion)).SetTargetText(in.Target).SetStatus(segment.Status(status)).SetQualityIssues(nil).Modify(func(u *sql.UpdateBuilder) {
						u.Set(segment.FieldQualityIssues, sql.ExprFunc(func(b *sql.Builder) {
							b.WriteString("CAST(").Arg(in.IssuesPayload)
							if b.Dialect() == dialect.Postgres {
								b.WriteString(" AS JSONB)")
							} else {
								b.WriteString(" AS TEXT)")
							}
						}))
					})
					if in.ClearReview {
						u.ClearReviewComment()
					}
					n, err := u.Save(ctx)
					if err != nil {
						return err
					}
					if n != 1 {
						return ErrStale
					}
					result.ContentVersion = in.BaselineVersion + 1
				} else {
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
				}
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
		if _, err := confirm(ctx, tx, in.Scope.JobID, in.Scope.RoundID, []Confirmation{{SegmentID: in.SegmentID, CommitID: in.CommitID, CandidateID: in.CandidateID, Outcome: outcome}}, true); err != nil {
			return err
		}
		if err := tx.WorkItem.UpdateOneID(w.ID).SetMemberOwned(false).SetState("resolved").ClearNextAttemptAt().Exec(ctx); err != nil {
			return err
		}
		if in.CandidateID != "" {
			if _, err := tx.WorkCandidate.Update().Where(workcandidate.IdentityEQ(in.CandidateID)).SetState("completed").ClearPayload().SetGrowthReserved(false).SetPayloadBytes(0).Save(ctx); err != nil {
				return err
			}
		}
		result.Outcome = outcome
		return clearRuntimeBindingTx(ctx, tx, in.Scope.JobID, in.CandidateID, false)
	})
	if err == nil {
		return result, nil
	}
	if unknownCommit(err) {
		err = s.finishUnknown(ctx, in.Scope.JobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) { return commitExists(ctx, tx, in) })
		if err == nil {
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
