package service

import (
	"context"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
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
			if current.Status == JobStatusRunning {
				if err := s.client.Job.Update().
					Where(job.IDEQ(current.ID), job.StatusEQ(JobStatusRunning)).
					SetStatus(JobStatusPending).Exec(ctx); err != nil {
					return err
				}
			}
			if err := s.client.JobResource.Update().
				Where(jobresource.HasJobWith(job.IDEQ(current.ID)), jobresource.StatusEQ(JobResourceStatusRunning)).
				SetStatus(JobResourceStatusPending).Exec(ctx); err != nil {
				return err
			}
			if err := s.client.JobRound.Update().Where(
				jobround.JobIDEQ(current.ID),
				jobround.HasJobResourceWith(jobresource.StatusIn(JobResourceStatusPending, JobResourceStatusRunning)),
				jobround.StatusIn(JobRoundStatusFailed, JobRoundStatusRunning, JobRoundStatusSkipped),
			).SetStatus(JobRoundStatusPending).Exec(ctx); err != nil {
				return err
			}
			if err := s.backfillJobRoundsForRecovery(ctx, current.ID); err != nil {
				return err
			}
			if err := recomputeJobProgress(ctx, clientProgressStore{s.client}, current.ID); err != nil {
				return err
			}
		}
		if len(rows) < pageSize {
			return nil
		}
	}
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
