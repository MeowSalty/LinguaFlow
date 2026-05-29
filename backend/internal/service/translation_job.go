package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/translationjob"
)

const (
	TranslationJobStatusPending        = "pending"
	TranslationJobStatusRunning        = "running"
	TranslationJobStatusAwaitingReview = "awaiting_review"
	TranslationJobStatusCompleted      = "completed"
	TranslationJobStatusFailed         = "failed"
	TranslationJobStatusCancelled      = "cancelled"

	TranslationJobTriggerManual = "manual"

	JobResourceStatusPending   = "pending"
	JobResourceStatusRunning   = "running"
	JobResourceStatusCompleted = "completed"
	JobResourceStatusFailed    = "failed"
	JobResourceStatusCancelled = "cancelled"
)

var (
	ErrTranslationJobNotFound     = errors.New("translation job not found")
	ErrTranslationJobEmpty        = errors.New("translation job has no pending segments")
	ErrJobResourceNotFound        = errors.New("job resource not found")
	ErrTranslationJobActorMissing = errors.New("translation job actor unavailable")
)

type TranslationJobService struct {
	client   *ent.Client
	projects *ProjectService
}

type CreateTranslationJobInput struct {
	ResourceIDs       []int
	SegmentIDs        []int
	TranslationConfig map[string]any
}

type TranslationJobListOptions struct {
	Status      string
	TriggerType string
	AfterID     int
	Limit       int
}

func NewTranslationJobService(client *ent.Client, projects *ProjectService) *TranslationJobService {
	return &TranslationJobService{client: client, projects: projects}
}

type TranslationJobExecution struct {
	Job          *ent.TranslationJob
	Project      *ent.Project
	JobResources []*ent.JobResource
	ActorUserID  int
}

func (s *TranslationJobService) CreateManualJob(ctx context.Context, actorUserID, projectID int, input CreateTranslationJobInput) (*ent.TranslationJob, error) {
	projectRow, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true)
	if err != nil {
		return nil, err
	}

	selection, err := s.resolveJobSelection(ctx, projectID, input)
	if err != nil {
		return nil, err
	}
	if len(selection) == 0 {
		return nil, ErrTranslationJobEmpty
	}

	resourceIDs := make([]int, 0, len(selection))
	totalSegments := 0
	for resourceID, segmentIDs := range selection {
		resourceIDs = append(resourceIDs, resourceID)
		totalSegments += len(segmentIDs)
	}
	sort.Ints(resourceIDs)

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	translationConfig := mergeConfigMaps(projectRow.DefaultTranslationConfig, input.TranslationConfig)
	created, err := tx.TranslationJob.Create().
		SetProjectID(projectID).
		SetCreatedByID(actorUserID).
		SetStatus(TranslationJobStatusPending).
		SetTriggerType(TranslationJobTriggerManual).
		SetTranslationConfig(translationConfig).
		SetResourceCount(len(selection)).
		SetTotalSegments(totalSegments).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	for _, resourceID := range resourceIDs {
		segmentIDs := append([]int(nil), selection[resourceID]...)
		sort.Ints(segmentIDs)
		if _, err := tx.JobResource.Create().
			SetJobID(created.ID).
			SetResourceID(resourceID).
			SetStatus(JobResourceStatusPending).
			SetSegmentIds(segmentIDs).
			SetSegmentCount(len(segmentIDs)).
			Save(ctx); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true

	return s.GetJob(ctx, actorUserID, created.ID)
}

func (s *TranslationJobService) RecoverPendingJobs(ctx context.Context) ([]int, error) {
	jobs, err := s.client.TranslationJob.Query().
		Where(translationjob.StatusIn(TranslationJobStatusPending, TranslationJobStatusRunning)).
		Order(ent.Asc(translationjob.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(jobs))
	for _, current := range jobs {
		ids = append(ids, current.ID)
		if current.Status == TranslationJobStatusRunning {
			if err := s.client.TranslationJob.UpdateOneID(current.ID).SetStatus(TranslationJobStatusPending).Exec(ctx); err != nil {
				return nil, err
			}
		}
		if err := s.client.JobResource.Update().
			Where(jobresource.HasJobWith(translationjob.IDEQ(current.ID)), jobresource.StatusEQ(JobResourceStatusRunning)).
			SetStatus(JobResourceStatusPending).
			Exec(ctx); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func (s *TranslationJobService) LoadJobExecution(ctx context.Context, jobID int) (*TranslationJobExecution, error) {
	current, err := s.client.TranslationJob.Query().
		Where(translationjob.IDEQ(jobID)).
		WithProject().
		WithCreatedBy().
		WithJobResources(func(q *ent.JobResourceQuery) {
			q.WithResource().Order(ent.Asc(jobresource.FieldID))
		}).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrTranslationJobNotFound
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
		return nil, ErrTranslationJobActorMissing
	}
	return &TranslationJobExecution{Job: current, Project: projectRow, JobResources: current.Edges.JobResources, ActorUserID: actorUserID}, nil
}

func (s *TranslationJobService) MarkJobRunning(ctx context.Context, jobID int) error {
	return s.client.TranslationJob.UpdateOneID(jobID).SetStatus(TranslationJobStatusRunning).Exec(ctx)
}

func (s *TranslationJobService) MarkJobResourceRunning(ctx context.Context, jobResourceID int) error {
	if err := s.client.JobResource.UpdateOneID(jobResourceID).
		SetStatus(JobResourceStatusRunning).
		ClearErrorMessage().
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrJobResourceNotFound
		}
		return err
	}
	return nil
}

func (s *TranslationJobService) MarkJobResourceCompleted(ctx context.Context, jobResourceID int, outputPath string, completedSegments int) error {
	if err := s.client.JobResource.UpdateOneID(jobResourceID).
		SetStatus(JobResourceStatusCompleted).
		SetOutputPath(strings.TrimSpace(outputPath)).
		SetCompletedSegments(completedSegments).
		ClearErrorMessage().
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrJobResourceNotFound
		}
		return err
	}
	return nil
}

func (s *TranslationJobService) MarkJobResourceFailed(ctx context.Context, jobResourceID int, failure error) error {
	message := "job resource failed"
	if failure != nil {
		message = failure.Error()
	}
	if err := s.client.JobResource.UpdateOneID(jobResourceID).
		SetStatus(JobResourceStatusFailed).
		SetErrorMessage(message).
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrJobResourceNotFound
		}
		return err
	}
	return nil
}

func (s *TranslationJobService) ReconcileJob(ctx context.Context, jobID int) error {
	current, err := s.client.TranslationJob.Query().
		Where(translationjob.IDEQ(jobID)).
		WithJobResources().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrTranslationJobNotFound
		}
		return err
	}
	var pendingCount, runningCount, completed, failed, cancelled, completedSegments int
	var firstFailure *string
	for _, item := range current.Edges.JobResources {
		completedSegments += item.CompletedSegments
		switch item.Status {
		case JobResourceStatusPending:
			pendingCount++
		case JobResourceStatusRunning:
			runningCount++
		case JobResourceStatusCompleted:
			completed++
		case JobResourceStatusCancelled:
			cancelled++
		default:
			failed++
			if firstFailure == nil && item.ErrorMessage != nil {
				msg := *item.ErrorMessage
				firstFailure = &msg
			}
		}
	}
	status := deriveTranslationJobStatus(len(current.Edges.JobResources), pendingCount, runningCount, completed, failed, cancelled)
	update := s.client.TranslationJob.UpdateOneID(jobID).
		SetStatus(status).
		SetResourceCount(len(current.Edges.JobResources)).
		SetCompletedResources(completed).
		SetFailedResources(failed).
		SetCompletedSegments(completedSegments)
	if firstFailure != nil && status == TranslationJobStatusFailed {
		update.SetErrorMessage(*firstFailure)
	} else {
		update.ClearErrorMessage()
	}
	if err := update.Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrTranslationJobNotFound
		}
		return err
	}
	return nil
}

func deriveTranslationJobStatus(total, pendingCount, runningCount, completed, failed, cancelled int) string {
	if total == 0 {
		return TranslationJobStatusPending
	}
	if runningCount > 0 {
		return TranslationJobStatusRunning
	}
	if completed == total {
		return TranslationJobStatusAwaitingReview
	}
	if cancelled == total {
		return TranslationJobStatusCancelled
	}
	if pendingCount == total {
		return TranslationJobStatusPending
	}
	if failed > 0 && completed+failed+cancelled == total {
		return TranslationJobStatusFailed
	}
	if completed > 0 || failed > 0 || cancelled > 0 {
		return TranslationJobStatusRunning
	}
	return TranslationJobStatusPending
}

func (s *TranslationJobService) ListJobs(ctx context.Context, actorUserID, projectID int, opts TranslationJobListOptions) ([]*ent.TranslationJob, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, false); err != nil {
		return nil, err
	}
	if opts.Limit <= 0 || opts.Limit > 100 {
		opts.Limit = 50
	}
	q := s.client.TranslationJob.Query().Where(translationjob.HasProjectWith(project.IDEQ(projectID)))
	if opts.AfterID > 0 {
		q = q.Where(translationjob.IDGT(opts.AfterID))
	}
	if status := strings.TrimSpace(opts.Status); status != "" {
		q = q.Where(translationjob.StatusEQ(status))
	}
	if triggerType := strings.TrimSpace(opts.TriggerType); triggerType != "" {
		q = q.Where(translationjob.TriggerTypeEQ(triggerType))
	}
	return q.Order(ent.Asc(translationjob.FieldID)).Limit(opts.Limit).WithJobResources(func(q *ent.JobResourceQuery) {
		q.WithResource().Order(ent.Asc(jobresource.FieldID))
	}).All(ctx)
}

func (s *TranslationJobService) GetJob(ctx context.Context, actorUserID, jobID int) (*ent.TranslationJob, error) {
	row, err := s.client.TranslationJob.Query().
		Where(translationjob.IDEQ(jobID)).
		WithProject().
		WithCreatedBy().
		WithJobResources(func(q *ent.JobResourceQuery) {
			q.WithResource().Order(ent.Asc(jobresource.FieldID))
		}).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrTranslationJobNotFound
		}
		return nil, err
	}
	projectRow, err := row.Edges.ProjectOrErr()
	if err != nil {
		return nil, err
	}
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectRow.ID, false); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *TranslationJobService) resolveJobSelection(ctx context.Context, projectID int, input CreateTranslationJobInput) (map[int][]int, error) {
	if len(input.SegmentIDs) > 0 {
		return s.resolveSegmentSelection(ctx, projectID, input.SegmentIDs)
	}
	return s.resolveResourceSelection(ctx, projectID, input.ResourceIDs)
}

func (s *TranslationJobService) resolveSegmentSelection(ctx context.Context, projectID int, segmentIDs []int) (map[int][]int, error) {
	rows, err := s.client.Segment.Query().
		Where(segment.IDIn(uniqueInts(segmentIDs)...), segment.HasResourceWith(resource.ProjectIDEQ(projectID))).
		All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) != len(uniqueInts(segmentIDs)) {
		return nil, ErrSegmentNotFound
	}
	selection := make(map[int][]int)
	for _, row := range rows {
		if row.ResourceID == nil {
			continue
		}
		selection[*row.ResourceID] = append(selection[*row.ResourceID], row.ID)
	}
	return selection, nil
}

func (s *TranslationJobService) resolveResourceSelection(ctx context.Context, projectID int, resourceIDs []int) (map[int][]int, error) {
	resourceQuery := s.client.Resource.Query().Where(resource.ProjectIDEQ(projectID))
	if len(resourceIDs) > 0 {
		ids := uniqueInts(resourceIDs)
		resourceQuery = resourceQuery.Where(resource.IDIn(ids...))
		count, err := resourceQuery.Clone().Count(ctx)
		if err != nil {
			return nil, err
		}
		if count != len(ids) {
			return nil, ErrResourceNotFound
		}
	}
	resources, err := resourceQuery.All(ctx)
	if err != nil {
		return nil, err
	}
	selection := make(map[int][]int)
	for _, res := range resources {
		segments, err := s.client.Segment.Query().
			Where(segment.ResourceIDEQ(res.ID), segment.StatusIn(SegmentStatusPending, SegmentStatusRejected)).
			Order(ent.Asc(segment.FieldID)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		if len(segments) == 0 {
			continue
		}
		ids := make([]int, 0, len(segments))
		for _, item := range segments {
			ids = append(ids, item.ID)
		}
		selection[res.ID] = ids
	}
	return selection, nil
}

func uniqueInts(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}
