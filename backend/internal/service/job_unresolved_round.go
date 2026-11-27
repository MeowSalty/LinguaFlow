package service

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// MarkJobRoundUnresolved reopens the progress contribution of a closed round
// when its planned successor did not take over the remaining members.
func (s *JobService) MarkJobRoundUnresolved(ctx context.Context, jobID, roundID int, failure error) error {
	return workstate.Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := workstate.LockJob(ctx, tx, jobID); err != nil {
			return err
		}
		running, err := tx.Job.Query().Where(job.IDEQ(jobID), job.StatusEQ(JobStatusRunning), job.PauseRequestedEQ(false)).Exist(ctx)
		if err != nil || !running {
			return err
		}
		status, err := tx.JobRound.Query().Where(jobround.IDEQ(roundID), jobround.JobIDEQ(jobID)).Select(jobround.FieldStatus).Only(ctx)
		if err != nil {
			return err
		}
		if status.Status != JobRoundStatusCompleted {
			return nil
		}
		if err := workstate.SetRoundStatus(ctx, tx, roundID, []string{JobRoundStatusCompleted}, JobRoundStatusFailed); err != nil {
			return err
		}
		return tx.JobRound.UpdateOneID(roundID).SetErrorMessage(failure.Error()).Exec(ctx)
	})
}
