package service

import (
	"context"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

// PrepareRecovery coordinates persisted execution state before any translation
// worker starts. Unlike the legacy ID-returning recovery API, it loads bounded
// pages containing only identity and status, not every execution snapshot.
func (s *JobService) PrepareRecovery(ctx context.Context) error {
	const pageSize = 128
	afterID := 0
	for {
		rows, err := s.client.Job.Query().
			Where(job.StatusIn(JobStatusPending, JobStatusRunning), job.IDGT(afterID)).
			Order(ent.Asc(job.FieldID)).Limit(pageSize).
			Select(job.FieldID, job.FieldStatus).All(ctx)
		if err != nil {
			return fmt.Errorf("prepare translation recovery: %w", err)
		}
		for _, current := range rows {
			afterID = current.ID
			if err := s.prepareRecoveredJob(ctx, current.ID); err != nil {
				return err
			}
		}
		if len(rows) < pageSize {
			return nil
		}
	}
}

func (s *JobService) prepareRecoveredJob(ctx context.Context, id int) error {
	guard, err := s.lifecycle.Lock(ctx, "translation", id)
	if err != nil {
		return err
	}
	defer guard.Release()
	if guard.Active() {
		return tasklife.ErrBusy
	}
	return withOrganizationTransaction(ctx, s.client, func(client *ent.Client) error {
		count, err := client.Job.Update().Where(job.IDEQ(id), job.StatusIn(JobStatusPending, JobStatusRunning)).
			SetStatus(JobStatusPending).ClearFinishedAt().ClearRetentionAnchorAt().Save(ctx)
		if err != nil || count == 0 {
			return err
		}
		if err := client.JobResource.Update().
			Where(jobresource.HasJobWith(job.IDEQ(id)), jobresource.StatusEQ(JobResourceStatusRunning)).
			SetStatus(JobResourceStatusPending).Exec(ctx); err != nil {
			return err
		}
		if err := client.JobRound.Update().Where(
			jobround.JobIDEQ(id),
			jobround.HasJobResourceWith(jobresource.StatusIn(JobResourceStatusPending, JobResourceStatusRunning)),
			jobround.StatusIn(JobRoundStatusFailed, JobRoundStatusRunning, JobRoundStatusSkipped),
		).SetStatus(JobRoundStatusPending).Exec(ctx); err != nil {
			return err
		}
		txService := *s
		txService.client = client
		if err := txService.backfillJobRoundsForRecovery(ctx, id); err != nil {
			return err
		}
		return recomputeJobProgress(ctx, clientProgressStore{client}, id)
	})
}

// PendingTaskIDs is deliberately read-only: periodic discovery must never
// change the state of a currently executing job.
func (s *JobService) PendingTaskIDs(ctx context.Context, afterID, limit int) ([]int, error) {
	if afterID < 0 || limit < 1 || limit > 128 {
		return nil, fmt.Errorf("invalid pending task page")
	}
	return s.client.Job.Query().
		Where(job.StatusEQ(JobStatusPending), job.IDGT(afterID)).
		Order(ent.Asc(job.FieldID)).Limit(limit).Select(job.FieldID).Ints(ctx)
}
