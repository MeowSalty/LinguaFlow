package service

import (
	"context"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
)

type SegmentService struct {
	client   *ent.Client
	projects *ProjectService
}

type ResourceSegmentPage struct {
	Items      []*ent.Segment
	NextCursor int
}

type ResourceSegmentListOptions struct {
	AfterID int
	Limit   int
	Status  string
	Search  string
}

type ResourceSegmentUpdateInput struct {
	SourceText *string
	TargetText *string
	Comment    *string
}

func NewSegmentService(client *ent.Client, projects *ProjectService) *SegmentService {
	return &SegmentService{client: client, projects: projects}
}

func (s *SegmentService) ListResourceSegments(ctx context.Context, actorUserID, projectID, resourceID int, opts ResourceSegmentListOptions) (*ResourceSegmentPage, error) {
	if _, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, false); err != nil {
		return nil, err
	}
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	q := s.client.Segment.Query().Where(segment.ResourceIDEQ(resourceID))
	if opts.AfterID > 0 {
		q = q.Where(segment.IDGT(opts.AfterID))
	}
	if s := strings.TrimSpace(opts.Status); s != "" {
		q = q.Where(segment.StatusEQ(segment.Status(s)))
	}
	if search := strings.TrimSpace(opts.Search); search != "" {
		q = q.Where(segment.Or(segment.SourceTextContains(search), segment.TargetTextContains(search)))
	}
	rows, err := q.Order(ent.Asc(segment.FieldID)).Limit(opts.Limit + 1).WithReviewedBy().WithResource().All(ctx)
	if err != nil {
		return nil, err
	}
	page := &ResourceSegmentPage{Items: rows}
	if len(rows) > opts.Limit {
		page.NextCursor = rows[opts.Limit-1].ID
		page.Items = rows[:opts.Limit]
	}
	return page, nil
}

func (s *SegmentService) UpdateResourceSegment(ctx context.Context, actorUserID, projectID, resourceID, segmentID int, input ResourceSegmentUpdateInput) (*ent.Segment, error) {
	if _, err := s.requireResourceAccess(ctx, actorUserID, projectID, resourceID, true); err != nil {
		return nil, err
	}
	current, err := s.client.Segment.Query().Where(segment.IDEQ(segmentID), segment.ResourceIDEQ(resourceID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}

	update := s.client.Segment.UpdateOneID(current.ID)
	changed := false
	sourceChanged := false
	targetChanged := false

	if input.SourceText != nil {
		source := strings.TrimSpace(*input.SourceText)
		if source == "" {
			return nil, ErrInvalidInput
		}
		update.SetSourceText(source).ClearTargetText().SetStatus(SegmentStatusPending)
		changed = true
		sourceChanged = true
	}
	if input.TargetText != nil {
		target := strings.TrimSpace(*input.TargetText)
		if target == "" {
			return nil, ErrInvalidInput
		}
		update.SetTargetText(target).SetStatus(SegmentStatusEdited).SetReviewedByID(actorUserID)
		changed = true
		targetChanged = true
	}
	if input.Comment != nil {
		comment := strings.TrimSpace(*input.Comment)
		if comment == "" {
			update.ClearReviewComment()
		} else {
			update.SetReviewComment(comment)
		}
		changed = true
	}
	if !changed {
		return nil, ErrInvalidInput
	}
	if sourceChanged && !targetChanged {
		update.ClearReviewedBy()
	}

	updated, err := update.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}
	return s.client.Segment.Query().Where(segment.IDEQ(updated.ID)).WithReviewedBy().WithResource().Only(ctx)
}

func (s *SegmentService) requireResourceAccess(ctx context.Context, actorUserID, projectID, resourceID int, write bool) (*ent.Resource, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, write); err != nil {
		return nil, err
	}
	row, err := s.client.Resource.Query().Where(resource.IDEQ(resourceID), resource.ProjectIDEQ(projectID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrResourceNotFound
		}
		return nil, err
	}
	return row, nil
}
