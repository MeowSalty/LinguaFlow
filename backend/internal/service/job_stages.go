package service

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type JobStageCounts = workstate.StageCounts
type jobPageStageKey struct{}
type jobPageStageObservation struct {
	actor  int
	counts map[int]*JobStageCounts
}

// PrepareJobStageObservation resolves authorized page IDs once. The context is
// local to response construction and never authorizes a later write operation.
func (s *JobService) PrepareJobStageObservation(ctx context.Context, actor int, rows []*ent.Job) context.Context {
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	counts, _ := s.GetJobStageCountsBatch(ctx, actor, ids)
	return context.WithValue(ctx, jobPageStageKey{}, jobPageStageObservation{actor: actor, counts: counts})
}

func (s *JobService) GetJobStageCountsBatch(ctx context.Context, actor int, ids []int) (map[int]*JobStageCounts, error) {
	if len(ids) == 0 {
		return map[int]*JobStageCounts{}, nil
	}
	allowed, err := s.client.Job.Query().Where(job.IDIn(ids...), job.HasProjectWith(readableProjectPredicate(actor))).IDs(ctx)
	if err != nil {
		return nil, err
	}
	return workstate.ReadStageCountsBatch(ctx, s.client, allowed)
}

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
	if page, ok := ctx.Value(jobPageStageKey{}).(jobPageStageObservation); ok && page.actor == actorID {
		return page.counts[jobID], nil
	}
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
