package workstate

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// LockJob must be the first execution-data write in a transaction. UPDATE
// obtains a PostgreSQL row lock and SQLite's write lock before any reads.
func LockJob(ctx context.Context, tx *ent.Client, id int) error {
	n, err := tx.Job.Update().Where(job.IDEQ(id)).AddRetryEpoch(0).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStopped
	}
	return nil
}

func EffectiveProgress(status string, total, completed int64) int64 {
	if (status == "completed" || status == "skipped") && total > completed {
		return total
	}
	return completed
}

// ReconcileJob is the single cache calculation, used after migration, state
// transitions and deletions. The caller holds LockJob in this transaction.
func ReconcileJob(ctx context.Context, tx *ent.Client, jobID int) error {
	var total, completed int64
	for after := 0; ; {
		rows, err := tx.JobRound.Query().Where(jobround.JobIDEQ(jobID), jobround.IDGT(after)).Select(jobround.FieldID, jobround.FieldStatus, jobround.FieldSegmentTotal, jobround.FieldSegmentCompleted).Order(ent.Asc(jobround.FieldID)).Limit(16).All(ctx)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			effective := EffectiveProgress(row.Status, int64(row.SegmentTotal), int64(row.SegmentCompleted))
			if int64(row.SegmentTotal) > math.MaxInt64-total || effective > math.MaxInt64-completed {
				return ErrBudget
			}
			total += int64(row.SegmentTotal)
			completed += effective
			after = row.ID
		}
	}
	return updateJobProgress(ctx, tx, jobID, total, completed)
}

// Calibrate repairs count caches from their database sets. It is also called
// after cascading segment/round deletion, before the deletion transaction ends.
func updateJobProgress(ctx context.Context, tx *ent.Client, jobID int, total, completed int64) error {
	n, err := tx.Job.Update().Where(job.IDEQ(jobID)).SetProgressTotal(total).SetProgressCompleted(completed).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStopped
	}
	return nil
}

func Calibrate(ctx context.Context, tx *ent.Client, jobID int) error {
	var jobTotal, jobCompleted int64
	after := 0
	for {
		rows, err := tx.JobRound.Query().Where(jobround.JobIDEQ(jobID), jobround.IDGT(after)).Order(ent.Asc(jobround.FieldID)).Select(jobround.FieldID, jobround.FieldStatus, jobround.FieldManifestSealed, jobround.FieldSegmentTotal, jobround.FieldSegmentCompleted).Limit(16).All(ctx)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		completed, total := map[int]int{}, map[int]int{}
		for start := 0; start < len(rows); start += bulkIDs {
			end := min(start+bulkIDs, len(rows))
			ids := make([]int, 0, end-start)
			sealed := make([]int, 0, end-start)
			for _, row := range rows[start:end] {
				ids = append(ids, row.ID)
				if row.ManifestSealed {
					sealed = append(sealed, row.ID)
				}
			}
			var counts []struct {
				RoundID int `json:"job_round_id"`
				Count   int `json:"count"`
			}
			if err := tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDIn(ids...)).GroupBy(jobroundsegment.FieldJobRoundID).Aggregate(ent.Count()).Scan(ctx, &counts); err != nil {
				return err
			}
			for _, count := range counts {
				completed[count.RoundID] = count.Count
			}
			if len(sealed) > 0 {
				counts = nil
				if err := tx.WorkItem.Query().Where(workitem.JobRoundIDIn(sealed...)).GroupBy(workitem.FieldJobRoundID).Aggregate(ent.Count()).Scan(ctx, &counts); err != nil {
					return err
				}
				for _, count := range counts {
					total[count.RoundID] = count.Count
				}
			}
		}
		for _, row := range rows {
			n := completed[row.ID]
			t := row.SegmentTotal
			if row.ManifestSealed {
				t = total[row.ID]
				if t < n {
					return fmt.Errorf("%w: round %d has completion outside manifest", ErrManifest, row.ID)
				}
			}
			if n != row.SegmentCompleted || t != row.SegmentTotal {
				matched, err := tx.JobRound.Update().Where(jobround.IDEQ(row.ID), jobround.JobIDEQ(jobID), jobround.SegmentCompletedEQ(row.SegmentCompleted), jobround.SegmentTotalEQ(row.SegmentTotal)).SetSegmentCompleted(n).SetSegmentTotal(t).Save(ctx)
				if err != nil {
					return err
				}
				if matched != 1 {
					return ErrManifest
				}
			}
			if int64(t) > math.MaxInt64-jobTotal {
				return ErrBudget
			}
			jobTotal += int64(t)
			effective := EffectiveProgress(row.Status, int64(t), int64(n))
			if effective > math.MaxInt64-jobCompleted {
				return ErrBudget
			}
			jobCompleted += effective
			after = row.ID
		}
	}

	return updateJobProgress(ctx, tx, jobID, jobTotal, jobCompleted)
}

// Confirm inserts genuinely new facts under the Job lock. It never uses a
// reporter's memory count, so late legacy buffers cannot erase another writer.
func Confirm(ctx context.Context, tx *ent.Client, jobID, roundID int, facts []Confirmation) (int, error) {
	return confirm(ctx, tx, jobID, roundID, facts, false)
}

func confirm(ctx context.Context, tx *ent.Client, jobID, roundID int, facts []Confirmation, enrich bool) (int, error) {
	row, err := loadCheckpointRound(ctx, tx, roundID)
	if err != nil {
		return 0, err
	}
	if row.JobID != jobID {
		return 0, ErrManifest
	}
	before := EffectiveProgress(row.Status, int64(row.SegmentTotal), int64(row.SegmentCompleted))
	byID := make(map[int]Confirmation, len(facts))
	for _, fact := range facts {
		if fact.SegmentID < 1 {
			continue
		}
		if old, ok := byID[fact.SegmentID]; ok {
			if confirmationConflict(old, fact) {
				return 0, fmt.Errorf("%w: conflicting completion identity", ErrManifest)
			}
			if old.CommitID != "" {
				fact.CommitID = old.CommitID
			}
			if old.CandidateID != "" {
				fact.CandidateID = old.CandidateID
			}
			if old.Outcome != "" {
				fact.Outcome = old.Outcome
			}
		}
		byID[fact.SegmentID] = fact
	}
	ids := make([]int, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for start := 0; start < len(ids); start += bulkIDs {
		end := min(start+bulkIDs, len(ids))
		existing, err := loadCheckpointFacts(ctx, tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(roundID), jobroundsegment.SegmentIDIn(ids[start:end]...)))
		if err != nil {
			return 0, err
		}
		for _, old := range existing {
			incoming := byID[old.SegmentID]
			previous := Confirmation{SegmentID: old.SegmentID}
			if old.CommitID != nil {
				previous.CommitID = *old.CommitID
			}
			if old.CandidateID != nil {
				previous.CandidateID = *old.CandidateID
			}
			if old.Outcome != "legacy" {
				previous.Outcome = Outcome(old.Outcome)
			}
			if confirmationConflict(previous, incoming) {
				return 0, fmt.Errorf("%w: conflicting persisted completion identity", ErrManifest)
			}
			// Only Commit reaches this path after full acceptance checks. A legacy
			// completion already counts, but needs the accepted commit identity so
			// a lost COMMIT acknowledgement can be verified without applying again.
			if enrich && (old.CommitID == nil && incoming.CommitID != "" || old.CandidateID == nil && incoming.CandidateID != "" || old.Outcome == "legacy" && incoming.Outcome != "") {
				u := tx.JobRoundSegment.Update().Where(jobroundsegment.IDEQ(old.ID), jobroundsegment.JobRoundIDEQ(roundID), jobroundsegment.SegmentIDEQ(old.SegmentID))
				if old.CommitID == nil && incoming.CommitID != "" {
					u.SetCommitID(incoming.CommitID)
				}
				if old.CandidateID == nil && incoming.CandidateID != "" {
					u.SetCandidateID(incoming.CandidateID)
				}
				if old.Outcome == "legacy" && incoming.Outcome != "" {
					u.SetOutcome(string(incoming.Outcome))
				}
				n, err := u.Save(ctx)
				if err != nil {
					return 0, err
				}
				if n != 1 {
					return 0, ErrManifest
				}
			}
			delete(byID, old.SegmentID)
		}
	}
	newIDs := make([]int, 0, len(byID))
	for _, id := range ids {
		if _, ok := byID[id]; ok {
			newIDs = append(newIDs, id)
		}
	}
	if row.ManifestSealed {
		for start := 0; start < len(newIDs); start += bulkIDs {
			end := min(start+bulkIDs, len(newIDs))
			count, err := tx.WorkItem.Query().Where(workitem.JobIDEQ(jobID), workitem.JobRoundIDEQ(roundID), workitem.SegmentIDIn(newIDs[start:end]...)).Count(ctx)
			if err != nil {
				return 0, err
			}
			if count != end-start {
				return 0, ErrManifest
			}
		}
	}
	for start := 0; start < len(newIDs); start += bulkRows {
		end := min(start+bulkRows, len(newIDs))
		builders := make([]*ent.JobRoundSegmentCreate, 0, end-start)
		for _, id := range newIDs[start:end] {
			fact := byID[id]
			b := tx.JobRoundSegment.Create().SetJobRoundID(roundID).SetSegmentID(id)
			if fact.CommitID != "" {
				b.SetCommitID(fact.CommitID)
			}
			if fact.CandidateID != "" {
				b.SetCandidateID(fact.CandidateID)
			}
			if fact.Outcome != "" {
				b.SetOutcome(string(fact.Outcome))
			}
			builders = append(builders, b)
		}
		if err := tx.JobRoundSegment.CreateBulk(builders...).Exec(ctx); err != nil {
			return 0, err
		}
	}
	added := len(newIDs)
	// Counting the round's indexed relation also repairs pre-upgrade caches. No
	// per-segment scan over the job or a reporter's stale absolute value is used.
	count := row.SegmentCompleted + added
	if !row.ManifestSealed {
		count, err = tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(roundID)).Count(ctx)
		if err != nil {
			return 0, err
		}
	}
	if n, err := tx.JobRound.Update().Where(jobround.IDEQ(roundID), jobround.JobIDEQ(jobID), jobround.SegmentCompletedEQ(row.SegmentCompleted)).SetSegmentCompleted(count).Save(ctx); err != nil {
		return 0, err
	} else if n != 1 {
		return 0, ErrManifest
	}
	after := EffectiveProgress(row.Status, int64(row.SegmentTotal), int64(count))
	if delta := after - before; delta != 0 {
		if n, err := tx.Job.Update().Where(job.IDEQ(jobID)).AddProgressCompleted(delta).Save(ctx); err != nil {
			return 0, err
		} else if n != 1 {
			return 0, ErrStopped
		}
	}
	return added, nil
}

// Empty metadata is a completion notification, not a conflicting identity.
func confirmationConflict(a, b Confirmation) bool {
	return a.CommitID != "" && b.CommitID != "" && a.CommitID != b.CommitID || a.CandidateID != "" && b.CandidateID != "" && a.CandidateID != b.CandidateID || a.Outcome != "" && b.Outcome != "" && a.Outcome != b.Outcome
}

func SetRoundStatus(ctx context.Context, tx *ent.Client, roundID int, allowed []string, status string) error {
	row, err := loadCheckpointRound(ctx, tx, roundID)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	matched := false
	for _, old := range allowed {
		matched = matched || row.Status == old
	}
	if !matched {
		return nil
	}
	before := EffectiveProgress(row.Status, int64(row.SegmentTotal), int64(row.SegmentCompleted))
	u := tx.JobRound.UpdateOneID(roundID).SetStatus(status)
	if status == "running" {
		u.SetStartedAt(timeutil.NowUTC()).ClearFinishedAt().ClearErrorMessage()
	} else if status == "pending" {
		u.ClearFinishedAt()
	} else {
		u.SetFinishedAt(timeutil.NowUTC())
	}
	if err := u.Exec(ctx); err != nil {
		return err
	}
	delta := EffectiveProgress(status, int64(row.SegmentTotal), int64(row.SegmentCompleted)) - before
	if delta != 0 {
		return tx.Job.UpdateOneID(row.JobID).AddProgressCompleted(delta).Exec(ctx)
	}
	return nil
}

// InitializeTotal is the compatibility path for modes not producing private
// candidates. It propagates failure instead of performing partial writes.
func InitializeTotal(ctx context.Context, tx *ent.Client, jobID, roundID, total int) error {
	row, err := loadCheckpointRound(ctx, tx, roundID)
	if err != nil {
		return err
	}
	if row.JobID != jobID {
		return ErrManifest
	}
	if row.ManifestSealed || row.SegmentTotal > 0 {
		return nil
	}
	if err := tx.JobRound.UpdateOneID(roundID).SetSegmentTotal(total).SetStartedAt(timeutil.NowUTC()).Exec(ctx); err != nil {
		return err
	}
	return ReconcileJob(ctx, tx, jobID)
}
