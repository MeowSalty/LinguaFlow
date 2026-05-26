package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/subjob"
)

const (
	JobStatusPending        = "pending"
	JobStatusRunning        = "running"
	JobStatusAwaitingReview = "awaiting_review"
	JobStatusCompleted      = "completed"
	JobStatusFailed         = "failed"
	JobStatusCancelled      = "cancelled"

	SubJobStatusPending   = "pending"
	SubJobStatusRunning   = "running"
	SubJobStatusCompleted = "completed"
	SubJobStatusFailed    = "failed"
	SubJobStatusCancelled = "cancelled"
)

var (
	ErrJobNotFound         = errors.New("job not found")
	ErrSubJobNotFound      = errors.New("subjob not found")
	ErrJobActorUnavailable = errors.New("job actor unavailable")
)

type JobService struct {
	client   *ent.Client
	projects *ProjectService
}

type JobExecution struct {
	Job         *ent.Job
	Project     *ent.Project
	SubJobs     []*ent.SubJob
	ActorUserID int
}

func NewJobService(client *ent.Client, projects *ProjectService) *JobService {
	return &JobService{client: client, projects: projects}
}

func (s *JobService) RecoverPendingJobs(ctx context.Context) ([]int, error) {
	jobs, err := s.client.Job.Query().
		Where(job.StatusIn(JobStatusPending, JobStatusRunning)).
		Order(ent.Asc(job.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(jobs))
	for _, current := range jobs {
		ids = append(ids, current.ID)
		if current.Status == JobStatusRunning {
			if err := s.client.Job.UpdateOneID(current.ID).
				SetStatus(JobStatusPending).
				Exec(ctx); err != nil {
				return nil, err
			}
		}
		if err := s.client.SubJob.Update().
			Where(subjob.HasJobWith(job.IDEQ(current.ID)), subjob.StatusEQ(SubJobStatusRunning)).
			SetStatus(SubJobStatusPending).
			Exec(ctx); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func (s *JobService) LoadJobExecution(ctx context.Context, jobID int) (*JobExecution, error) {
	current, err := s.client.Job.Query().
		Where(job.IDEQ(jobID)).
		WithProject().
		WithCreatedBy().
		WithSubJobs(func(q *ent.SubJobQuery) {
			q.Order(ent.Asc(subjob.FieldID))
		}).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	projectRow, err := current.Edges.ProjectOrErr()
	if err != nil {
		return nil, err
	}
	actorUserID := 0
	if current.Edges.CreatedBy != nil {
		actorUserID = current.Edges.CreatedBy.ID
	} else if projectRow.OwnerUserID != nil {
		actorUserID = *projectRow.OwnerUserID
	}
	if actorUserID <= 0 {
		return nil, ErrJobActorUnavailable
	}
	return &JobExecution{
		Job:         current,
		Project:     projectRow,
		SubJobs:     current.Edges.SubJobs,
		ActorUserID: actorUserID,
	}, nil
}

func (s *JobService) MarkJobRunning(ctx context.Context, jobID int) error {
	return s.client.Job.UpdateOneID(jobID).SetStatus(JobStatusRunning).Exec(ctx)
}

func (s *JobService) MarkSubJobRunning(ctx context.Context, subJobID int) error {
	updated, err := s.client.SubJob.UpdateOneID(subJobID).
		SetStatus(SubJobStatusRunning).
		ClearErrorMessage().
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrSubJobNotFound
		}
		return err
	}
	_ = updated
	return nil
}

func (s *JobService) MarkSubJobCompleted(ctx context.Context, subJobID int, outputPath string, segmentCount int) error {
	update := s.client.SubJob.UpdateOneID(subJobID).
		SetStatus(SubJobStatusCompleted).
		SetOutputPath(strings.TrimSpace(outputPath)).
		SetSegmentCount(segmentCount).
		ClearErrorMessage()
	if err := update.Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrSubJobNotFound
		}
		return err
	}
	return nil
}

func (s *JobService) MarkSubJobFailed(ctx context.Context, subJobID int, failure error) error {
	message := "subjob failed"
	if failure != nil {
		message = failure.Error()
	}
	if err := s.client.SubJob.UpdateOneID(subJobID).
		SetStatus(SubJobStatusFailed).
		SetErrorMessage(message).
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrSubJobNotFound
		}
		return err
	}
	return nil
}

func (s *JobService) ReconcileJob(ctx context.Context, jobID int) error {
	current, err := s.client.Job.Query().
		Where(job.IDEQ(jobID)).
		WithSubJobs().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrJobNotFound
		}
		return err
	}
	var (
		total        = len(current.Edges.SubJobs)
		pendingCount int
		runningCount int
		completed    int
		failed       int
		cancelled    int
		firstFailure *string
	)
	for _, sub := range current.Edges.SubJobs {
		switch sub.Status {
		case SubJobStatusPending:
			pendingCount++
		case SubJobStatusRunning:
			runningCount++
		case SubJobStatusCompleted:
			completed++
		case SubJobStatusCancelled:
			cancelled++
		default:
			failed++
			if firstFailure == nil && sub.ErrorMessage != nil {
				msg := *sub.ErrorMessage
				firstFailure = &msg
			}
		}
	}
	status := deriveJobStatus(total, pendingCount, runningCount, completed, failed, cancelled)
	update := s.client.Job.UpdateOneID(jobID).
		SetStatus(status).
		SetSubJobCount(total).
		SetCompletedSubJobs(completed).
		SetFailedSubJobs(failed)
	if firstFailure != nil && status == JobStatusFailed {
		update.SetErrorMessage(*firstFailure)
	} else {
		update.ClearErrorMessage()
	}
	if err := update.Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrJobNotFound
		}
		return err
	}
	return nil
}

func deriveJobStatus(total, pendingCount, runningCount, completed, failed, cancelled int) string {
	if total == 0 {
		return JobStatusPending
	}
	if runningCount > 0 {
		return JobStatusRunning
	}
	if completed == total {
		return JobStatusAwaitingReview
	}
	if cancelled == total {
		return JobStatusCancelled
	}
	if pendingCount == total {
		return JobStatusPending
	}
	if failed > 0 && completed+failed+cancelled == total {
		return JobStatusFailed
	}
	if completed > 0 || failed > 0 || cancelled > 0 {
		return JobStatusRunning
	}
	return JobStatusPending
}

var _ = project.FieldID

func (s *JobService) String() string {
	return fmt.Sprintf("JobService(%p)", s)
}
