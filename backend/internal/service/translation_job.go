package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/translationjob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
)

const (
	TranslationJobStatusPending   = "pending"
	TranslationJobStatusRunning   = "running"
	TranslationJobStatusCompleted = "completed"
	TranslationJobStatusFailed    = "failed"
	TranslationJobStatusCancelled = "cancelled"

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

// TranslationJobService 翻译任务服务。
type TranslationJobService struct {
	client          *ent.Client
	projects        *ProjectService
	executionPlans  *ExecutionPlanService
	backends        *BackendService
	promptTemplates *PromptTemplateService
	profiles        *TranslationProfileService
	store           *filestore.LocalStore
	broker          *event.Broker
}

// CreateTranslationJobInput 创建翻译任务的输入参数。
type CreateTranslationJobInput struct {
	ResourceIDs      []int
	SegmentIDs       []int
	SegmentGroupKeys []string
	ExecutionPlanID  int
	AutoApprove      bool
}

// TranslationJobListOptions 任务列表查询选项。
type TranslationJobListOptions struct {
	Status      string
	TriggerType string
	AfterID     int
	Limit       int
}

// NewTranslationJobService 创建翻译任务服务。
func NewTranslationJobService(
	client *ent.Client,
	projects *ProjectService,
	executionPlans *ExecutionPlanService,
	backends *BackendService,
	promptTemplates *PromptTemplateService,
	profiles *TranslationProfileService,
	store *filestore.LocalStore,
	broker *event.Broker,
) *TranslationJobService {
	return &TranslationJobService{
		client:          client,
		projects:        projects,
		executionPlans:  executionPlans,
		backends:        backends,
		promptTemplates: promptTemplates,
		profiles:        profiles,
		store:           store,
		broker:          broker,
	}
}

// TranslationJobExecution 任务执行上下文。
type TranslationJobExecution struct {
	Job          *ent.TranslationJob
	Project      *ent.Project
	JobResources []*ent.JobResource
	ActorUserID  int
}

// --- 快照类型定义 ---

// JobExecutionSnapshot 任务执行快照，创建时生成，不可变。
type JobExecutionSnapshot struct {
	ExecutionPlanID   int                             `json:"execution_plan_id"`
	ExecutionPlanName string                          `json:"execution_plan_name"`
	Rounds            []JobRoundSnapshot              `json:"rounds"`
	SourceLang        string                          `json:"source_lang"`
	TargetLang        string                          `json:"target_lang"`
	GlossaryEnabled   bool                            `json:"glossary_enabled"`
	AutoApprove       bool                            `json:"auto_approve,omitempty"`
	Bootstrap         *ExecutionPlanBootstrapSnapshot `json:"bootstrap,omitempty"`
	RubyRetry         *ExecutionPlanRubyRetrySnapshot `json:"ruby_retry,omitempty"`
}

// ExecutionPlanBootstrapSnapshot 独立自举快照。
type ExecutionPlanBootstrapSnapshot struct {
	Enabled          bool            `json:"enabled"`
	Backend          BackendSnapshot `json:"backend"`
	TemplateContent  string          `json:"template_content"`
	BatchSize        int             `json:"batch_size"`
	Concurrency      int             `json:"concurrency"`
	MaxTermsPerBatch int             `json:"max_terms_per_batch"`
	MinSourceLen     int             `json:"min_source_len"`
}

// ExecutionPlanRubyRetrySnapshot 注音对齐重试快照。
type ExecutionPlanRubyRetrySnapshot struct {
	Enabled bool            `json:"enabled"`
	Backend BackendSnapshot `json:"backend"`
}

// JobRoundSnapshot 单轮的完整执行快照。
type JobRoundSnapshot struct {
	Name             string             `json:"name"`
	Backend          BackendSnapshot    `json:"backend"`
	Prompt           PromptSnapshot     `json:"prompt"`
	Strategy         StrategySnapshot   `json:"strategy"`
	BatchSize        int                `json:"batch_size"`
	MaxWordsPerBatch int                `json:"max_words_per_batch"`
	Concurrency      int                `json:"concurrency"`
	FallbackShrink   float64            `json:"fallback_shrink"`
	Retry            schema.RetryConfig `json:"retry"`
}

// BackendSnapshot 后端配置快照。
type BackendSnapshot struct {
	ID                 int            `json:"id"`
	Scope              string         `json:"scope"`
	Name               string         `json:"name"`
	Type               string         `json:"type"`
	Options            map[string]any `json:"options"`
	RateLimitPerMinute int            `json:"rate_limit_per_minute"`
}

// PromptSnapshot 提示词模板快照。
type PromptSnapshot struct {
	TemplateID       *int   `json:"template_id,omitempty"`
	TemplateName     string `json:"template_name"`
	Content          string `json:"content"`
	BootstrapContent string `json:"bootstrap_content,omitempty"`
}

// StrategySnapshot 策略模板快照。
type StrategySnapshot struct {
	ProfileID   *int                            `json:"profile_id,omitempty"`
	ProfileName string                          `json:"profile_name"`
	Split       schema.ProfileSplitConfig       `json:"split"`
	Protect     schema.ProfileProtectConfig     `json:"protect"`
	Postprocess schema.ProfilePostprocessConfig `json:"postprocess"`
	Repair      schema.ProfileRepairConfig      `json:"repair"`
	Glossary    schema.ProfileGlossaryConfig    `json:"glossary"`
	Context     schema.ProfileContextConfig     `json:"context"`
}

// --- CRUD 方法 ---

// CreateManualJob 创建手动翻译任务。
func (s *TranslationJobService) CreateManualJob(ctx context.Context, actorUserID, projectID int, input CreateTranslationJobInput) (*ent.TranslationJob, error) {
	// 1. 校验项目访问权限
	projectRow, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true)
	if err != nil {
		return nil, err
	}

	// 2. 加载执行计划模板（必填）
	plan, err := s.executionPlans.GetByID(ctx, actorUserID, input.ExecutionPlanID)
	if err != nil {
		return nil, fmt.Errorf("execution plan: %w", err)
	}

	// 3. 校验并生成快照
	snapshot, err := s.validateAndSnapshot(ctx, actorUserID, projectRow, plan)
	if err != nil {
		return nil, err
	}

	// 4. 填充通用配置
	snapshot.SourceLang = projectRow.SourceLang
	snapshot.TargetLang = projectRow.TargetLang
	snapshot.GlossaryEnabled = projectRow.GlossaryEnabled
	snapshot.AutoApprove = input.AutoApprove

	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot: %w", err)
	}

	// 5. 解析任务选择
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

	// 6. 事务创建任务
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

	var snapshotMap map[string]any
	if err := json.Unmarshal(snapshotBytes, &snapshotMap); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}

	created, err := tx.TranslationJob.Create().
		SetProjectID(projectID).
		SetCreatedByID(actorUserID).
		SetStatus(TranslationJobStatusPending).
		SetTriggerType(TranslationJobTriggerManual).
		SetExecutionPlanID(plan.ID).
		SetTranslationConfig(snapshotMap).
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

// --- 快照方法 ---

// validateAndSnapshot 校验执行计划中的每轮配置，并生成完整快照。
func (s *TranslationJobService) validateAndSnapshot(
	ctx context.Context,
	actorUserID int,
	projectRow *ent.Project,
	plan *ent.ExecutionPlanTemplate,
) (*JobExecutionSnapshot, error) {
	snapshot := &JobExecutionSnapshot{
		ExecutionPlanID:   plan.ID,
		ExecutionPlanName: plan.Name,
		Rounds:            make([]JobRoundSnapshot, 0, len(plan.Rounds)),
	}

	for i, round := range plan.Rounds {
		// 校验后端可访问性
		if err := s.validateBackendAccess(ctx, projectRow, round.BackendID); err != nil {
			return nil, fmt.Errorf("rounds[%d] backend: %w", i, err)
		}

		// 快照后端
		backendSnap, err := s.snapshotBackend(ctx, round.BackendID)
		if err != nil {
			return nil, fmt.Errorf("rounds[%d] snapshot backend: %w", i, err)
		}

		// 快照提示词模板
		promptSnap, err := s.snapshotPromptTemplate(ctx, round.PromptTemplateID)
		if err != nil {
			return nil, fmt.Errorf("rounds[%d] snapshot prompt: %w", i, err)
		}

		// 快照策略模板
		strategySnap, err := s.snapshotProfile(ctx, round.ProfileID)
		if err != nil {
			return nil, fmt.Errorf("rounds[%d] snapshot profile: %w", i, err)
		}

		// 校验翻译模板必填
		if promptSnap.Content == "" {
			return nil, fmt.Errorf("rounds[%d] prompt_template %q has no system_prompt_content (translation prompt is required)", i, promptSnap.TemplateName)
		}

		// 内联自举不再需要独立的 bootstrap 模板（inline 是翻译 prompt 的一部分）

		snapshot.Rounds = append(snapshot.Rounds, JobRoundSnapshot{
			Name:             round.Name,
			Backend:          *backendSnap,
			Prompt:           *promptSnap,
			Strategy:         *strategySnap,
			BatchSize:        round.BatchSize,
			MaxWordsPerBatch: round.MaxWordsPerBatch,
			Concurrency:      round.Concurrency,
			FallbackShrink:   round.FallbackShrink,
			Retry:            round.Retry,
		})
	}

	// 独立自举快照
	if plan.Bootstrap.Enabled {
		bs := &plan.Bootstrap

		// 校验自举后端可访问性
		if err := s.validateBackendAccess(ctx, projectRow, bs.BackendID); err != nil {
			return nil, fmt.Errorf("bootstrap backend: %w", err)
		}

		// 快照自举后端
		bootstrapBackendSnap, err := s.snapshotBackend(ctx, bs.BackendID)
		if err != nil {
			return nil, fmt.Errorf("bootstrap snapshot backend: %w", err)
		}

		// 快照自举提示词模板（仅用其 bootstrap_prompt_content）
		bootstrapPromptSnap, err := s.snapshotPromptTemplate(ctx, bs.PromptTemplateID)
		if err != nil {
			return nil, fmt.Errorf("bootstrap snapshot prompt: %w", err)
		}

		if bootstrapPromptSnap.BootstrapContent == "" {
			return nil, fmt.Errorf("bootstrap prompt_template %q has no bootstrap_prompt_content", bootstrapPromptSnap.TemplateName)
		}

		snapshot.GlossaryEnabled = true
		snapshot.Bootstrap = &ExecutionPlanBootstrapSnapshot{
			Enabled:          true,
			Backend:          *bootstrapBackendSnap,
			TemplateContent:  bootstrapPromptSnap.BootstrapContent,
			BatchSize:        bs.BatchSize,
			Concurrency:      bs.Concurrency,
			MaxTermsPerBatch: bs.MaxTermsPerBatch,
			MinSourceLen:     bs.MinSourceLen,
		}
	}

	// 注音对齐重试快照
	if plan.RubyRetry.Enabled && plan.RubyRetry.BackendID > 0 {
		rr := &plan.RubyRetry

		if err := s.validateBackendAccess(ctx, projectRow, rr.BackendID); err != nil {
			return nil, fmt.Errorf("ruby retry backend: %w", err)
		}

		rrBackendSnap, err := s.snapshotBackend(ctx, rr.BackendID)
		if err != nil {
			return nil, fmt.Errorf("ruby retry snapshot backend: %w", err)
		}

		snapshot.RubyRetry = &ExecutionPlanRubyRetrySnapshot{
			Enabled: true,
			Backend: *rrBackendSnap,
		}
	}

	return snapshot, nil
}

// validateBackendAccess 检查后端对项目是否可访问。
func (s *TranslationJobService) validateBackendAccess(
	ctx context.Context,
	projectRow *ent.Project,
	backendID int,
) error {
	b, err := s.backends.GetByID(ctx, backendID)
	if err != nil {
		return fmt.Errorf("backend %d: %w", backendID, err)
	}

	if projectRow.OwnerUserID != nil {
		if b.Scope == ScopeUser && b.OwnerUserID != nil && *b.OwnerUserID == *projectRow.OwnerUserID {
			return nil
		}
		if b.Scope == ScopeOrg && b.OwnerOrgID != nil && s.userBelongsToOrg(ctx, *projectRow.OwnerUserID, *b.OwnerOrgID) {
			return nil
		}
		return fmt.Errorf("backend %d is not accessible for this project", backendID)
	}

	if projectRow.OwnerOrgID != nil {
		if b.Scope == ScopeOrg && b.OwnerOrgID != nil && *b.OwnerOrgID == *projectRow.OwnerOrgID {
			return nil
		}
		return fmt.Errorf("backend %d is not accessible for this project", backendID)
	}

	return fmt.Errorf("project has no owner")
}

// userBelongsToOrg 检查用户是否属于指定组织。
func (s *TranslationJobService) userBelongsToOrg(ctx context.Context, userID, orgID int) bool {
	count, err := s.client.OrgMembership.Query().
		Where(
			orgmembership.HasOrganizationWith(organization.IDEQ(orgID)),
			orgmembership.HasUserWith(user.IDEQ(userID)),
		).
		Count(ctx)
	return err == nil && count > 0
}

// snapshotBackend 快照后端配置。
func (s *TranslationJobService) snapshotBackend(ctx context.Context, backendID int) (*BackendSnapshot, error) {
	b, err := s.client.Backend.Get(ctx, backendID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("backend %d not found", backendID)
		}
		return nil, err
	}
	return &BackendSnapshot{
		ID:                 b.ID,
		Scope:              b.Scope,
		Name:               b.Name,
		Type:               string(b.BackendType),
		Options:            cloneMap(b.Options),
		RateLimitPerMinute: b.RateLimitPerMinute,
	}, nil
}

// snapshotPromptTemplate 快照提示词模板。
func (s *TranslationJobService) snapshotPromptTemplate(ctx context.Context, templateID int) (*PromptSnapshot, error) {
	pt, err := s.promptTemplates.GetByID(ctx, templateID)
	if err != nil {
		return nil, err
	}
	id := pt.ID
	return &PromptSnapshot{
		TemplateID:       &id,
		TemplateName:     pt.Name,
		Content:          pt.SystemPromptContent,
		BootstrapContent: pt.BootstrapPromptContent,
	}, nil
}

// snapshotProfile 快照策略模板。
func (s *TranslationJobService) snapshotProfile(ctx context.Context, profileID int) (*StrategySnapshot, error) {
	tp, err := s.profiles.GetByID(ctx, profileID)
	if err != nil {
		return nil, err
	}
	tp.Config.NormalizeContext()
	tp.Config.NormalizePreserveKinds()
	id := tp.ID
	return &StrategySnapshot{
		ProfileID:   &id,
		ProfileName: tp.Name,
		Split:       tp.Config.Split,
		Protect:     tp.Config.Protect,
		Postprocess: tp.Config.Postprocess,
		Repair:      tp.Config.Repair,
		Glossary:    tp.Config.Glossary,
		Context:     tp.Config.Context,
	}, nil
}

// --- 其他方法 ---

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
	if err := s.client.TranslationJob.UpdateOneID(jobID).SetStatus(TranslationJobStatusRunning).Exec(ctx); err != nil {
		return err
	}
	s.publishEvent(jobID, "job_started", "info", "", "任务开始执行")
	return nil
}

// publishEvent publishes a lifecycle event to the Broker. No-op if broker is nil.
func (s *TranslationJobService) publishEvent(jobID int, eventType, level, stage, message string) {
	if s.broker == nil {
		return
	}
	s.broker.Publish(jobID, event.Event{
		Type:      eventType,
		JobID:     jobID,
		Level:     level,
		Stage:     stage,
		Message:   message,
		CreatedAt: time.Now(),
	})
}

// MarkJobStarted 记录任务开始时间。
func (s *TranslationJobService) MarkJobStarted(ctx context.Context, jobID int) error {
	now := time.Now()
	return s.client.TranslationJob.UpdateOneID(jobID).
		SetStartedAt(now).
		Exec(ctx)
}

// MarkJobResourceStarted 记录资源开始时间。
func (s *TranslationJobService) MarkJobResourceStarted(ctx context.Context, jobResourceID int) error {
	now := time.Now()
	return s.client.JobResource.UpdateOneID(jobResourceID).
		SetStartedAt(now).
		Exec(ctx)
}

func (s *TranslationJobService) MarkJobResourceRunning(ctx context.Context, jobID, jobResourceID int) error {
	if err := s.client.JobResource.UpdateOneID(jobResourceID).
		SetStatus(JobResourceStatusRunning).
		ClearErrorMessage().
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrJobResourceNotFound
		}
		return err
	}
	s.publishEvent(jobID, "resource_started", "info", "", "开始翻译资源")
	return nil
}

func (s *TranslationJobService) MarkJobResourceCompleted(ctx context.Context, jobID, jobResourceID int, outputPath string, completedSegments int) error {
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
	s.publishEvent(jobID, "resource_completed", "info", "", fmt.Sprintf("资源翻译完成 (%d 段)", completedSegments))
	return nil
}

func (s *TranslationJobService) MarkJobResourceFailed(ctx context.Context, jobID, jobResourceID int, failure error) error {
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
	s.publishEvent(jobID, "resource_failed", "error", "", fmt.Sprintf("资源翻译失败: %s", message))
	return nil
}

func (s *TranslationJobService) MarkJobResourceCancelled(ctx context.Context, jobID, jobResourceID int) error {
	if err := s.client.JobResource.UpdateOneID(jobResourceID).
		SetStatus(JobResourceStatusCancelled).
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrJobResourceNotFound
		}
		return err
	}
	s.publishEvent(jobID, "resource_cancelled", "info", "", "资源翻译取消")
	return nil
}

func (s *TranslationJobService) CancelJob(ctx context.Context, actorUserID, jobID int) (*ent.TranslationJob, error) {
	current, err := s.GetJob(ctx, actorUserID, jobID)
	if err != nil {
		return nil, err
	}
	if err := s.client.JobResource.Update().
		Where(jobresource.HasJobWith(translationjob.IDEQ(current.ID)), jobresource.StatusIn(JobResourceStatusPending, JobResourceStatusRunning)).
		SetStatus(JobResourceStatusCancelled).
		Exec(ctx); err != nil {
		return nil, err
	}
	if err := s.client.TranslationJob.UpdateOneID(current.ID).
		SetStatus(TranslationJobStatusCancelled).
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrTranslationJobNotFound
		}
		return nil, err
	}
	s.publishEvent(jobID, "job_cancelled", "info", "", "任务已取消")
	return s.GetJob(ctx, actorUserID, current.ID)
}

func (s *TranslationJobService) RetryJob(ctx context.Context, actorUserID, jobID int) (*ent.TranslationJob, error) {
	current, err := s.GetJob(ctx, actorUserID, jobID)
	if err != nil {
		return nil, err
	}
	if err := s.client.JobResource.Update().
		Where(jobresource.HasJobWith(translationjob.IDEQ(current.ID)), jobresource.StatusEQ(JobResourceStatusFailed)).
		SetStatus(JobResourceStatusPending).
		ClearErrorMessage().
		Exec(ctx); err != nil {
		return nil, err
	}
	if err := s.client.TranslationJob.UpdateOneID(current.ID).
		SetStatus(TranslationJobStatusPending).
		SetFailedResources(0).
		ClearErrorMessage().
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrTranslationJobNotFound
		}
		return nil, err
	}
	return s.GetJob(ctx, actorUserID, current.ID)
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
	// [DEBUG] 诊断：记录每个资源的状态
	for _, item := range current.Edges.JobResources {
		completedSegments += item.CompletedSegments
		slog.Info("[DEBUG] ReconcileJob resource status",
			"job_id", jobID,
			"resource_id", item.ID,
			"status", item.Status,
			"segment_count", item.SegmentCount,
			"completed_segments", item.CompletedSegments,
		)
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
	// [DEBUG] 诊断：记录汇总信息
	total := len(current.Edges.JobResources)
	slog.Info("[DEBUG] ReconcileJob summary",
		"job_id", jobID,
		"total_resources", total,
		"pending", pendingCount,
		"running", runningCount,
		"completed", completed,
		"failed", failed,
		"cancelled", cancelled,
		"completed_segments", completedSegments,
	)
	status := deriveTranslationJobStatus(len(current.Edges.JobResources), pendingCount, runningCount, completed, failed, cancelled)
	// [DEBUG] 诊断：记录最终决定的作业状态
	slog.Info("[DEBUG] ReconcileJob derived status",
		"job_id", jobID,
		"derived_status", status,
		"completed_resources", completed,
		"total_resources", len(current.Edges.JobResources),
	)
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
	// 所有资源结束后，用各资源的 stage_total 之和修正 total_segments。
	// stage_total 是 pipeline 运行时确定的实际翻译量（排除了空白段、装饰分隔符等），
	// 而创建时的 total_segments 统计的是所有选中的 segment IDs（含被跳过的）。
	if pendingCount == 0 && runningCount == 0 {
		actualTotal := 0
		for _, item := range current.Edges.JobResources {
			actualTotal += item.StageTotal
		}
		if actualTotal > 0 {
			update.SetTotalSegments(actualTotal)
		}
	}
	if err := update.Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrTranslationJobNotFound
		}
		return err
	}

	// Publish lifecycle events based on derived status.
	switch status {
	case TranslationJobStatusCompleted:
		s.publishEvent(jobID, "job_completed", "info", "", "任务完成")
	case TranslationJobStatusFailed:
		errMsg := "任务失败"
		if firstFailure != nil {
			errMsg = *firstFailure
		}
		s.publishEvent(jobID, "job_failed", "error", "", errMsg)
	case TranslationJobStatusCancelled:
		s.publishEvent(jobID, "job_cancelled", "info", "", "任务已取消")
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
		return TranslationJobStatusCompleted
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
	return q.Order(ent.Asc(translationjob.FieldID)).Limit(opts.Limit).All(ctx)
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
	if len(input.SegmentGroupKeys) > 0 {
		return s.resolveGroupKeySelection(ctx, projectID, input.SegmentGroupKeys, input.ResourceIDs)
	}
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

func (s *TranslationJobService) resolveGroupKeySelection(ctx context.Context, projectID int, groupKeys []string, resourceIDs []int) (map[int][]int, error) {
	uniqueKeys := make(map[string]struct{}, len(groupKeys))
	for _, key := range groupKeys {
		k := strings.TrimSpace(key)
		if k != "" {
			uniqueKeys[k] = struct{}{}
		}
	}
	if len(uniqueKeys) == 0 {
		return nil, fmt.Errorf("segment_group_keys 不能为空")
	}

	// 查询该项目下指定资源的 segments（带 meta 字段）
	segQuery := s.client.Segment.Query().
		Where(segment.HasResourceWith(resource.ProjectIDEQ(projectID)))
	if len(resourceIDs) > 0 {
		segQuery = segQuery.Where(segment.HasResourceWith(resource.IDIn(uniqueInts(resourceIDs)...)))
	}
	rows, err := segQuery.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询 segments 失败: %w", err)
	}

	selection := make(map[int][]int)
	matchedCount := 0
	for _, row := range rows {
		if row.Meta == nil || row.ResourceID == nil {
			continue
		}
		var meta map[string]any
		if err := json.Unmarshal([]byte(*row.Meta), &meta); err != nil {
			continue
		}
		epubFile, ok := meta["epub_file"].(string)
		if !ok {
			continue
		}
		if _, matched := uniqueKeys[epubFile]; matched {
			selection[*row.ResourceID] = append(selection[*row.ResourceID], row.ID)
			matchedCount++
			slog.Debug("[resolveGroupKeySelection] resource matched",
				"resource_id", *row.ResourceID,
				"segment_count", len(selection[*row.ResourceID]),
				"segment_ids", selection[*row.ResourceID])
		}
	}

	slog.Debug("[resolveGroupKeySelection] diagnostic",
		"project_id", projectID,
		"group_keys", groupKeys,
		"total_segments_in_project", len(rows),
		"matched_segments", matchedCount,
		"matched_resources", len(selection))

	if matchedCount == 0 {
		return nil, fmt.Errorf("未找到匹配指定章节的 segments")
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

func defaultProjectTranslationConfig(projectRow *ent.Project) map[string]any {
	base := map[string]any{}
	if projectRow == nil {
		return base
	}
	if sourceLang := strings.TrimSpace(projectRow.SourceLang); sourceLang != "" {
		base["source_lang"] = sourceLang
	}
	if targetLang := strings.TrimSpace(projectRow.TargetLang); targetLang != "" {
		base["target_lang"] = targetLang
	}
	return mergeConfigMaps(base, projectRow.DefaultTranslationConfig)
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

// GetSnapshot 从 TranslationJob 的 TranslationConfig 字段解析快照。
func GetSnapshot(job *ent.TranslationJob) (*JobExecutionSnapshot, error) {
	if job.TranslationConfig == nil {
		return nil, nil
	}
	raw, err := json.Marshal(job.TranslationConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal translation config: %w", err)
	}
	var snap JobExecutionSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	return &snap, nil
}

// GetTranslationSnapshot 从翻译任务获取执行快照。
func (s *TranslationJobService) GetTranslationSnapshot(ctx context.Context, jobID int) (*JobExecutionSnapshot, error) {
	job, err := s.client.TranslationJob.Get(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("load job: %w", err)
	}
	return GetSnapshot(job)
}

// CheckJobAccess 校验用户是否有权访问翻译任务。
func (s *TranslationJobService) CheckJobAccess(ctx context.Context, userID, jobID int) error {
	job, err := s.client.TranslationJob.Query().
		Where(translationjob.IDEQ(jobID)).
		WithProject().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrTranslationJobNotFound
		}
		return fmt.Errorf("load job: %w", err)
	}
	// 通过项目权限校验：用户必须是任务所属项目的访问者
	projectRow, err := job.Edges.ProjectOrErr()
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	if _, err := s.projects.requireProjectAccess(ctx, userID, projectRow.ID, false); err != nil {
		return fmt.Errorf("access denied: %w", err)
	}
	return nil
}
