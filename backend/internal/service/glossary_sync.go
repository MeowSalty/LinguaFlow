package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

// SyncTask 状态常量
const (
	SyncTaskStatusPending   = "pending"
	SyncTaskStatusRunning   = "running"
	SyncTaskStatusCompleted = "completed"
	SyncTaskStatusFailed    = "failed"
	SyncTaskStatusCancelled = "cancelled"
)

// 需要同步的段落状态
var syncableSegmentStatuses = []segment.Status{
	SegmentStatusTranslated,
	SegmentStatusEdited,
	SegmentStatusApproved,
	SegmentStatusRejected,
}

// 错误类型
var ErrNoAffectedSegments = fmt.Errorf("no affected segments found")

// GlossarySyncService 术语同步更新服务
type GlossarySyncService struct {
	client      *ent.Client
	glossarySvc *GlossaryService
	projects    *ProjectService
	auditSvc    *AuditService
	logger      *slog.Logger
}

// NewGlossarySyncService 创建新的 GlossarySyncService
func NewGlossarySyncService(
	client *ent.Client,
	glossarySvc *GlossaryService,
	projects *ProjectService,
	auditSvc *AuditService,
	logger *slog.Logger,
) *GlossarySyncService {
	return &GlossarySyncService{
		client:      client,
		glossarySvc: glossarySvc,
		projects:    projects,
		auditSvc:    auditSvc,
		logger:      logger,
	}
}

// --- 影响分析类型 ---

// GlossarySyncImpactInput 影响分析请求输入
type GlossarySyncImpactInput struct {
	OldTarget   string `json:"old_target"`
	NewTarget   string `json:"new_target,omitempty"`
	ResourceIDs []int  `json:"resource_ids,omitempty"`
}

// GlossarySyncImpactResult 影响分析结果
type GlossarySyncImpactResult struct {
	OldTarget     string                       `json:"old_target"`
	NewTarget     string                       `json:"new_target"`
	TotalAffected int                          `json:"total_affected"`
	Resources     []GlossarySyncImpactResource `json:"resources"`
}

// GlossarySyncImpactResource 单个资源的影响统计
type GlossarySyncImpactResource struct {
	ResourceID    int    `json:"resource_id"`
	ResourcePath  string `json:"resource_path"`
	AffectedCount int    `json:"affected_count"`
}

// --- 同步执行类型 ---

// GlossarySyncExecuteInput 同步执行请求输入
type GlossarySyncExecuteInput struct {
	OldTarget   string `json:"old_target"`
	NewTarget   string `json:"new_target"`
	ResourceIDs []int  `json:"resource_ids,omitempty"`
}

// SyncTaskInfo 异步任务信息
type SyncTaskInfo struct {
	TaskID    int    `json:"task_id"`
	Status    string `json:"status"`
	StatusURL string `json:"status_url"`
}

// GlossarySyncResult 同步执行结果
type GlossarySyncResult struct {
	TotalUpdated int                                        `json:"total_updated"`
	TotalSkipped int                                        `json:"total_skipped"`
	Resources    map[int]*GlossarySyncExecuteResourceResult `json:"-"`
}

// GlossarySyncExecuteResourceResult 单个资源的执行结果
type GlossarySyncExecuteResourceResult struct {
	ResourceID   int    `json:"resource_id"`
	ResourcePath string `json:"resource_path"`
	UpdatedCount int    `json:"updated_count"`
	SkippedCount int    `json:"skipped_count"`
}

// glossarySyncResultJSON 是用于 JSON 序列化的中间结构体，
// 解决 GlossarySyncResult.Resources 字段 json:"-" 导致的序列化丢失问题。
type glossarySyncResultJSON struct {
	TotalUpdated int                                  `json:"total_updated"`
	TotalSkipped int                                  `json:"total_skipped"`
	Resources    []*GlossarySyncExecuteResourceResult `json:"resources"`
}

// --- 影响分析 ---

// AnalyzeSyncImpact 分析术语修改对已翻译内容的影响
func (s *GlossarySyncService) AnalyzeSyncImpact(
	ctx context.Context,
	actorUserID, projectID, entryID int,
	input GlossarySyncImpactInput,
) (*GlossarySyncImpactResult, error) {
	// 1. 验证术语条目存在且属于该项目
	entry, err := s.glossarySvc.GetEntry(ctx, actorUserID, projectID, entryID)
	if err != nil {
		return nil, err
	}

	// 2. 两阶段匹配查询受影响的段落
	affected, err := s.findAffectedSegments(ctx, projectID, entry.Source, entry.CaseSensitive, input.OldTarget, input.ResourceIDs)
	if err != nil {
		return nil, fmt.Errorf("find affected segments: %w", err)
	}

	// 3. 按资源分组统计
	resourceMap := make(map[int]*GlossarySyncImpactResource)
	for _, seg := range affected {
		resID := *seg.ResourceID
		if _, ok := resourceMap[resID]; !ok {
			resourceMap[resID] = &GlossarySyncImpactResource{
				ResourceID: resID,
			}
		}
		resourceMap[resID].AffectedCount++
	}

	// 4. 获取资源路径
	resources := make([]GlossarySyncImpactResource, 0, len(resourceMap))
	for resID, stats := range resourceMap {
		res, err := s.client.Resource.Get(ctx, resID)
		if err != nil {
			s.logger.Warn("failed to get resource for path", "resource_id", resID, "error", err)
			stats.ResourcePath = fmt.Sprintf("resource_%d", resID)
		} else {
			stats.ResourcePath = res.Path
		}
		resources = append(resources, *stats)
	}

	return &GlossarySyncImpactResult{
		OldTarget:     input.OldTarget,
		NewTarget:     input.NewTarget,
		TotalAffected: len(affected),
		Resources:     resources,
	}, nil
}

// --- 两阶段匹配 ---

// findAffectedSegments 两阶段匹配查询受影响的段落
func (s *GlossarySyncService) findAffectedSegments(
	ctx context.Context,
	projectID int,
	source string,
	caseSensitive bool,
	oldTarget string,
	resourceIDs []int,
) ([]*ent.Segment, error) {

	// 基础查询条件
	predicates := []predicate.Segment{
		segment.HasResourceWith(
			resource.ProjectIDEQ(projectID),
		),
		segment.TargetTextNotNil(),
		segment.StatusIn(syncableSegmentStatuses...),
	}

	// 限定资源范围
	if len(resourceIDs) > 0 {
		predicates = append(predicates, segment.ResourceIDIn(resourceIDs...))
	}

	// 阶段1匹配：source_text 包含术语 source
	if caseSensitive {
		predicates = append(predicates, segment.SourceTextContains(source))
	} else {
		predicates = append(predicates, segment.SourceTextContainsFold(source))
	}

	// 阶段2匹配：target_text 包含 old_target
	if caseSensitive {
		predicates = append(predicates, segment.TargetTextContains(oldTarget))
	} else {
		predicates = append(predicates, segment.TargetTextContainsFold(oldTarget))
	}

	segments, err := s.client.Segment.Query().
		Where(predicates...).
		Order(ent.Asc(segment.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	return segments, nil
}

// --- 异步任务管理 ---

// SubmitSyncTask 创建异步同步任务
func (s *GlossarySyncService) SubmitSyncTask(
	ctx context.Context,
	actorUserID, projectID, entryID int,
	input GlossarySyncExecuteInput,
) (*SyncTaskInfo, error) {
	// 1. 验证术语条目存在
	entry, err := s.glossarySvc.GetEntry(ctx, actorUserID, projectID, entryID)
	if err != nil {
		return nil, err
	}

	// 2. 查询受影响的段落
	affected, err := s.findAffectedSegments(ctx, projectID, entry.Source, entry.CaseSensitive, input.OldTarget, input.ResourceIDs)
	if err != nil {
		return nil, fmt.Errorf("find affected segments: %w", err)
	}

	if len(affected) == 0 {
		return nil, ErrNoAffectedSegments
	}

	// 3. 序列化段落 ID 列表
	segmentIDs := make([]int, len(affected))
	for i, seg := range affected {
		segmentIDs[i] = seg.ID
	}
	segmentIDsJSON, err := json.Marshal(segmentIDs)
	if err != nil {
		return nil, fmt.Errorf("marshal segment IDs: %w", err)
	}

	// 4. 序列化资源 ID 列表
	resourceIDsJSON, err := json.Marshal(input.ResourceIDs)
	if err != nil {
		return nil, fmt.Errorf("marshal resource IDs: %w", err)
	}

	// 5. 创建 sync_task 记录
	task, err := s.client.SyncTask.Create().
		SetProjectID(projectID).
		SetEntryID(entryID).
		SetActorUserID(actorUserID).
		SetOldTarget(input.OldTarget).
		SetNewTarget(input.NewTarget).
		SetTotalSegments(len(affected)).
		SetProcessedSegments(0).
		SetStatus(SyncTaskStatusPending).
		SetSegmentIds(string(segmentIDsJSON)).
		SetResourceIds(string(resourceIDsJSON)).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create sync task: %w", err)
	}

	return &SyncTaskInfo{
		TaskID:    task.ID,
		Status:    SyncTaskStatusPending,
		StatusURL: fmt.Sprintf("/projects/%d/sync-tasks/%d", projectID, task.ID),
	}, nil
}

// GetSyncTaskStatus 查询同步任务状态
func (s *GlossarySyncService) GetSyncTaskStatus(
	ctx context.Context,
	actorUserID, projectID, taskID int,
) (*ent.SyncTask, error) {
	task, err := s.client.SyncTask.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}

	// 验证任务属于该项目
	if task.ProjectID != projectID {
		return nil, fmt.Errorf("sync task not found")
	}

	return task, nil
}

// CancelSyncTask 取消同步任务
func (s *GlossarySyncService) CancelSyncTask(
	ctx context.Context,
	actorUserID, projectID, taskID int,
) (*ent.SyncTask, error) {
	task, err := s.client.SyncTask.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}

	if task.ProjectID != projectID {
		return nil, fmt.Errorf("sync task not found")
	}

	// 已完成或已失败的任务不允许取消
	if task.Status == SyncTaskStatusCompleted || task.Status == SyncTaskStatusFailed {
		return nil, fmt.Errorf("cannot cancel task in %s status", task.Status)
	}

	// 已取消的任务直接返回
	if task.Status == SyncTaskStatusCancelled {
		return task, nil
	}

	// 标记为取消
	now := time.Now()
	updated, err := s.client.SyncTask.UpdateOneID(taskID).
		SetStatus(SyncTaskStatusCancelled).
		SetCancelledAt(now).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("cancel sync task: %w", err)
	}

	return updated, nil
}

// --- 任务执行 ---

// ExecuteSyncTask 由 SyncTaskRunner Worker 调用，实际执行替换逻辑
func (s *GlossarySyncService) ExecuteSyncTask(
	ctx context.Context,
	taskID int,
) error {
	// 1. 加载任务信息
	task, err := s.client.SyncTask.Get(ctx, taskID)
	if err != nil {
		return fmt.Errorf("load sync task: %w", err)
	}

	// 2. 检查是否已取消
	if task.Status == SyncTaskStatusCancelled {
		return nil
	}

	// 3. 更新状态为 running
	_, err = s.client.SyncTask.UpdateOneID(taskID).
		SetStatus(SyncTaskStatusRunning).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("update task status to running: %w", err)
	}

	// 4. 加载术语条目
	entry, err := s.client.GlossaryEntry.Query().
		Where(glossaryentry.IDEQ(task.EntryID), glossaryentry.ProjectIDEQ(task.ProjectID)).
		Only(ctx)
	if err != nil {
		return s.failTask(ctx, taskID, fmt.Errorf("load glossary entry: %w", err))
	}

	// 5. 获取项目的目标语言
	proj, err := s.client.Project.Get(ctx, task.ProjectID)
	if err != nil {
		return s.failTask(ctx, taskID, fmt.Errorf("load project: %w", err))
	}
	targetLang := proj.TargetLang

	// 6. 反序列化段落 ID 列表
	var segmentIDs []int
	if err := json.Unmarshal([]byte(task.SegmentIds), &segmentIDs); err != nil {
		return s.failTask(ctx, taskID, fmt.Errorf("unmarshal segment IDs: %w", err))
	}

	result := &GlossarySyncResult{
		Resources: make(map[int]*GlossarySyncExecuteResourceResult),
	}

	// 7. 分批处理，每批 100 个段落
	batchSize := 100
	for i := 0; i < len(segmentIDs); i += batchSize {
		// 每批开始前检查取消标志
		currentTask, err := s.client.SyncTask.Get(ctx, taskID)
		if err != nil {
			s.logger.Warn("failed to check cancel flag, continuing execution",
				"task_id", taskID, "error", err)
		}
		if currentTask != nil && currentTask.Status == SyncTaskStatusCancelled {
			s.logger.Info("sync task cancelled by user",
				"task_id", taskID,
				"processed", result.TotalUpdated+result.TotalSkipped,
				"remaining", len(segmentIDs)-i,
			)
			return nil
		}

		end := i + batchSize
		if end > len(segmentIDs) {
			end = len(segmentIDs)
		}
		batchIDs := segmentIDs[i:end]

		// 加载本批次段落
		batch, err := s.client.Segment.Query().
			Where(segment.IDIn(batchIDs...)).
			All(ctx)
		if err != nil {
			return s.failTask(ctx, taskID, err)
		}

		// 事务内处理本批次
		tx, err := s.client.Tx(ctx)
		if err != nil {
			return s.failTask(ctx, taskID, fmt.Errorf("begin transaction: %w", err))
		}

		for _, seg := range batch {
			// 二次验证：source_text 包含术语 source
			if !s.verifySourceInSegment(seg, entry) {
				result.TotalSkipped++
				s.logger.Debug("skipping segment: source not found in source_text",
					"segment_id", seg.ID,
					"term_source", entry.Source,
				)
				continue
			}

			targetText := *seg.TargetText

			// 执行替换
			newText, replaced, warn := glossary.SafeReplace(
				targetText,
				task.OldTarget,
				task.NewTarget,
				targetLang,
			)

			// case_sensitive 降级路径
			if !replaced && !entry.CaseSensitive {
				newText, replaced = glossary.CaseInsensitiveReplace(
					targetText,
					task.OldTarget,
					task.NewTarget,
				)
			}

			if warn != "" {
				s.logger.Warn("glossary sync replace warning",
					"segment_id", seg.ID, "warning", warn,
				)
			}

			if !replaced {
				result.TotalSkipped++
				continue
			}

			// 更新段落：设置新译文、状态改为 edited、清除审核信息
			_, err := tx.Segment.UpdateOneID(seg.ID).
				SetTargetText(newText).
				SetStatus(SegmentStatusEdited).
				ClearReviewedBy().
				ClearReviewComment().
				Save(ctx)
			if err != nil {
				_ = tx.Rollback()
				return s.failTask(ctx, taskID, fmt.Errorf("update segment %d: %w", seg.ID, err))
			}

			result.TotalUpdated++
			s.updateResourceStats(result, *seg.ResourceID, true)
		}

		// 更新进度
		_, err = tx.SyncTask.UpdateOneID(taskID).
			SetProcessedSegments(i + len(batchIDs)).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return s.failTask(ctx, taskID, fmt.Errorf("update progress: %w", err))
		}

		if err := tx.Commit(); err != nil {
			return s.failTask(ctx, taskID, fmt.Errorf("commit transaction: %w", err))
		}
	}

	// 8. 解析资源路径
	for resourceID, stats := range result.Resources {
		res, err := s.client.Resource.Get(ctx, resourceID)
		if err != nil {
			s.logger.Warn("failed to get resource for path",
				"resource_id", resourceID, "error", err)
			stats.ResourcePath = fmt.Sprintf("resource_%d", resourceID)
		} else {
			stats.ResourcePath = res.Path
		}
	}

	// 9. 任务完成
	resources := make([]*GlossarySyncExecuteResourceResult, 0, len(result.Resources))
	for _, r := range result.Resources {
		resources = append(resources, r)
	}
	resultForJSON := glossarySyncResultJSON{
		TotalUpdated: result.TotalUpdated,
		TotalSkipped: result.TotalSkipped,
		Resources:    resources,
	}
	resultJSON, err := json.Marshal(resultForJSON)
	if err != nil {
		s.logger.Warn("failed to marshal sync result", "error", err)
	}

	_, err = s.client.SyncTask.UpdateOneID(taskID).
		SetStatus(SyncTaskStatusCompleted).
		SetResult(string(resultJSON)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("mark task completed: %w", err)
	}

	// 10. 记录审计日志
	s.recordAuditLog(ctx, task, result)

	return nil
}

// --- 辅助方法 ---

// verifySourceInSegment 验证段落的 source_text 是否包含术语的 source
func (s *GlossarySyncService) verifySourceInSegment(seg *ent.Segment, entry *ent.GlossaryEntry) bool {
	source := entry.Source
	if entry.CaseSensitive {
		return strings.Contains(seg.SourceText, source)
	}
	return strings.Contains(strings.ToLower(seg.SourceText), strings.ToLower(source))
}

// failTask 标记任务失败
func (s *GlossarySyncService) failTask(ctx context.Context, taskID int, err error) error {
	s.logger.Error("sync task failed", "task_id", taskID, "error", err)
	_, updateErr := s.client.SyncTask.UpdateOneID(taskID).
		SetStatus(SyncTaskStatusFailed).
		SetError(err.Error()).
		Save(ctx)
	if updateErr != nil {
		s.logger.Error("failed to mark task as failed", "task_id", taskID, "error", updateErr)
	}
	return err
}

// updateResourceStats 更新资源级别的统计
func (s *GlossarySyncService) updateResourceStats(result *GlossarySyncResult, resourceID int, updated bool) {
	if _, ok := result.Resources[resourceID]; !ok {
		result.Resources[resourceID] = &GlossarySyncExecuteResourceResult{
			ResourceID: resourceID,
		}
	}
	if updated {
		result.Resources[resourceID].UpdatedCount++
	} else {
		result.Resources[resourceID].SkippedCount++
	}
}

// recordAuditLog 记录审计日志
func (s *GlossarySyncService) recordAuditLog(ctx context.Context, task *ent.SyncTask, result *GlossarySyncResult) {
	if s.auditSvc == nil {
		return
	}

	projectID := task.ProjectID
	err := s.auditSvc.Record(ctx, AuditEvent{
		ActorUserID:  task.ActorUserID,
		ProjectID:    &projectID,
		Action:       "glossary.sync_execute",
		ResourceType: "glossary_entry",
		ResourceID:   task.EntryID,
		Message:      fmt.Sprintf("术语同步更新完成：更新 %d 个段落，跳过 %d 个段落", result.TotalUpdated, result.TotalSkipped),
	})
	if err != nil {
		s.logger.Warn("failed to record audit log for glossary sync", "error", err)
	}
}

// --- JobController 接口实现 ---

// RecoverPendingJobs 实现 JobController 接口，查询并返回挂起的同步任务 ID 列表。
// 用于服务重启后恢复未完成的任务。
func (s *GlossarySyncService) RecoverPendingJobs(ctx context.Context) ([]int, error) {
	// 查询所有 pending 和 running 状态的任务
	tasks, err := s.client.SyncTask.Query().
		Where(synctask.StatusIn(SyncTaskStatusPending, SyncTaskStatusRunning)).
		Select(synctask.FieldID).
		Ints(ctx)
	if err != nil {
		return nil, fmt.Errorf("recover pending sync tasks: %w", err)
	}

	// 将 running 状态重置为 pending（避免重复执行）
	if err := s.resetRunningToPending(ctx); err != nil {
		return nil, err
	}

	return tasks, nil
}

// resetRunningToPending 将所有 running 状态的同步任务重置为 pending。
func (s *GlossarySyncService) resetRunningToPending(ctx context.Context) error {
	_, err := s.client.SyncTask.Update().
		Where(synctask.StatusEQ(SyncTaskStatusRunning)).
		SetStatus(SyncTaskStatusPending).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("reset running sync tasks to pending: %w", err)
	}
	return nil
}

// ReconcileJob 实现 JobController 接口。
// 对于同步任务，任务状态由 ExecuteSyncTask 内部管理，此处无需额外协调。
func (s *GlossarySyncService) ReconcileJob(_ context.Context, _ int) error {
	return nil
}

// --- 清理 ---

// CleanupExpiredTasks 将超时任务标记为 failed
func (s *GlossarySyncService) CleanupExpiredTasks(ctx context.Context) error {
	expired := time.Now().Add(-24 * time.Hour)
	count, err := s.client.SyncTask.Update().
		Where(
			synctask.StatusIn(SyncTaskStatusPending, SyncTaskStatusRunning),
			synctask.CreatedAtLT(expired),
		).
		SetStatus(SyncTaskStatusFailed).
		SetError("任务超时，自动标记为失败").
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("cleanup expired sync tasks: %w", err)
	}
	if count > 0 {
		s.logger.Info("cleaned up expired sync tasks", "count", count)
	}
	return nil
}
