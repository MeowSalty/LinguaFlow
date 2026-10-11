package service

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

// resetWorkEpoch only touches unfinished members in resources actually reset
// by RetryJob. Resume and startup recovery never call it.
func resetWorkEpoch(ctx context.Context, tx *ent.Client, jobID int, epoch int64) error {
	rows, err := tx.WorkItem.Query().Where(workitem.JobIDEQ(jobID), workitem.StateNEQ("resolved"), workitem.HasRoundWith(jobround.StatusEQ(JobRoundStatusPending), jobround.HasJobResourceWith(jobresource.StatusEQ(JobResourceStatusPending)))).All(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		state := "pending"
		if row.CandidateID != "" {
			valid, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(row.CandidateID), workcandidate.StateIn("pending_alignment", "ready_to_commit")).Exist(ctx)
			if err != nil {
				return err
			}
			if valid {
				state = "candidate"
			}
		}
		if err := tx.WorkItem.UpdateOneID(row.ID).SetRetryEpoch(epoch).SetState(state).SetPoolIndex(0).SetMainAttempts(0).SetAlignmentAttempts(0).SetNetworkAttempts(0).SetMainNetworkAttempts(0).SetAlignmentNetworkAttempts(0).SetPromptPhase("initial").ClearCursor().ClearNextAttemptAt().SetLastError("").Exec(ctx); err != nil {
			return err
		}
	}
	_, err = tx.JobRound.Update().Where(jobround.JobIDEQ(jobID), jobround.StatusEQ(JobRoundStatusPending), jobround.HasJobResourceWith(jobresource.StatusEQ(JobResourceStatusPending))).SetPoolIndex(0).Save(ctx)
	return err
}
