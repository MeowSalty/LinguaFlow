package workstate

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

type NotCommittedError struct{ Cause error }

func (e *NotCommittedError) Error() string {
	return fmt.Sprintf("verified transaction did not commit: %v", e.Cause)
}
func (e *NotCommittedError) Unwrap() error   { return e.Cause }
func (e *NotCommittedError) Permanent() bool { return false }
func (e *NotCommittedError) Uncertain() bool { return false }

type VerificationError struct{ Cause, Verification error }

var errUnverifiable = errors.New("write was superseded or has no complete identity proof")

func (e *VerificationError) Error() string {
	return fmt.Sprintf("transaction remains unknown: %v", errors.Join(e.Cause, e.Verification))
}
func (e *VerificationError) Unwrap() []error { return []error{e.Cause, e.Verification} }
func (e *VerificationError) Permanent() bool { return true }
func (e *VerificationError) Uncertain() bool { return true }

// verifyLocked waits for the original transaction to terminate before deciding
// that an absent identity proves non-commit. A failed/expired read proves nothing.
func (s *Store) verifyLocked(ctx context.Context, jobID int, check func(context.Context, *ent.Client) (bool, error)) (bool, error) {
	var confirmed bool
	err := Transaction(persistence.WithRetryOwner(ctx), s.client, func(tx *ent.Client) error {
		if err := LockJob(ctx, tx, jobID); err != nil {
			return err
		}
		var err error
		confirmed, err = check(ctx, tx)
		return err
	})
	return confirmed, err
}

func (s *Store) finishUnknown(ctx context.Context, jobID int, err error, check func(context.Context, *ent.Client) (bool, error)) error {
	if !unknownCommit(err) {
		return err
	}
	parent := verificationParent{Context: persistence.Lifetime(ctx), values: ctx}
	total := persistence.Deadline(ctx)
	if total.IsZero() {
		total = time.Now().Add(120 * time.Second)
	}
	var verifyErr error
	for attempt := 0; attempt < 4; attempt++ {
		verifyCtx, cancel := context.WithDeadline(parent, minTime(time.Now().Add(5*time.Second), total))
		confirmed, currentErr := s.verifyLocked(verifyCtx, jobID, check)
		cancel()
		if currentErr == nil {
			if confirmed {
				return nil
			}
			return &NotCommittedError{Cause: err}
		}
		verifyErr = currentErr
		if parent.Err() != nil || !time.Now().Before(total) || errors.Is(currentErr, ErrManifest) || errors.Is(currentErr, ErrStopped) || errors.Is(currentErr, ErrCandidateVersion) || errors.Is(currentErr, errUnverifiable) || attempt == 3 {
			break
		}
		timer := time.NewTimer(time.Duration(100<<attempt) * time.Millisecond)
		select {
		case <-parent.Done():
			timer.Stop()
			return &VerificationError{Cause: err, Verification: errors.Join(verifyErr, parent.Err())}
		case <-timer.C:
		}
	}
	return &VerificationError{Cause: err, Verification: verifyErr}
}

// Preserve correlation/observer values while cancellation follows the worker
// instance, rather than the expired SQL attempt or an unbounded detached root.
type verificationParent struct {
	context.Context
	values context.Context
}

func (c verificationParent) Value(key any) any {
	if value := c.values.Value(key); value != nil {
		return value
	}
	return c.Context.Value(key)
}

func reservationMatches(row *ent.WorkRequest, r Request) (bool, error) {
	ids := append([]int(nil), r.SegmentIDs...)
	sort.Ints(ids)
	members, err := normalizedMembers(r, ids)
	if err != nil {
		return false, err
	}
	oldMembers, err := decodeRequestMembers(row.Members)
	if err != nil {
		return false, err
	}
	if row.JobID != r.Scope.JobID || row.JobRoundID != r.Scope.RoundID || row.ResourceID != r.Scope.ResourceID || row.RetryEpoch != r.Scope.RetryEpoch || row.InputDigest != r.InputDigest || row.Stage != r.Stage || row.BackendID != r.BackendID || row.CandidateID != r.CandidateID || row.BudgetModel != r.BudgetModel || !reflect.DeepEqual(row.SegmentIds, ids) || !reflect.DeepEqual(oldMembers, members) {
		return false, fmt.Errorf("%w: request identity already used for different input", ErrManifest)
	}
	if row.State == "cancelled_before_dispatch" {
		return false, ErrStopped
	}
	return true, nil
}

func reservationExists(ctx context.Context, tx *ent.Client, r Request) (bool, error) {
	row, err := tx.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID)).Only(ctx)
	if ent.IsNotFound(err) {
		round, scopeErr := tx.JobRound.Get(ctx, r.Scope.RoundID)
		if scopeErr != nil {
			return false, scopeErr
		}
		if round.JobID != r.Scope.JobID {
			return false, ErrManifest
		}
		if _, scopeErr := tx.Resource.Get(ctx, r.Scope.ResourceID); scopeErr != nil {
			return false, scopeErr
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return reservationMatches(row, r)
}

func commitExists(ctx context.Context, tx *ent.Client, in CommitInput) (bool, error) {
	round, err := tx.JobRound.Get(ctx, in.Scope.RoundID)
	if err != nil {
		return false, err
	}
	if round.JobID != in.Scope.JobID {
		return false, ErrManifest
	}
	row, err := tx.JobRoundSegment.Query().Where(jobroundsegment.CommitIDEQ(in.CommitID)).Only(ctx)
	if ent.IsNotFound(err) {
		other, lookupErr := tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(in.Scope.RoundID), jobroundsegment.SegmentIDEQ(in.SegmentID)).Only(ctx)
		if lookupErr == nil {
			if other.CommitID != nil {
				return false, ErrManifest
			}
			return false, errUnverifiable
		}
		if !ent.IsNotFound(lookupErr) {
			return false, lookupErr
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.JobRoundID != in.Scope.RoundID || row.SegmentID != in.SegmentID {
		return false, fmt.Errorf("%w: commit identity belongs to another work item", ErrManifest)
	}
	return true, nil
}

// ResolveCheckpoint may acknowledge only a complete set of durable facts.
// Callers with an unidentifiable legacy counter delta must preserve unknown.
func ResolveCheckpoint(ctx context.Context, client *ent.Client, jobID int, err error, facts map[int][]Confirmation) error {
	return NewStore(client).finishUnknown(ctx, jobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) {
		for roundID, confirmations := range facts {
			round, err := loadCheckpointRound(ctx, tx, roundID)
			if err != nil {
				return false, err
			}
			if round.JobID != jobID {
				return false, ErrManifest
			}
			byID := map[int]Confirmation{}
			ids := make([]int, 0, len(confirmations))
			for _, fact := range confirmations {
				if _, ok := byID[fact.SegmentID]; !ok {
					ids = append(ids, fact.SegmentID)
				}
				byID[fact.SegmentID] = fact
			}
			for start := 0; start < len(ids); start += bulkIDs {
				end := min(start+bulkIDs, len(ids))
				rows, err := loadCheckpointFacts(ctx, tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(roundID), jobroundsegment.SegmentIDIn(ids[start:end]...)))
				if err != nil {
					return false, err
				}
				if len(rows) != end-start {
					return false, nil
				}
				for _, row := range rows {
					old := Confirmation{SegmentID: row.SegmentID}
					if row.CommitID != nil {
						old.CommitID = *row.CommitID
					}
					if row.CandidateID != nil {
						old.CandidateID = *row.CandidateID
					}
					if row.Outcome != "legacy" {
						old.Outcome = Outcome(row.Outcome)
					}
					if confirmationConflict(old, byID[row.SegmentID]) {
						return false, ErrManifest
					}
				}
			}
			count, err := tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(roundID)).Count(ctx)
			if err != nil {
				return false, err
			}
			if count != round.SegmentCompleted {
				return false, errUnverifiable
			}
		}
		var total, completed int64
		for after := 0; ; {
			rounds, err := tx.JobRound.Query().Where(jobround.JobIDEQ(jobID), jobround.IDGT(after)).Select(jobround.FieldID, jobround.FieldStatus, jobround.FieldSegmentTotal, jobround.FieldSegmentCompleted).Order(ent.Asc(jobround.FieldID)).Limit(16).All(ctx)
			if err != nil {
				return false, err
			}
			if len(rounds) == 0 {
				break
			}
			for _, round := range rounds {
				total += int64(round.SegmentTotal)
				completed += EffectiveProgress(round.Status, int64(round.SegmentTotal), int64(round.SegmentCompleted))
				after = round.ID
			}
		}
		job, err := tx.Job.Query().Where(job.IDEQ(jobID)).Select(job.FieldID, job.FieldProgressTotal, job.FieldProgressCompleted).Only(ctx)
		if err != nil {
			return false, err
		}
		if job.ProgressTotal != total || job.ProgressCompleted != completed {
			return false, errUnverifiable
		}
		return true, nil
	})
}
