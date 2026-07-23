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
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
)

const (
	JobStatusPending   = "pending"
	JobStatusRunning   = "running"
	JobStatusCompleted = "completed"
	JobStatusFailed    = "failed"
	JobStatusCancelled = "cancelled"

	JobTriggerManual = "manual"

	JobResourceStatusPending   = "pending"
	JobResourceStatusRunning   = "running"
	JobResourceStatusCompleted = "completed"
	JobResourceStatusFailed    = "failed"
	JobResourceStatusCancelled = "cancelled"
)

var (
	ErrJobNotFound         = errors.New("job not found")
	ErrJobEmpty            = errors.New("job has no pending segments")
	ErrJobResourceNotFound = errors.New("job resource not found")
	ErrJobActorMissing     = errors.New("job actor unavailable")
)

// JobService 任务服务。
type JobService struct {
	client                     *ent.Client
	projects                   *ProjectService
	executionPlans             *ExecutionPlanService
	backends                   *BackendService
	translationPromptTemplates *TranslationPromptTemplateService
	bootstrapPromptTemplates   *BootstrapPromptTemplateService
	profiles                   *ExecutionProfileService
	store                      *filestore.LocalStore
	broker                     *event.Broker
}

// CreateJobInput 创建任务的输入参数。
type CreateJobInput struct {
	ResourceIDs      []int
	SegmentIDs       []int
	SegmentGroupKeys []string
	SegmentFilter    string // 覆盖模板中的段落过滤策略；空值表示使用模板默认
	ExecutionPlanID  int
	AutoApprove      bool
}

// JobListOptions 任务列表查询选项。
type JobListOptions struct {
	Status      string
	TriggerType string
	AfterID     int
	Limit       int
}

// NewJobService 创建任务服务。
func NewJobService(
	client *ent.Client,
	projects *ProjectService,
	executionPlans *ExecutionPlanService,
	backends *BackendService,
	translationPromptTemplates *TranslationPromptTemplateService,
	bootstrapPromptTemplates *BootstrapPromptTemplateService,
	profiles *ExecutionProfileService,
	store *filestore.LocalStore,
	broker *event.Broker,
) *JobService {
	return &JobService{
		client:                     client,
		projects:                   projects,
		executionPlans:             executionPlans,
		backends:                   backends,
		translationPromptTemplates: translationPromptTemplates,
		bootstrapPromptTemplates:   bootstrapPromptTemplates,
		profiles:                   profiles,
		store:                      store,
		broker:                     broker,
	}
}

// JobExecution 任务执行上下文。
type JobExecution struct {
	Job          *ent.Job
	Project      *ent.Project
	JobResources []*ent.JobResource
	ActorUserID  int
}

// --- 快照类型定义 ---

// JobExecutionSnapshot 任务执行快照，创建时生成，不可变。
type JobExecutionSnapshot struct {
	ExecutionPlanID          int                             `json:"execution_plan_id"`
	ExecutionPlanName        string                          `json:"execution_plan_name"`
	Rounds                   []JobRoundSnapshot              `json:"rounds"`
	SourceLang               string                          `json:"source_lang"`
	TargetLang               string                          `json:"target_lang"`
	GlossaryEnabled          bool                            `json:"glossary_enabled"`
	TMEnabled                bool                            `json:"tm_enabled,omitempty"`
	AutoApprove              bool                            `json:"auto_approve,omitempty"`
	ExplicitSegmentSelection bool                            `json:"explicit_segment_selection,omitempty"`
	RubyRetry                *ExecutionPlanRubyRetrySnapshot `json:"ruby_retry,omitempty"`
}

// ExecutionPlanRubyRetrySnapshot 注音对齐重试快照。
type ExecutionPlanRubyRetrySnapshot struct {
	Enabled bool            `json:"enabled"`
	Backend BackendSnapshot `json:"backend"`
}

// JobRoundSnapshot 单轮的完整执行快照。
type JobRoundSnapshot struct {
	Mode       string                      `json:"mode"` // "translate" | "extract" | "adjudicate"
	Backend    BackendSnapshot             `json:"backend"`
	Translate  *JobTranslateRoundSnapshot  `json:"translate,omitempty"`
	Extract    *JobExtractRoundSnapshot    `json:"extract,omitempty"`
	Adjudicate *JobAdjudicateRoundSnapshot `json:"adjudicate,omitempty"`
}

// JobTranslateRoundSnapshot 翻译轮次快照。
type JobTranslateRoundSnapshot struct {
	Prompt           PromptSnapshot         `json:"prompt"`
	Strategy         StrategySnapshot       `json:"strategy"`
	BatchSize        int                    `json:"batch_size"`
	MaxWordsPerBatch int                    `json:"max_words_per_batch"`
	Concurrency      int                    `json:"concurrency"`
	FallbackShrink   float64                `json:"fallback_shrink"`
	SegmentFilter    *SegmentFilterSnapshot `json:"segment_filter,omitempty"`
	Retry            schema.RetryConfig     `json:"retry"`
}

// JobExtractRoundSnapshot 术语抽取轮次快照。
type JobExtractRoundSnapshot struct {
	TemplateContent      string             `json:"template_content"` // 从 BootstrapPromptTemplate.Content 快照
	BatchSize            int                `json:"batch_size"`
	MaxWordsPerBatch     int                `json:"max_words_per_batch"`
	Concurrency          int                `json:"concurrency"`
	MaxTermsPer1000Chars float64            `json:"max_terms_per_1000_chars"`
	MinSourceLen         int                `json:"min_source_len"`
	Retry                schema.RetryConfig `json:"retry"`
}

// JobAdjudicateRoundSnapshot 质量裁决轮次快照（无 prompt 字段，内置不可见）。
type JobAdjudicateRoundSnapshot struct {
	BatchSize        int                `json:"batch_size"`
	MaxWordsPerBatch int                `json:"max_words_per_batch"`
	Concurrency      int                `json:"concurrency"`
	AdjudicateCodes  []string           `json:"adjudicate_codes,omitempty"`
	Retry            schema.RetryConfig `json:"retry"`
}

// SegmentFilterSnapshot 翻译轮次段落过滤快照。
type SegmentFilterSnapshot struct {
	StatusFilter string `json:"status_filter"`        // "pending_only" | "skip_approved" | "all"
	Overridden   bool   `json:"overridden,omitempty"` // true 表示由任务创建时显式覆盖
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

// PromptSnapshot 翻译提示词模板快照。
type PromptSnapshot struct {
	TemplateID   *int   `json:"template_id,omitempty"`
	TemplateName string `json:"template_name"`
	Content      string `json:"content"`
}

// BootstrapPromptSnapshot 术语抽取提示词模板快照。
type BootstrapPromptSnapshot struct {
	TemplateID   *int   `json:"template_id,omitempty"`
	TemplateName string `json:"template_name"`
	Content      string `json:"content"`
}

// StrategySnapshot 策略模板快照。
type StrategySnapshot struct {
	ProfileID   *int                            `json:"profile_id,omitempty"`
	ProfileName string                          `json:"profile_name"`
	Protect     schema.ProfileProtectConfig     `json:"protect"`
	Postprocess schema.ProfilePostprocessConfig `json:"postprocess"`
	Repair      schema.ProfileRepairConfig      `json:"repair"`
	Glossary    schema.ProfileGlossaryConfig    `json:"glossary"`
	Context     schema.ProfileContextConfig     `json:"context"`
	Ruby        schema.ProfileRubyConfig        `json:"ruby"`
	QA          schema.ProfileQAConfig          `json:"qa"`
}

// --- CRUD 方法 ---

// CreateManualJob 创建手动翻译任务。
func (s *JobService) CreateManualJob(ctx context.Context, actorUserID, projectID int, input CreateJobInput) (*ent.Job, error) {
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
	snapshot, err := s.validateAndSnapshot(ctx, actorUserID, projectRow, plan, input.SegmentFilter)
	if err != nil {
		return nil, err
	}

	// 4. 填充通用配置
	snapshot.SourceLang = projectRow.SourceLang
	snapshot.TargetLang = projectRow.TargetLang
	snapshot.GlossaryEnabled = jobGlossaryEnabled(projectRow.GlossaryEnabled, snapshot.Rounds)
	snapshot.AutoApprove = input.AutoApprove
	snapshot.ExplicitSegmentSelection = len(input.SegmentGroupKeys) == 0 && len(input.SegmentIDs) > 0

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
		return nil, ErrJobEmpty
	}

	resourceIDs := make([]int, 0, len(selection))
	totalSegments := 0
	for resourceID, segmentIDs := range selection {
		resourceIDs = append(resourceIDs, resourceID)
		totalSegments += len(segmentIDs)
	}
	sort.Ints(resourceIDs)

	// 7. 事务创建任务
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

	created, err := tx.Job.Create().
		SetProjectID(projectID).
		SetCreatedByID(actorUserID).
		SetStatus(JobStatusPending).
		SetTriggerType(JobTriggerManual).
		SetExecutionPlanID(plan.ID).
		SetExecutionConfig(snapshotMap).
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
func (s *JobService) validateAndSnapshot(
	ctx context.Context,
	actorUserID int,
	projectRow *ent.Project,
	plan *ent.ExecutionPlanTemplate,
	overrideSegmentFilter string,
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

		switch round.Mode {
		case "translate":
			if round.Translate == nil {
				return nil, fmt.Errorf("rounds[%d] translate config is nil", i)
			}
			t := round.Translate

			// 快照提示词模板
			promptSnap, err := s.snapshotPromptTemplate(ctx, t.PromptTemplateID)
			if err != nil {
				return nil, fmt.Errorf("rounds[%d] snapshot prompt: %w", i, err)
			}

			// 快照策略模板
			strategySnap, err := s.snapshotProfile(ctx, t.ProfileID)
			if err != nil {
				return nil, fmt.Errorf("rounds[%d] snapshot profile: %w", i, err)
			}

			// 校验翻译模板必填
			if promptSnap.Content == "" {
				return nil, fmt.Errorf("rounds[%d] prompt_template %q has no system_prompt_content (translation prompt is required)", i, promptSnap.TemplateName)
			}

			snapshot.Rounds = append(snapshot.Rounds, JobRoundSnapshot{
				Mode:    "translate",
				Backend: *backendSnap,
				Translate: &JobTranslateRoundSnapshot{
					Prompt:           *promptSnap,
					Strategy:         *strategySnap,
					BatchSize:        t.BatchSize,
					MaxWordsPerBatch: t.MaxWordsPerBatch,
					Concurrency:      t.Concurrency,
					FallbackShrink:   t.FallbackShrink,
					SegmentFilter:    snapshotSegmentFilter(t.SegmentFilter, overrideSegmentFilter),
					Retry:            t.Retry,
				},
			})

		case "extract":
			if round.Extract == nil {
				return nil, fmt.Errorf("rounds[%d] extract config is nil", i)
			}
			e := round.Extract

			// 快照自举提示词模板
			bootstrapSnap, err := s.snapshotBootstrapTemplate(ctx, e.BootstrapTemplateID)
			if err != nil {
				return nil, fmt.Errorf("rounds[%d] snapshot bootstrap template: %w", i, err)
			}

			if bootstrapSnap.Content == "" {
				return nil, fmt.Errorf("rounds[%d] bootstrap_template %q has no content", i, bootstrapSnap.TemplateName)
			}

			snapshot.Rounds = append(snapshot.Rounds, JobRoundSnapshot{
				Mode:    "extract",
				Backend: *backendSnap,
				Extract: &JobExtractRoundSnapshot{
					TemplateContent:      bootstrapSnap.Content,
					BatchSize:            e.BatchSize,
					MaxWordsPerBatch:     e.MaxWordsPerBatch,
					Concurrency:          e.Concurrency,
					MaxTermsPer1000Chars: e.MaxTermsPer1000Chars,
					MinSourceLen:         e.MinSourceLen,
					Retry:                e.Retry,
				},
			})

		case "adjudicate":
			if round.Adjudicate == nil {
				return nil, fmt.Errorf("rounds[%d] adjudicate config is nil", i)
			}
			a := round.Adjudicate
			codes := a.AdjudicateCodes
			if len(codes) == 0 {
				codes = []string{"source_residual"}
			}
			snapshot.Rounds = append(snapshot.Rounds, JobRoundSnapshot{
				Mode:    "adjudicate",
				Backend: *backendSnap,
				Adjudicate: &JobAdjudicateRoundSnapshot{
					BatchSize:        a.BatchSize,
					MaxWordsPerBatch: a.MaxWordsPerBatch,
					Concurrency:      a.Concurrency,
					AdjudicateCodes:  codes,
					Retry:            a.Retry,
				},
			})

		default:
			return nil, fmt.Errorf("rounds[%d] unsupported mode: %s", i, round.Mode)
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

func jobGlossaryEnabled(projectEnabled bool, rounds []JobRoundSnapshot) bool {
	if projectEnabled {
		return true
	}
	for _, round := range rounds {
		if round.Mode == "extract" {
			return true
		}
	}
	return false
}

// validateBackendAccess 检查后端对项目是否可访问。
func (s *JobService) validateBackendAccess(
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
func (s *JobService) userBelongsToOrg(ctx context.Context, userID, orgID int) bool {
	count, err := s.client.OrgMembership.Query().
		Where(
			orgmembership.HasOrganizationWith(organization.IDEQ(orgID)),
			orgmembership.HasUserWith(user.IDEQ(userID)),
		).
		Count(ctx)
	return err == nil && count > 0
}

// snapshotBackend 快照后端配置。
func (s *JobService) snapshotBackend(ctx context.Context, backendID int) (*BackendSnapshot, error) {
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

// snapshotPromptTemplate 快照翻译提示词模板。
func (s *JobService) snapshotPromptTemplate(ctx context.Context, templateID int) (*PromptSnapshot, error) {
	pt, err := s.translationPromptTemplates.GetByID(ctx, templateID)
	if err != nil {
		return nil, err
	}
	id := pt.ID
	return &PromptSnapshot{
		TemplateID:   &id,
		TemplateName: pt.Name,
		Content:      pt.SystemPromptContent,
	}, nil
}

// snapshotBootstrapTemplate 快照术语抽取提示词模板。
func (s *JobService) snapshotBootstrapTemplate(ctx context.Context, templateID int) (*BootstrapPromptSnapshot, error) {
	pt, err := s.bootstrapPromptTemplates.GetByID(ctx, templateID)
	if err != nil {
		return nil, err
	}
	id := pt.ID
	return &BootstrapPromptSnapshot{
		TemplateID:   &id,
		TemplateName: pt.Name,
		Content:      pt.Content,
	}, nil
}

// snapshotProfile 快照策略模板。
func (s *JobService) snapshotProfile(ctx context.Context, profileID int) (*StrategySnapshot, error) {
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
		Protect:     tp.Config.Protect,
		Postprocess: tp.Config.Postprocess,
		Repair:      tp.Config.Repair,
		Glossary:    tp.Config.Glossary,
		Context:     tp.Config.Context,
		Ruby:        tp.Config.Ruby,
		QA:          tp.Config.QA,
	}, nil
}

// snapshotSegmentFilter 将轮次配置中的 SegmentFilter 转换为快照。
// override 非空时覆盖模板值；未配置时默认 pending_only。
func snapshotSegmentFilter(cfg *schema.TranslateSegmentFilterConfig, override string) *SegmentFilterSnapshot {
	sf := "pending_only"
	if cfg != nil && cfg.StatusFilter != "" {
		sf = cfg.StatusFilter
	}
	overridden := false
	if override != "" {
		sf = override
		overridden = true
	}
	switch sf {
	case "pending_only", "skip_approved", "all":
	default:
		slog.Warn("invalid status_filter value, falling back to pending_only",
			"value", sf,
		)
		sf = "pending_only"
	}
	return &SegmentFilterSnapshot{StatusFilter: sf, Overridden: overridden}
}

// --- 其他方法 ---

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
			if err := s.client.Job.UpdateOneID(current.ID).SetStatus(JobStatusPending).Exec(ctx); err != nil {
				return nil, err
			}
		}
		if err := s.client.JobResource.Update().
			Where(jobresource.HasJobWith(job.IDEQ(current.ID)), jobresource.StatusEQ(JobResourceStatusRunning)).
			SetStatus(JobResourceStatusPending).
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
		WithJobResources(func(q *ent.JobResourceQuery) {
			q.WithResource().Order(ent.Asc(jobresource.FieldID))
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
		return nil, ErrJobActorMissing
	}
	return &JobExecution{Job: current, Project: projectRow, JobResources: current.Edges.JobResources, ActorUserID: actorUserID}, nil
}

func (s *JobService) MarkJobRunning(ctx context.Context, jobID int) error {
	if err := s.client.Job.UpdateOneID(jobID).SetStatus(JobStatusRunning).Exec(ctx); err != nil {
		return err
	}
	s.publishEvent(jobID, "job_started", "info", "", "任务开始执行")
	return nil
}

// publishEvent publishes a lifecycle event to the Broker. No-op if broker is nil.
func (s *JobService) publishEvent(jobID int, eventType, level, stage, message string) {
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
func (s *JobService) MarkJobStarted(ctx context.Context, jobID int) error {
	now := time.Now()
	return s.client.Job.UpdateOneID(jobID).
		SetStartedAt(now).
		Exec(ctx)
}

// MarkJobResourceStarted 记录资源开始时间。
func (s *JobService) MarkJobResourceStarted(ctx context.Context, jobResourceID int) error {
	now := time.Now()
	return s.client.JobResource.UpdateOneID(jobResourceID).
		SetStartedAt(now).
		Exec(ctx)
}

func (s *JobService) MarkJobResourceRunning(ctx context.Context, jobID, jobResourceID int) error {
	if err := s.client.JobResource.UpdateOneID(jobResourceID).
		SetStatus(JobResourceStatusRunning).
		ClearErrorMessage().
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrJobResourceNotFound
		}
		return err
	}
	s.publishEvent(jobID, "resource_started", "info", "", "开始处理资源")
	return nil
}

func (s *JobService) MarkJobResourceCompleted(ctx context.Context, jobID, jobResourceID int, outputPath string, completedSegments, skippedSegments int) error {
	if err := s.client.JobResource.UpdateOneID(jobResourceID).
		SetStatus(JobResourceStatusCompleted).
		SetOutputPath(strings.TrimSpace(outputPath)).
		SetCompletedSegments(completedSegments).
		SetSkippedSegments(skippedSegments).
		ClearErrorMessage().
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrJobResourceNotFound
		}
		return err
	}
	s.publishEvent(jobID, "resource_completed", "info", "", fmt.Sprintf("资源处理完成 (%d 段)", completedSegments))
	return nil
}

func (s *JobService) MarkJobResourceFailed(ctx context.Context, jobID, jobResourceID int, failure error) error {
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
	s.publishEvent(jobID, "resource_failed", "error", "", fmt.Sprintf("资源处理失败: %s", message))
	return nil
}

func (s *JobService) MarkJobResourceCancelled(ctx context.Context, jobID, jobResourceID int) error {
	if err := s.client.JobResource.UpdateOneID(jobResourceID).
		SetStatus(JobResourceStatusCancelled).
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrJobResourceNotFound
		}
		return err
	}
	s.publishEvent(jobID, "resource_cancelled", "info", "", "资源处理取消")
	return nil
}

func (s *JobService) CancelJob(ctx context.Context, actorUserID, jobID int) (*ent.Job, error) {
	current, err := s.GetJob(ctx, actorUserID, jobID)
	if err != nil {
		return nil, err
	}
	if err := s.client.JobResource.Update().
		Where(jobresource.HasJobWith(job.IDEQ(current.ID)), jobresource.StatusIn(JobResourceStatusPending, JobResourceStatusRunning)).
		SetStatus(JobResourceStatusCancelled).
		Exec(ctx); err != nil {
		return nil, err
	}
	if err := s.client.Job.UpdateOneID(current.ID).
		SetStatus(JobStatusCancelled).
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	s.publishEvent(jobID, "job_cancelled", "info", "", "任务已取消")
	return s.GetJob(ctx, actorUserID, current.ID)
}

func (s *JobService) RetryJob(ctx context.Context, actorUserID, jobID int) (*ent.Job, error) {
	current, err := s.GetJob(ctx, actorUserID, jobID)
	if err != nil {
		return nil, err
	}
	if err := s.client.JobResource.Update().
		Where(jobresource.HasJobWith(job.IDEQ(current.ID)), jobresource.StatusEQ(JobResourceStatusFailed)).
		SetStatus(JobResourceStatusPending).
		SetSkippedSegments(0).
		ClearErrorMessage().
		Exec(ctx); err != nil {
		return nil, err
	}
	if err := s.client.Job.UpdateOneID(current.ID).
		SetStatus(JobStatusPending).
		SetFailedResources(0).
		SetSkippedSegments(0).
		ClearErrorMessage().
		Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	return s.GetJob(ctx, actorUserID, current.ID)
}

func (s *JobService) ReconcileJob(ctx context.Context, jobID int) error {
	current, err := s.client.Job.Query().
		Where(job.IDEQ(jobID)).
		WithJobResources().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrJobNotFound
		}
		return err
	}
	var pendingCount, runningCount, completed, failed, cancelled, completedSegments, skippedSegments int
	var firstFailure *string
	// [DEBUG] 诊断：记录每个资源的状态
	for _, item := range current.Edges.JobResources {
		completedSegments += item.CompletedSegments
		skippedSegments += item.SkippedSegments
		slog.Debug("reconcile job resource status",
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
	slog.Debug("reconcile job summary",
		"job_id", jobID,
		"total_resources", total,
		"pending", pendingCount,
		"running", runningCount,
		"completed", completed,
		"failed", failed,
		"cancelled", cancelled,
		"completed_segments", completedSegments,
	)
	status := deriveJobStatus(len(current.Edges.JobResources), pendingCount, runningCount, completed, failed, cancelled)
	// [DEBUG] 诊断：记录最终决定的作业状态
	slog.Debug("reconcile job derived status",
		"job_id", jobID,
		"derived_status", status,
		"completed_resources", completed,
		"total_resources", len(current.Edges.JobResources),
	)
	// 动态计算 stage_total：已完成资源取 stage_total（精确值），未完成资源取 segment_count（近似值）
	stageTotal := 0
	for _, item := range current.Edges.JobResources {
		if item.StageTotal > 0 {
			stageTotal += item.StageTotal
		} else {
			stageTotal += item.SegmentCount
		}
	}

	update := s.client.Job.UpdateOneID(jobID).
		SetStatus(status).
		SetResourceCount(len(current.Edges.JobResources)).
		SetCompletedResources(completed).
		SetFailedResources(failed).
		SetCompletedSegments(completedSegments).
		SetSkippedSegments(skippedSegments).
		SetStageTotal(stageTotal)
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

	// Publish lifecycle events based on derived status.
	switch status {
	case JobStatusCompleted:
		s.publishEvent(jobID, "job_completed", "info", "", "任务完成")
	case JobStatusFailed:
		errMsg := "任务失败"
		if firstFailure != nil {
			errMsg = *firstFailure
		}
		s.publishEvent(jobID, "job_failed", "error", "", errMsg)
	case JobStatusCancelled:
		s.publishEvent(jobID, "job_cancelled", "info", "", "任务已取消")
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
		return JobStatusCompleted
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

func (s *JobService) ListJobs(ctx context.Context, actorUserID, projectID int, opts JobListOptions) ([]*ent.Job, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, false); err != nil {
		return nil, err
	}
	if opts.Limit <= 0 || opts.Limit > 100 {
		opts.Limit = 50
	}
	q := s.client.Job.Query().Where(job.ProjectIDEQ(projectID))
	if opts.AfterID > 0 {
		q = q.Where(job.IDLT(opts.AfterID))
	}
	if status := strings.TrimSpace(opts.Status); status != "" {
		q = q.Where(job.StatusEQ(status))
	}
	if triggerType := strings.TrimSpace(opts.TriggerType); triggerType != "" {
		q = q.Where(job.TriggerTypeEQ(triggerType))
	}
	return q.
		WithCreatedBy().
		Order(ent.Desc(job.FieldID)).
		Limit(opts.Limit).
		All(ctx)
}

func (s *JobService) GetJob(ctx context.Context, actorUserID, jobID int) (*ent.Job, error) {
	row, err := s.client.Job.Query().
		Where(job.IDEQ(jobID)).
		WithProject().
		WithCreatedBy().
		WithJobResources(func(q *ent.JobResourceQuery) {
			q.WithResource().Order(ent.Asc(jobresource.FieldID))
		}).
		Only(ctx)
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
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectRow.ID, false); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *JobService) resolveJobSelection(ctx context.Context, projectID int, input CreateJobInput) (map[int][]int, error) {
	if len(input.SegmentGroupKeys) > 0 {
		return s.resolveGroupKeySelection(ctx, projectID, input.SegmentGroupKeys, input.ResourceIDs)
	}
	if len(input.SegmentIDs) > 0 {
		return s.resolveSegmentSelection(ctx, projectID, input.SegmentIDs)
	}
	return s.resolveResourceSelection(ctx, projectID, input.ResourceIDs)
}

func (s *JobService) resolveSegmentSelection(ctx context.Context, projectID int, segmentIDs []int) (map[int][]int, error) {
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

func (s *JobService) resolveGroupKeySelection(ctx context.Context, projectID int, groupKeys []string, resourceIDs []int) (map[int][]int, error) {
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
	rows, err := segQuery.
		Select(segment.FieldID, segment.FieldMeta, segment.FieldResourceID).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询 segments 失败：%w", err)
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

func (s *JobService) resolveResourceSelection(ctx context.Context, projectID int, resourceIDs []int) (map[int][]int, error) {
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
		ids, err := s.client.Segment.Query().
			Where(segment.ResourceIDEQ(res.ID)).
			Order(ent.Asc(segment.FieldID)).
			IDs(ctx)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			continue
		}
		selection[res.ID] = ids
	}
	return selection, nil
}

func defaultProjectConfig(projectRow *ent.Project) map[string]any {
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
	return base
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

// GetSnapshot 从 Job 的 ExecutionConfig 字段解析快照。
func GetSnapshot(job *ent.Job) (*JobExecutionSnapshot, error) {
	if job.ExecutionConfig == nil {
		return nil, nil
	}
	raw, err := json.Marshal(job.ExecutionConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal execution config: %w", err)
	}
	var snap JobExecutionSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	return &snap, nil
}

// GetExecutionSnapshot 从任务获取执行快照。
func (s *JobService) GetExecutionSnapshot(ctx context.Context, jobID int) (*JobExecutionSnapshot, error) {
	job, err := s.client.Job.Get(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("load job: %w", err)
	}
	return GetSnapshot(job)
}

// CheckJobAccess 校验用户是否有权访问任务。
func (s *JobService) CheckJobAccess(ctx context.Context, userID, jobID int) error {
	job, err := s.client.Job.Query().
		Where(job.IDEQ(jobID)).
		WithProject().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrJobNotFound
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
