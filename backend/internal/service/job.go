package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
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
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
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
	ErrJobNotCancellable   = errors.New("job is not in a cancellable state")
	ErrJobNotRetryable     = errors.New("job is not in a retryable state")
	ErrJobNoFailedResource = errors.New("job has no failed resources to retry")
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
	Enabled     bool            `json:"enabled"`
	Backend     BackendSnapshot `json:"backend"`
	MaxAttempts int             `json:"max_attempts,omitempty"`
}

// NormalizeRubyRetryAttempts 规范化 ruby_retry.max_attempts：<=0 返回 1。
func NormalizeRubyRetryAttempts(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}

// JobRoundSnapshot 单轮的完整执行快照。
type JobRoundSnapshot struct {
	Mode       string                      `json:"mode"` // "translate" | "extract" | "adjudicate" | "semantic_qa" | "revise" | "correct"
	Backend    BackendSnapshot             `json:"backend"`
	Translate  *JobTranslateRoundSnapshot  `json:"translate,omitempty"`
	Extract    *JobExtractRoundSnapshot    `json:"extract,omitempty"`
	Adjudicate *JobAdjudicateRoundSnapshot `json:"adjudicate,omitempty"`
	SemanticQA *JobSemanticQARoundSnapshot `json:"semantic_qa,omitempty"`
	Revise     *JobReviseRoundSnapshot     `json:"revise,omitempty"`
	Correct    *JobCorrectRoundSnapshot    `json:"correct,omitempty"`
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
// NOTE: 无 FallbackShrink 字段——extract 不需要缩批（仅 translate 实现）。
// 若未来需要，在此结构体加 FallbackShrink float64，并在 prepareExecutionSnapshot 赋值。
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
// NOTE: 无 FallbackShrink 字段——adjudicate 不需要缩批（仅 translate 实现）。
// 若未来需要，在此结构体加 FallbackShrink float64，并在 validateAndSnapshotWith 赋值。
type JobAdjudicateRoundSnapshot struct {
	BatchSize        int                `json:"batch_size"`
	MaxWordsPerBatch int                `json:"max_words_per_batch"`
	Concurrency      int                `json:"concurrency"`
	AdjudicateCodes  []string           `json:"adjudicate_codes,omitempty"`
	Retry            schema.RetryConfig `json:"retry"`
}

// JobSemanticQARoundSnapshot 语义质检轮次快照（无 prompt 字段，内置不可见）。
// NOTE: 无 FallbackShrink 字段——semantic_qa 不需要缩批（仅 translate 实现）。
// 若未来需要，在此结构体加 FallbackShrink float64，并在 snapshotSemanticQARound 赋值。
type JobSemanticQARoundSnapshot struct {
	BatchSize        int                `json:"batch_size"`
	MaxWordsPerBatch int                `json:"max_words_per_batch"`
	Concurrency      int                `json:"concurrency"`
	SegmentScope     string             `json:"segment_scope,omitempty"` // 物化后的 scope（空 → "all"）
	IssueCodes       []string           `json:"issue_codes,omitempty"`   // 仅 with_issue_codes 有效
	Retry            schema.RetryConfig `json:"retry"`
}

// JobReviseRoundSnapshot LLM 修订轮次快照（无 prompt 字段，内置不可见）。
// NOTE: 无 FallbackShrink 字段——revise 不需要缩批（仅 translate 实现）。
// 若未来需要，在此结构体加 FallbackShrink float64，并在 snapshotReviseRound 赋值。
type JobReviseRoundSnapshot struct {
	BatchSize        int                `json:"batch_size"`
	MaxWordsPerBatch int                `json:"max_words_per_batch"`
	Concurrency      int                `json:"concurrency"`
	SegmentScope     string             `json:"segment_scope,omitempty"` // 物化后的 scope（空 → "with_issues"）
	IssueCodes       []string           `json:"issue_codes,omitempty"`   // with_issues 为空时物化为完整语义白名单
	Retry            schema.RetryConfig `json:"retry"`
}

// JobCorrectRoundSnapshot 本地改写轮次快照（纯本地、不调 LLM，无 prompt/backend 字段）。
// NOTE: 无 FallbackShrink — correct 不接缩批（与 extract/adjudicate/semantic_qa 一致）。
// NOTE: 无 Retry — schema 层 CorrectRoundConfig 无 Retry（纯本地、无外部 I/O、无重试语义）。
// NOTE: 无 Enabled — 是否执行由轮次是否出现在 rounds 数组决定（与其他轮次一致）。
type JobCorrectRoundSnapshot struct {
	Rules       []JobCorrectRuleSnapshot `json:"rules,omitempty"`
	Concurrency int                      `json:"concurrency"`
}

// JobCorrectRuleSnapshot 单条本地改写规则快照。
type JobCorrectRuleSnapshot struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func snapshotCorrectRound(c *schema.CorrectRoundConfig) *JobCorrectRoundSnapshot {
	rules := make([]JobCorrectRuleSnapshot, 0, len(c.Rules))
	for _, r := range c.Rules {
		rules = append(rules, JobCorrectRuleSnapshot{Name: r.Name, Enabled: r.Enabled})
	}
	return &JobCorrectRoundSnapshot{
		Rules:       rules,
		Concurrency: c.Concurrency,
	}
}

func snapshotReviseRound(r *schema.ReviseRoundConfig) *JobReviseRoundSnapshot {
	scope := r.SegmentScope
	if scope == "" {
		scope = "with_issues"
	}
	issueCodes := append([]string(nil), r.IssueCodes...)
	if scope == "with_issues" {
		// 规范约定 issue_codes 仅 with_issue_codes 生效；with_issues 一律物化为
		// 完整语义白名单。执行链（ReviseHandler）只有 codes 维度、无 scope 维度，
		// 若保留用户误填的子集会被当作过滤条件，漏掉其余 pending 语义 issue。
		issueCodes = append([]string(nil), qa.SemanticQACodes()...)
	}
	return &JobReviseRoundSnapshot{
		BatchSize:        r.BatchSize,
		MaxWordsPerBatch: r.MaxWordsPerBatch,
		Concurrency:      r.Concurrency,
		SegmentScope:     scope,
		IssueCodes:       issueCodes,
		Retry:            r.Retry,
	}
}

func snapshotSemanticQARound(s *schema.SemanticQARoundConfig) *JobSemanticQARoundSnapshot {
	scope := s.SegmentScope
	if scope == "" {
		scope = "all"
	}
	return &JobSemanticQARoundSnapshot{
		BatchSize:        s.BatchSize,
		MaxWordsPerBatch: s.MaxWordsPerBatch,
		Concurrency:      s.Concurrency,
		SegmentScope:     scope,
		IssueCodes:       append([]string(nil), s.IssueCodes...),
		Retry:            s.Retry,
	}
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

	// 2. 加载、校验执行计划并生成不可变快照。
	snapshot, err := s.prepareExecutionSnapshot(ctx, actorUserID, projectRow, input.ExecutionPlanID, input.SegmentFilter)
	if err != nil {
		return nil, err
	}
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
		SetExecutionPlanID(snapshot.ExecutionPlanID).
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

// prepareExecutionSnapshot loads an execution plan and freezes every runtime
// dependency used by jobs and synchronous previews at request creation time.
func (s *JobService) prepareExecutionSnapshot(
	ctx context.Context,
	actorUserID int,
	projectRow *ent.Project,
	executionPlanID int,
	overrideSegmentFilter string,
) (*JobExecutionSnapshot, error) {
	plan, err := s.executionPlans.GetByID(ctx, actorUserID, executionPlanID)
	if err != nil {
		return nil, fmt.Errorf("execution plan: %w", err)
	}
	snapshot, err := s.validateAndSnapshot(ctx, actorUserID, projectRow, plan, overrideSegmentFilter)
	if err != nil {
		return nil, err
	}
	snapshot.SourceLang = projectRow.SourceLang
	snapshot.TargetLang = projectRow.TargetLang
	snapshot.GlossaryEnabled = jobGlossaryEnabled(projectRow.GlossaryEnabled, snapshot.Rounds)
	return snapshot, nil
}

// validateAndSnapshotWith 校验执行计划中的每轮配置并生成完整快照，backend 可访问性由 check 注入。
func (s *JobService) validateAndSnapshotWith(
	ctx context.Context,
	plan *ent.ExecutionPlanTemplate,
	overrideSegmentFilter string,
	check func(backendID int) error,
) (*JobExecutionSnapshot, error) {
	snapshot := &JobExecutionSnapshot{
		ExecutionPlanID:   plan.ID,
		ExecutionPlanName: plan.Name,
		Rounds:            make([]JobRoundSnapshot, 0, len(plan.Rounds)),
	}

	for i, round := range plan.Rounds {
		// correct 是纯本地轮，无 backend：跳过 RBAC check 与 backend snapshot，短路出零 BackendSnapshot。
		if round.Mode == "correct" {
			if round.Correct == nil {
				return nil, fmt.Errorf("rounds[%d] correct config is nil", i)
			}
			snapshot.Rounds = append(snapshot.Rounds, JobRoundSnapshot{
				Mode:    "correct",
				Backend: BackendSnapshot{}, // 零值（无 backend）
				Correct: snapshotCorrectRound(round.Correct),
			})
			continue
		}

		// 校验后端可访问性
		if err := check(round.BackendID); err != nil {
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

		case "semantic_qa":
			if round.SemanticQA == nil {
				return nil, fmt.Errorf("rounds[%d] semantic_qa config is nil", i)
			}
			snapshot.Rounds = append(snapshot.Rounds, JobRoundSnapshot{
				Mode:       "semantic_qa",
				Backend:    *backendSnap,
				SemanticQA: snapshotSemanticQARound(round.SemanticQA),
			})

		case "revise":
			if round.Revise == nil {
				return nil, fmt.Errorf("rounds[%d] revise config is nil", i)
			}
			r := round.Revise
			scope := r.SegmentScope
			if scope == "" {
				scope = "with_issues"
			}
			if scope != "with_issues" && scope != "with_issue_codes" {
				return nil, fmt.Errorf("rounds[%d] revise segment_scope invalid: %s", i, r.SegmentScope)
			}
			if scope == "with_issue_codes" && len(r.IssueCodes) == 0 {
				return nil, fmt.Errorf("rounds[%d] revise issue_codes is empty for with_issue_codes", i)
			}
			for _, code := range r.IssueCodes {
				if !qa.IsSemanticQACode(code) {
					return nil, fmt.Errorf("rounds[%d] revise issue_codes contains invalid code: %s", i, code)
				}
			}
			snapshot.Rounds = append(snapshot.Rounds, JobRoundSnapshot{
				Mode:    "revise",
				Backend: *backendSnap,
				Revise:  snapshotReviseRound(r),
			})

		default:
			return nil, fmt.Errorf("rounds[%d] unsupported mode: %s", i, round.Mode)
		}
	}

	// 注音对齐重试快照
	if plan.RubyRetry.Enabled && plan.RubyRetry.BackendID > 0 {
		rr := &plan.RubyRetry

		if err := check(rr.BackendID); err != nil {
			return nil, fmt.Errorf("ruby retry backend: %w", err)
		}

		rrBackendSnap, err := s.snapshotBackend(ctx, rr.BackendID)
		if err != nil {
			return nil, fmt.Errorf("ruby retry snapshot backend: %w", err)
		}

		snapshot.RubyRetry = &ExecutionPlanRubyRetrySnapshot{
			Enabled:     true,
			Backend:     *rrBackendSnap,
			MaxAttempts: NormalizeRubyRetryAttempts(rr.MaxAttempts),
		}
	}

	return snapshot, nil
}

// validateAndSnapshot 校验执行计划中的每轮配置，并生成完整快照。（保持原有行为不变）
func (s *JobService) validateAndSnapshot(
	ctx context.Context,
	actorUserID int,
	projectRow *ent.Project,
	plan *ent.ExecutionPlanTemplate,
	overrideSegmentFilter string,
) (*JobExecutionSnapshot, error) {
	return s.validateAndSnapshotWith(ctx, plan, overrideSegmentFilter, func(backendID int) error {
		return s.validateBackendAccess(ctx, projectRow, backendID)
	})
}

// validateBackendAccessForActor 检查后端对 actor(用户/org 级) 是否可访问，不依赖项目。
// 复用 BackendService.requireOwnership:user scope 直接比对 owner;org scope 要求 admin。
// 即时翻译等无项目场景下，统一归一化授权失败为 ErrBackendNotFound，
// 与 user-scope 的"不泄露后端存在性"语义一致，避免 org 成员通过 403/404 探测后端存在。
func (s *JobService) validateBackendAccessForActor(ctx context.Context, actorUserID, backendID int) error {
	if _, err := s.backends.requireOwnership(ctx, actorUserID, backendID); err != nil {
		if errors.Is(err, ErrForbidden) {
			return ErrBackendNotFound
		}
		return err
	}
	return nil
}

// prepareExecutionSnapshotForActor 构建即时翻译等场景的执行快照。
// backend 授权检查通过 check 注入：
//   - 无项目（projectRow == nil）：以 actor 自身身份为准（requireOwnership），
//     统一归一化授权失败为 ErrBackendNotFound，避免 org 成员通过 403/404 探测后端存在。
//   - 有项目（projectRow != nil）：复用 job/preview 的项目级 validateBackendAccess，
//     按 *项目 owner* 的 org 归属判定，使同一 plan+project 在 job/preview/即时翻译
//     三条路径上的后端鉴权语义保持一致。
//
// source/target 语言与 glossaryEnabled 由调用方显式传入。
func (s *JobService) prepareExecutionSnapshotForActor(
	ctx context.Context,
	actorUserID, executionPlanID int,
	overrideSegmentFilter, sourceLang, targetLang string,
	glossaryEnabled bool,
	projectRow *ent.Project,
) (*JobExecutionSnapshot, error) {
	plan, err := s.executionPlans.GetByID(ctx, actorUserID, executionPlanID)
	if err != nil {
		return nil, fmt.Errorf("execution plan: %w", err)
	}
	var check func(backendID int) error
	if projectRow != nil {
		check = func(backendID int) error {
			return s.validateBackendAccess(ctx, projectRow, backendID)
		}
	} else {
		check = func(backendID int) error {
			return s.validateBackendAccessForActor(ctx, actorUserID, backendID)
		}
	}
	snapshot, err := s.validateAndSnapshotWith(ctx, plan, overrideSegmentFilter, check)
	if err != nil {
		return nil, err
	}
	snapshot.SourceLang = sourceLang
	snapshot.TargetLang = targetLang
	snapshot.GlossaryEnabled = glossaryEnabled
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

// NormalizeShrink 将 shrink 值规范化：NaN、Inf 视为 1.0（不缩）；(0,1] 原样返回。
// 快照写入（service）与引擎读取（worker）共用同一实现，避免两处重复定义导致漂移。
// 合法域严格 (0,1]（校验层拒绝 0，与 OpenAPI exclusiveMinimum:0 一致）。
// NaN/Inf 是 float64 的合法但无意义值，作防御性兜底规范化为 1.0。
func NormalizeShrink(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 1.0
	}
	return v
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

func (s *JobService) MarkJobResourceCompleted(ctx context.Context, jobID, jobResourceID int, outputPath string, completedSegments, skippedSegments int, warning string) error {
	update := s.client.JobResource.UpdateOneID(jobResourceID).
		SetStatus(JobResourceStatusCompleted).
		SetOutputPath(strings.TrimSpace(outputPath)).
		SetCompletedSegments(completedSegments).
		SetSkippedSegments(skippedSegments).
		ClearErrorMessage()
	warning = strings.TrimSpace(warning)
	if warning != "" {
		update.SetWarningMessage(warning)
	} else {
		update.ClearWarningMessage()
	}
	if err := update.Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrJobResourceNotFound
		}
		return err
	}
	s.publishEvent(jobID, "resource_completed", "info", "", fmt.Sprintf("资源处理完成 (%d 段)", completedSegments))
	if warning != "" {
		s.publishEvent(jobID, "resource_warning", "warning", "", warning)
	}
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
	// 仅 pending/running 可取消；completed/failed/cancelled 为终态，取消会篡改状态。
	if current.Status != JobStatusPending && current.Status != JobStatusRunning {
		return nil, ErrJobNotCancellable
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
	// 仅 failed 任务可重试；对 completed 任务重试会把它重新入队空耗 worker。
	if current.Status != JobStatusFailed {
		return nil, ErrJobNotRetryable
	}
	// 必须存在 failed 资源才有可重试的对象，从本次读取的资源实时计数。
	failedResources := 0
	for _, item := range current.Edges.JobResources {
		if item.Status == JobResourceStatusFailed {
			failedResources++
		}
	}
	if failedResources == 0 {
		return nil, ErrJobNoFailedResource
	}
	if err := s.client.JobResource.Update().
		Where(jobresource.HasJobWith(job.IDEQ(current.ID)), jobresource.StatusEQ(JobResourceStatusFailed)).
		SetStatus(JobResourceStatusPending).
		SetSkippedSegments(0).
		ClearErrorMessage().
		ClearWarningMessage().
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
	var weightedTotal, weightedCompleted int
	var firstFailure *string
	// [DEBUG] 诊断：记录每个资源的状态
	for _, item := range current.Edges.JobResources {
		completedSegments += item.CompletedSegments
		skippedSegments += item.SkippedSegments
		weightedTotal += item.WeightedTotal
		weightedCompleted += item.WeightedCompleted
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

	update := s.client.Job.UpdateOneID(jobID).
		SetStatus(status).
		SetResourceCount(len(current.Edges.JobResources)).
		SetCompletedResources(completed).
		SetFailedResources(failed).
		SetCompletedSegments(completedSegments).
		SetSkippedSegments(skippedSegments).
		SetWeightedTotal(weightedTotal).
		SetWeightedCompleted(weightedCompleted)
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
		// 聚合资源级软警告为 job_warning（不改变 completed 状态）。
		var warnings []string
		for _, item := range current.Edges.JobResources {
			if item.WarningMessage != nil {
				if msg := strings.TrimSpace(*item.WarningMessage); msg != "" {
					warnings = append(warnings, msg)
				}
			}
		}
		if len(warnings) > 0 {
			s.publishEvent(jobID, "job_warning", "warning", "", strings.Join(warnings, "; "))
		}
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
