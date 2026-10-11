package workstate

import (
	"context"
	"fmt"
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
	rows, err := tx.JobRound.Query().Where(jobround.JobIDEQ(jobID)).All(ctx)
	if err != nil {
		return err
	}
	var total, completed int64
	for _, row := range rows {
		total += int64(row.SegmentTotal)
		completed += EffectiveProgress(row.Status, int64(row.SegmentTotal), int64(row.SegmentCompleted))
	}
	return tx.Job.UpdateOneID(jobID).SetProgressTotal(total).SetProgressCompleted(completed).Exec(ctx)
}

// Calibrate repairs count caches from their database sets. It is also called
// after cascading segment/round deletion, before the deletion transaction ends.
func Calibrate(ctx context.Context, tx *ent.Client, jobID int) error {
	rows, err := tx.JobRound.Query().Where(jobround.JobIDEQ(jobID)).Order(ent.Asc(jobround.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		n, err := tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(row.ID)).Count(ctx)
		if err != nil {
			return err
		}
		u := tx.JobRound.UpdateOneID(row.ID).SetSegmentCompleted(n)
		if row.ManifestSealed {
			total, err := tx.WorkItem.Query().Where(workitem.JobRoundIDEQ(row.ID)).Count(ctx)
			if err != nil {
				return err
			}
			if total < n {
				return fmt.Errorf("%w: round %d has completion outside manifest", ErrManifest, row.ID)
			}
			u.SetSegmentTotal(total)
		}
		if err := u.Exec(ctx); err != nil {
			return err
		}
	}
	return ReconcileJob(ctx, tx, jobID)
}

// Confirm inserts genuinely new facts under the Job lock. It never uses a
// reporter's memory count, so late legacy buffers cannot erase another writer.
func Confirm(ctx context.Context, tx *ent.Client, jobID, roundID int, facts []Confirmation) (int, error) {
	row, err := tx.JobRound.Get(ctx, roundID)
	if err != nil {
		return 0, err
	}
	if row.JobID != jobID {
		return 0, ErrManifest
	}
	before := EffectiveProgress(row.Status, int64(row.SegmentTotal), int64(row.SegmentCompleted))
	ids := make([]int, 0, len(facts))
	byID := make(map[int]Confirmation, len(facts))
	for _, fact := range facts {
		if fact.SegmentID > 0 {
			ids = append(ids, fact.SegmentID)
			byID[fact.SegmentID] = fact
		}
	}
	sort.Ints(ids)
	existing, err := tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(roundID), jobroundsegment.SegmentIDIn(ids...)).All(ctx)
	if err != nil {
		return 0, err
	}
	for _, fact := range existing {
		delete(byID, fact.SegmentID)
	}
	added := 0
	for _, id := range ids {
		fact, ok := byID[id]
		if !ok {
			continue
		}
		delete(byID, id)
		if row.ManifestSealed {
			ok, err := tx.WorkItem.Query().Where(workitem.JobRoundIDEQ(roundID), workitem.SegmentIDEQ(id)).Exist(ctx)
			if err != nil {
				return 0, err
			}
			if !ok {
				return 0, ErrManifest
			}
		}
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
		if err := b.Exec(ctx); err != nil {
			return 0, err
		}
		added++
	}
	// Counting the round's indexed relation also repairs pre-upgrade caches. No
	// per-segment scan over the job or a reporter's stale absolute value is used.
	count := row.SegmentCompleted + added
	if !row.ManifestSealed {
		count, err = tx.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(roundID)).Count(ctx)
		if err != nil {
			return 0, err
		}
	}
	if err := tx.JobRound.UpdateOneID(roundID).SetSegmentCompleted(count).Exec(ctx); err != nil {
		return 0, err
	}
	after := EffectiveProgress(row.Status, int64(row.SegmentTotal), int64(count))
	if delta := after - before; delta != 0 {
		if err := tx.Job.UpdateOneID(jobID).AddProgressCompleted(delta).Exec(ctx); err != nil {
			return 0, err
		}
	}
	return added, nil
}

func SetRoundStatus(ctx context.Context, tx *ent.Client, roundID int, allowed []string, status string) error {
	row, err := tx.JobRound.Get(ctx, roundID)
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
	row, err := tx.JobRound.Get(ctx, roundID)
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
