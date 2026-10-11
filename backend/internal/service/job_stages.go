package service

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type JobStageCounts = workstate.StageCounts

func (s *JobService) GetJobStageCounts(ctx context.Context, actorID, jobID int) (*JobStageCounts, error) {
	if err := s.CheckJobAccess(ctx, actorID, jobID); err != nil {
		return nil, err
	}
	return workstate.ReadStageCounts(ctx, s.client, jobID)
}

// publishStageCounts observes an already committed lifecycle change. Failure is
// diagnostic only; it never creates a new completion fact or resets counters.
func (s *JobService) publishStageCounts(ctx context.Context, jobID int) {
	if s.broker == nil {
		return
	}
	counts, err := workstate.ReadStageCounts(ctx, s.client, jobID)
	if err != nil {
		return
	}
	s.broker.Publish(jobID, event.Event{Type: "stage_counts", JobID: jobID, Level: "info", Message: "任务阶段进度", Metadata: map[string]any{"stages": counts}, CreatedAt: timeutil.NowUTC()})
}
