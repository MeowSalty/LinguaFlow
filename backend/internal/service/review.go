package service

import (
	"context"
	"errors"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/subjob"
)

const (
	SegmentStatusPending    = "pending"
	SegmentStatusTranslated = "translated"
	SegmentStatusReviewed   = "reviewed"
	SegmentStatusEdited     = "edited"
	SegmentStatusApproved   = "approved"
	SegmentStatusRejected   = "rejected"
)

var (
	ErrSegmentNotFound     = errors.New("segment not found")
	ErrInvalidReviewState  = errors.New("invalid review state")
	ErrRetranslateNoReject = errors.New("no rejected segments to retranslate")
)

type ReviewService struct {
	client   *ent.Client
	projects *ProjectService
}

type SegmentEditInput struct {
	TargetText string
	Comment    string
}

type SegmentDecisionInput struct {
	Comment string
}

type SegmentPage struct {
	Items      []*ent.Segment
	NextCursor int
}

func NewReviewService(client *ent.Client, projects *ProjectService) *ReviewService {
	return &ReviewService{client: client, projects: projects}
}

func (s *ReviewService) ListJobSegments(ctx context.Context, actorUserID, jobID, afterID, limit int) (*SegmentPage, error) {
	if _, err := s.authorizeJob(ctx, actorUserID, jobID, false); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	predicates := []func(*ent.SegmentQuery){
		func(q *ent.SegmentQuery) {
			q.Where(segment.HasSubJobWith(subjob.HasJobWith(job.IDEQ(jobID))))
		},
	}
	if afterID > 0 {
		predicates = append(predicates, func(q *ent.SegmentQuery) { q.Where(segment.IDGT(afterID)) })
	}
	query := s.client.Segment.Query()
	for _, apply := range predicates {
		apply(query)
	}
	rows, err := query.Order(ent.Asc(segment.FieldID)).Limit(limit + 1).WithReviewedBy().WithSubJob().All(ctx)
	if err != nil {
		return nil, err
	}
	page := &SegmentPage{Items: rows}
	if len(rows) > limit {
		page.NextCursor = rows[limit-1].ID
		page.Items = rows[:limit]
	}
	return page, nil
}

func (s *ReviewService) EditSegment(ctx context.Context, actorUserID, segmentID int, input SegmentEditInput) (*ent.Segment, error) {
	if strings.TrimSpace(input.TargetText) == "" {
		return nil, ErrInvalidInput
	}
	if _, err := s.authorizeSegment(ctx, actorUserID, segmentID, true); err != nil {
		return nil, err
	}
	update := s.client.Segment.UpdateOneID(segmentID).
		SetTargetText(input.TargetText).
		SetStatus(SegmentStatusEdited).
		SetReviewedByID(actorUserID)
	if strings.TrimSpace(input.Comment) == "" {
		update.ClearReviewComment()
	} else {
		update.SetReviewComment(strings.TrimSpace(input.Comment))
	}
	updated, err := update.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}
	_ = s.reconcileJobReviewStatus(ctx, updated)
	return s.client.Segment.Query().Where(segment.IDEQ(updated.ID)).WithReviewedBy().WithSubJob().Only(ctx)
}

func (s *ReviewService) ApproveSegment(ctx context.Context, actorUserID, segmentID int, input SegmentDecisionInput) (*ent.Segment, error) {
	if _, err := s.authorizeSegment(ctx, actorUserID, segmentID, true); err != nil {
		return nil, err
	}
	update := s.client.Segment.UpdateOneID(segmentID).
		SetStatus(SegmentStatusApproved).
		SetReviewedByID(actorUserID)
	if strings.TrimSpace(input.Comment) == "" {
		update.ClearReviewComment()
	} else {
		update.SetReviewComment(strings.TrimSpace(input.Comment))
	}
	updated, err := update.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}
	_ = s.reconcileJobReviewStatus(ctx, updated)
	return s.client.Segment.Query().Where(segment.IDEQ(updated.ID)).WithReviewedBy().WithSubJob().Only(ctx)
}

func (s *ReviewService) RejectSegment(ctx context.Context, actorUserID, segmentID int, input SegmentDecisionInput) (*ent.Segment, error) {
	if _, err := s.authorizeSegment(ctx, actorUserID, segmentID, true); err != nil {
		return nil, err
	}
	update := s.client.Segment.UpdateOneID(segmentID).
		SetStatus(SegmentStatusRejected).
		SetReviewedByID(actorUserID)
	if strings.TrimSpace(input.Comment) == "" {
		update.ClearReviewComment()
	} else {
		update.SetReviewComment(strings.TrimSpace(input.Comment))
	}
	updated, err := update.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}
	_ = s.reconcileJobReviewStatus(ctx, updated)
	return s.client.Segment.Query().Where(segment.IDEQ(updated.ID)).WithReviewedBy().WithSubJob().Only(ctx)
}

func (s *ReviewService) ApproveJob(ctx context.Context, actorUserID, jobID int) (*ent.Job, error) {
	if _, err := s.authorizeJob(ctx, actorUserID, jobID, true); err != nil {
		return nil, err
	}
	if err := s.client.Segment.Update().
		Where(segment.HasSubJobWith(subjob.HasJobWith(job.IDEQ(jobID))), segment.StatusNEQ(SegmentStatusRejected)).
		SetStatus(SegmentStatusApproved).
		SetReviewedByID(actorUserID).
		Exec(ctx); err != nil {
		return nil, err
	}
	if err := s.reconcileJobByID(ctx, jobID); err != nil {
		return nil, err
	}
	return s.client.Job.Query().Where(job.IDEQ(jobID)).WithSubJobs().Only(ctx)
}

func (s *ReviewService) RetranslateRejected(ctx context.Context, actorUserID, jobID int) error {
	if _, err := s.authorizeJob(ctx, actorUserID, jobID, true); err != nil {
		return err
	}
	count, err := s.client.Segment.Query().
		Where(segment.HasSubJobWith(subjob.HasJobWith(job.IDEQ(jobID))), segment.StatusEQ(SegmentStatusRejected)).
		Count(ctx)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrRetranslateNoReject
	}
	if err := s.client.SubJob.Update().
		Where(subjob.HasJobWith(job.IDEQ(jobID)), subjob.HasSegmentsWith(segment.StatusEQ(SegmentStatusRejected))).
		SetStatus(SubJobStatusPending).
		ClearErrorMessage().
		Exec(ctx); err != nil {
		return err
	}
	return s.client.Job.UpdateOneID(jobID).
		SetStatus(JobStatusPending).
		ClearErrorMessage().
		Exec(ctx)
}

func (s *ReviewService) authorizeSegment(ctx context.Context, actorUserID, segmentID int, write bool) (*ent.Segment, error) {
	row, err := s.client.Segment.Query().
		Where(segment.IDEQ(segmentID)).
		WithSubJob(func(q *ent.SubJobQuery) { q.WithJob(func(jq *ent.JobQuery) { jq.WithProject() }) }).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}
	sub, err := row.Edges.SubJobOrErr()
	if err != nil {
		return nil, err
	}
	jobRow, err := sub.Edges.JobOrErr()
	if err != nil {
		return nil, err
	}
	projectRow, err := jobRow.Edges.ProjectOrErr()
	if err != nil {
		return nil, err
	}
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectRow.ID, write); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *ReviewService) authorizeJob(ctx context.Context, actorUserID, jobID int, write bool) (*ent.Job, error) {
	row, err := s.client.Job.Query().Where(job.IDEQ(jobID)).WithProject().Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	projectRow, err := row.Edges.ProjectOrErr()
	if err != nil {
		return nil, err
	}
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectRow.ID, write); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *ReviewService) reconcileJobReviewStatus(ctx context.Context, row *ent.Segment) error {
	loaded, err := s.client.Segment.Query().
		Where(segment.IDEQ(row.ID)).
		WithSubJob(func(q *ent.SubJobQuery) { q.WithJob() }).
		Only(ctx)
	if err != nil {
		return err
	}
	sub, err := loaded.Edges.SubJobOrErr()
	if err != nil {
		return err
	}
	jobRow, err := sub.Edges.JobOrErr()
	if err != nil {
		return err
	}
	return s.reconcileJobByID(ctx, jobRow.ID)
}

func (s *ReviewService) reconcileJobByID(ctx context.Context, jobID int) error {
	total, err := s.client.Segment.Query().Where(segment.HasSubJobWith(subjob.HasJobWith(job.IDEQ(jobID)))).Count(ctx)
	if err != nil {
		return err
	}
	if total == 0 {
		return nil
	}
	approved, err := s.client.Segment.Query().Where(segment.HasSubJobWith(subjob.HasJobWith(job.IDEQ(jobID))), segment.StatusEQ(SegmentStatusApproved)).Count(ctx)
	if err != nil {
		return err
	}
	if approved == total {
		return s.client.Job.UpdateOneID(jobID).SetStatus(JobStatusCompleted).Exec(ctx)
	}
	return s.client.Job.UpdateOneID(jobID).SetStatus(JobStatusAwaitingReview).Exec(ctx)
}
