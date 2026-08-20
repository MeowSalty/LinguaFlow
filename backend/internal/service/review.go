package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
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
	ErrIssueNotFound       = errors.New("quality issue not found")
)

// ReviewService 审核服务，通过 Resource 路径做权限校验。
type ReviewService struct {
	client   *ent.Client
	projects *ProjectService
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
		// 重译后译文将整体改变，旧 issues 指纹基本全变，清空合理；
		// re-translate 的语义即"从头来过"，旧裁决不跨文本存活。
		Where(segment.ResourceIDEQ(resourceID), segment.StatusEQ(SegmentStatusRejected)).
		SetStatus(SegmentStatusPending).
		ClearReviewedBy().
		ClearReviewComment().
		ClearQualityIssues().
		Exec(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

// SetIssueDisposition 对单条质量问题下裁决（可逆）。
//
// 裁决等价：LLM 与用户都是"给一条 issue 下结论"，本接口是用户侧入口。
// disposition=dismissed → 标记为非问题；disposition=pending → 撤销裁决改回未决。
// 通过 (code, matched_text) 定位单条 issue（与 qa.Fingerprint 一致）。
func (s *ReviewService) SetIssueDisposition(ctx context.Context, actorUserID, projectID, resourceID, segmentID int, code, matchedText, disposition, note string) (updated *ent.Segment, err error) {
	if _, err := s.authorizeSegment(ctx, actorUserID, projectID, resourceID, segmentID, true); err != nil {
		return nil, err
	}
	// 按 fingerprint 定位目标 issue（与 qa.Fingerprint 同一构造，避免漂移）
	targetFP := qa.Fingerprint(qa.QualityIssue{Code: code, Span: &qa.Span{MatchedText: matchedText}})

	// 事务内读改写：quality_issues 是整列 JSON，读后改写回会覆盖整列。
	// 事务串行化同行的并发写者（SQLite 单写事务；PostgreSQL 行锁），避免
	// 与 persistSemanticQASegmentIssues/persistDuplicateSourceDivergence 等
	// 并发写者之间 last-writer-wins 静默丢失裁决。TargetTextEQ 作为额外
	// 语义保护：若译文已被改写，旧指纹定位的 issue 不再适用，拒绝写入。
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("set issue disposition: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	row, err := tx.Segment.Query().Where(segment.IDEQ(segmentID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}
	issues := row.QualityIssues
	found := -1
	for i, iss := range issues {
		if qa.Fingerprint(iss) == targetFP {
			found = i
			break
		}
	}
	if found < 0 {
		return nil, ErrIssueNotFound
	}
	now := time.Now().UTC()
	switch disposition {
	case string(qa.DispositionDismissed):
		issues[found].Disposition = qa.DispositionDismissed
		issues[found].DecidedBy = &actorUserID
		issues[found].DecidedAt = &now
		issues[found].Note = note
	case string(qa.DispositionPending):
		// 撤销裁决：改回未决
		issues[found].Disposition = qa.DispositionPending
		issues[found].DecidedBy = nil
		issues[found].DecidedAt = nil
		issues[found].Note = ""
	default:
		return nil, ErrInvalidInput
	}
	update := tx.Segment.UpdateOneID(segmentID)
	if row.TargetText != nil && *row.TargetText != "" {
		update = update.Where(segment.TargetTextEQ(*row.TargetText))
	}
	if err = update.SetQualityIssues(issues).Exec(ctx); err != nil {
		// CAS 未命中（译文在事务内读取后被并发改写）或写入失败。
		// 裁决基于旧译文，让调用方重试（重新定位 issue）而非静默丢弃。
		if ent.IsNotFound(err) {
			return nil, ErrIssueNotFound
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("set issue disposition: commit transaction: %w", err)
	}
	// 返回带 ReviewedBy/Resource eager load 的完整 segment
	return s.client.Segment.Query().Where(segment.IDEQ(segmentID)).WithReviewedBy().WithResource().Only(ctx)
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
