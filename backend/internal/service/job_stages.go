package service

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type JobStageCounts = workstate.StageCounts
type controlStageKey struct{}
type controlStageObservation struct {
	actor, id int
	counts    *JobStageCounts
}

// Control DTO construction observes these already collected fixed scalars;
// the SQL counts run before acquiring its short CPU owner.
func (s *JobService) PrepareExecutionControlObservation(ctx context.Context, actor, id int) context.Context {
	counts, _ := s.GetJobStageCounts(ctx, actor, id)
	return context.WithValue(ctx, controlStageKey{}, controlStageObservation{actor, id, counts})
}

func (s *JobService) GetJobStageCounts(ctx context.Context, actorID, jobID int) (*JobStageCounts, error) {
	if observation, ok := ctx.Value(controlStageKey{}).(controlStageObservation); ok && observation.actor == actorID && observation.id == jobID {
		return observation.counts, nil
	}
	definition, control := ctx.Value(controlDefinitionKey{}).(*controlDefinition)
	if control && definition.id == jobID {
		ok, err := s.client.Job.Query().Where(job.IDEQ(jobID), job.HasProjectWith(readableProjectPredicate(actorID))).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrForbidden
		}
	} else if err := s.CheckJobAccess(ctx, actorID, jobID); err != nil {
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
