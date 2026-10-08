package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
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
	lifecycle   *tasklife.Coordinator
	cancelTask  func(int)
}

func (s *GlossarySyncService) SetLifecycle(coordinator *tasklife.Coordinator) {
	s.lifecycle = coordinator
}

func (s *GlossarySyncService) SetTaskControl(cancel func(int)) { s.cancelTask = cancel }

// NewGlossarySyncService 创建新的 GlossarySyncService
func NewGlossarySyncService(
	client *ent.Client,
	glossarySvc *GlossaryService,
	projects *ProjectService,
	auditSvc *AuditService,
	logger *slog.Logger,
) *GlossarySyncService {
	if logger == nil {
		logger = slog.Default()
	}
	if projects == nil {
		projects = NewProjectService(client, NewUserService(client, nil))
	}
	if glossarySvc == nil {
		glossarySvc = NewGlossaryService(client, projects)
	}
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
	if actorUserID <= 0 || projectID <= 0 || entryID <= 0 || input.OldTarget == "" {
		return nil, ErrInvalidInput
	}
	// 1. 验证术语条目存在且属于该项目
	entry, err := s.glossarySvc.GetEntry(ctx, actorUserID, projectID, entryID)
	if err != nil {
		return nil, err
	}
	if err := validateSyncResources(ctx, s.client, projectID, input.ResourceIDs); err != nil {
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
	sort.Slice(resources, func(i, j int) bool { return resources[i].ResourceID < resources[j].ResourceID })

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
