package service

import (
	"context"
	"errors"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/translationjob"
)

const (
	SegmentStatusPending    = segment.StatusPending
	SegmentStatusTranslated = segment.StatusTranslated
	SegmentStatusEdited     = segment.StatusEdited
	SegmentStatusApproved   = segment.StatusApproved
	SegmentStatusRejected   = segment.StatusRejected
)

var (
	ErrSegmentNotFound     = errors.New("segment not found")
	ErrInvalidReviewState  = errors.New("invalid review state")
	ErrRetranslateNoReject = errors.New("no rejected segments to retranslate")
)

// ReviewService 审核服务，通过 Resource 路径做权限校验。
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

type BatchReviewInput struct {
	SegmentIDs []int
	Action     string // "approve" or "reject"
	Comment    string
}

type SegmentPage struct {
	Items      []*ent.Segment
	NextCursor int
}

func NewReviewService(client *ent.Client, projects *ProjectService) *ReviewService {
	return &ReviewService{client: client, projects: projects}
}

// EditSegment 编辑段落的译文。
func (s *ReviewService) EditSegment(ctx context.Context, actorUserID, projectID, resourceID, segmentID int, input SegmentEditInput) (*ent.Segment, error) {
	if strings.TrimSpace(input.TargetText) == "" {
		return nil, ErrInvalidInput
	}
	if _, err := s.authorizeSegment(ctx, actorUserID, projectID, resourceID, segmentID, true); err != nil {
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
	return s.client.Segment.Query().Where(segment.IDEQ(updated.ID)).WithReviewedBy().WithResource().Only(ctx)
}

// ApproveSegment 审批通过单个段落。
func (s *ReviewService) ApproveSegment(ctx context.Context, actorUserID, projectID, resourceID, segmentID int, input SegmentDecisionInput) (*ent.Segment, error) {
	if _, err := s.authorizeSegment(ctx, actorUserID, projectID, resourceID, segmentID, true); err != nil {
		return nil, err
	}
	current, err := s.client.Segment.Query().Where(segment.IDEQ(segmentID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}
	if current.Status != SegmentStatusTranslated && current.Status != SegmentStatusEdited && current.Status != SegmentStatusRejected {
		return nil, ErrInvalidReviewState
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
	return s.client.Segment.Query().Where(segment.IDEQ(updated.ID)).WithReviewedBy().WithResource().Only(ctx)
}

// RejectSegment 审批拒绝单个段落。
func (s *ReviewService) RejectSegment(ctx context.Context, actorUserID, projectID, resourceID, segmentID int, input SegmentDecisionInput) (*ent.Segment, error) {
	if _, err := s.authorizeSegment(ctx, actorUserID, projectID, resourceID, segmentID, true); err != nil {
		return nil, err
	}
	current, err := s.client.Segment.Query().Where(segment.IDEQ(segmentID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}
	if current.Status != SegmentStatusTranslated && current.Status != SegmentStatusEdited {
		return nil, ErrInvalidReviewState
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
	return s.client.Segment.Query().Where(segment.IDEQ(updated.ID)).WithReviewedBy().WithResource().Only(ctx)
}

// BatchReview 批量审核段落。
func (s *ReviewService) BatchReview(ctx context.Context, actorUserID, projectID, resourceID int, input BatchReviewInput) ([]*ent.Segment, error) {
	if len(input.SegmentIDs) == 0 {
		return nil, ErrInvalidInput
	}
	if input.Action != "approve" && input.Action != "reject" {
		return nil, ErrInvalidInput
	}
	// 验证权限
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, err
	}
	// 验证所有段落属于该资源
	rows, err := s.client.Segment.Query().
		Where(segment.IDIn(input.SegmentIDs...), segment.ResourceIDEQ(resourceID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) != len(input.SegmentIDs) {
		return nil, ErrSegmentNotFound
	}

	targetStatus := SegmentStatusApproved
	if input.Action == "reject" {
		targetStatus = SegmentStatusRejected
	}

	comment := strings.TrimSpace(input.Comment)
	for _, row := range rows {
		// 对于 approve：允许 translated, edited, rejected 状态
		// 对于 reject：允许 translated, edited 状态
		if input.Action == "approve" {
			if row.Status != SegmentStatusTranslated && row.Status != SegmentStatusEdited && row.Status != SegmentStatusRejected {
				continue
			}
		} else {
			if row.Status != SegmentStatusTranslated && row.Status != SegmentStatusEdited {
				continue
			}
		}
		update := s.client.Segment.UpdateOneID(row.ID).
			SetStatus(targetStatus).
			SetReviewedByID(actorUserID)
		if comment == "" {
			update.ClearReviewComment()
		} else {
			update.SetReviewComment(comment)
		}
		if _, err := update.Save(ctx); err != nil {
			return nil, err
		}
	}
	// 返回更新后的段落
	return s.client.Segment.Query().
		Where(segment.IDIn(input.SegmentIDs...)).
		WithReviewedBy().
		WithResource().
		All(ctx)
}

// ApproveAllResource 批准资源中所有已翻译/已编辑的段落。
func (s *ReviewService) ApproveAllResource(ctx context.Context, actorUserID, projectID, resourceID int) (int, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return 0, err
	}
	// 验证资源存在且属于项目
	if _, err := s.client.Resource.Query().Where(resource.IDEQ(resourceID), resource.ProjectIDEQ(projectID)).Only(ctx); err != nil {
		if ent.IsNotFound(err) {
			return 0, ErrResourceNotFound
		}
		return 0, err
	}
	count, err := s.client.Segment.Update().
		Where(
			segment.ResourceIDEQ(resourceID),
			segment.StatusIn(SegmentStatusTranslated, SegmentStatusEdited, SegmentStatusRejected),
		).
		SetStatus(SegmentStatusApproved).
		SetReviewedByID(actorUserID).
		ClearReviewComment().
		Save(ctx)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// RetranslateRejected 将资源中被拒绝的段落重置为 pending，以便重新翻译。
func (s *ReviewService) RetranslateRejected(ctx context.Context, actorUserID, projectID, resourceID int) (int, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return 0, err
	}
	// 验证资源存在且属于项目
	if _, err := s.client.Resource.Query().Where(resource.IDEQ(resourceID), resource.ProjectIDEQ(projectID)).Only(ctx); err != nil {
		if ent.IsNotFound(err) {
			return 0, ErrResourceNotFound
		}
		return 0, err
	}
	count, err := s.client.Segment.Query().
		Where(segment.ResourceIDEQ(resourceID), segment.StatusEQ(SegmentStatusRejected)).
		Count(ctx)
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, ErrRetranslateNoReject
	}
	if err := s.client.Segment.Update().
		Where(segment.ResourceIDEQ(resourceID), segment.StatusEQ(SegmentStatusRejected)).
		SetStatus(SegmentStatusPending).
		ClearReviewedBy().
		ClearReviewComment().
		Exec(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

// ApproveTranslationJob 批准翻译任务中所有已翻译/已编辑的段落。
func (s *ReviewService) ApproveTranslationJob(ctx context.Context, actorUserID, jobID int) (int, error) {
	// 加载翻译任务以获取项目 ID
	jobRow, err := s.client.TranslationJob.Query().
		Where(translationjob.IDEQ(jobID)).
		WithProject().
		WithJobResources(func(q *ent.JobResourceQuery) { q.WithResource() }).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return 0, ErrTranslationJobNotFound
		}
		return 0, err
	}
	projectRow, err := jobRow.Edges.ProjectOrErr()
	if err != nil {
		return 0, err
	}
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectRow.ID, true); err != nil {
		return 0, err
	}
	// 通过 JobResource 边关系获取关联的资源 ID
	resourceIDs := make([]int, 0, len(jobRow.Edges.JobResources))
	for _, jr := range jobRow.Edges.JobResources {
		if res, err := jr.Edges.ResourceOrErr(); err == nil {
			resourceIDs = append(resourceIDs, res.ID)
		}
	}
	if len(resourceIDs) == 0 {
		return 0, nil
	}
	count, err := s.client.Segment.Update().
		Where(
			segment.ResourceIDIn(resourceIDs...),
			segment.StatusIn(SegmentStatusTranslated, SegmentStatusEdited, SegmentStatusRejected),
		).
		SetStatus(SegmentStatusApproved).
		SetReviewedByID(actorUserID).
		ClearReviewComment().
		Save(ctx)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// authorizeSegment 通过 Segment → Resource → Project 路径校验访问权限。
func (s *ReviewService) authorizeSegment(ctx context.Context, actorUserID, projectID, resourceID, segmentID int, write bool) (*ent.Segment, error) {
	row, err := s.client.Segment.Query().
		Where(segment.IDEQ(segmentID), segment.ResourceIDEQ(resourceID)).
		WithResource().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}
	res, err := row.Edges.ResourceOrErr()
	if err != nil {
		return nil, err
	}
	if res.ID != resourceID {
		return nil, ErrSegmentNotFound
	}
	projectRow, err := s.client.Resource.Query().Where(resource.IDEQ(resourceID)).WithProject().Only(ctx)
	if err != nil {
		return nil, err
	}
	if projectRow.Edges.Project != nil && projectRow.Edges.Project.ID != projectID {
		return nil, ErrSegmentNotFound
	}
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, write); err != nil {
		return nil, err
	}
	return row, nil
}
