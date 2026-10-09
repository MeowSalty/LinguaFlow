package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

const (
	JobStatusPending   = "pending"
	JobStatusRunning   = "running"
	JobStatusPausing   = "pausing"
	JobStatusPaused    = "paused"
	JobStatusCompleted = "completed"
	JobStatusFailed    = "failed"
	JobStatusCancelled = "cancelled"

	JobTriggerManual = "manual"

	JobResourceStatusPending   = "pending"
	JobResourceStatusRunning   = "running"
	JobResourceStatusCompleted = "completed"
	JobResourceStatusFailed    = "failed"
	JobResourceStatusCancelled = "cancelled"

	// JobRound 状态机：pending → running → completed/failed；skipped 由
	// pending 直达（本轮无段可处理）。JobRound 矩阵是进度的唯一事实源。
	JobRoundStatusPending   = "pending"
	JobRoundStatusRunning   = "running"
	JobRoundStatusCompleted = "completed"
	JobRoundStatusFailed    = "failed"
	JobRoundStatusSkipped   = "skipped"
)

var (
	ErrJobNotFound            = errors.New("job not found")
	ErrJobEmpty               = errors.New("job has no pending segments")
	ErrJobActorMissing        = errors.New("job actor unavailable")
	ErrJobNotCancellable      = errors.New("job is not in a cancellable state")
	ErrJobNotRetryable        = errors.New("job is not in a retryable state")
	ErrJobNoFailedResource    = errors.New("job has no failed resources to retry")
	ErrJobNotRunnable         = errors.New("job is not in a runnable state")
	ErrJobNotPausable         = errors.New("job is not in a pausable state")
	ErrJobNotResumable        = errors.New("job is not in a resumable state")
	ErrJobResourceNotRunnable = errors.New("job resource is not in a runnable state")
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
	lifecycle                  *tasklife.Coordinator
	cancelTask                 func(int)
	pauseTask                  func(int) bool
}

// SetLifecycle is configured before serving requests or starting workers.
func (s *JobService) SetLifecycle(coordinator *tasklife.Coordinator) { s.lifecycle = coordinator }

// SetTaskControl keeps execution notifications inside the same control guard
// as the corresponding durable state transition.
func (s *JobService) SetTaskControl(cancel func(int), pause func(int) bool) {
	s.cancelTask, s.pauseTask = cancel, pause
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
type JobExecutionSnapshot = execution.JobExecutionSnapshot

// ExecutionPlanRubyRetrySnapshot 注音对齐重试快照。
type ExecutionPlanRubyRetrySnapshot = execution.ExecutionPlanRubyRetrySnapshot

// NormalizeRubyRetryAttempts 规范化 ruby_retry.max_attempts：<=0 返回 1。
func NormalizeRubyRetryAttempts(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}

// JobRoundSnapshot 单轮的完整执行快照。
type JobRoundSnapshot = execution.JobRoundSnapshot

// JobTranslateRoundSnapshot 翻译轮次快照。
// 无 Strategy 字段：策略快照位于 JobExecutionSnapshot.Strategy（计划级引用物化一次）。
type JobTranslateRoundSnapshot = execution.JobTranslateRoundSnapshot

// JobExtractRoundSnapshot 术语抽取轮次快照。
// NOTE: 无 FallbackShrink 字段——extract 不需要缩批（仅 translate 实现）。
// 若未来需要，在此结构体加 FallbackShrink float64，并在 prepareExecutionSnapshot 赋值。
type JobExtractRoundSnapshot = execution.JobExtractRoundSnapshot

// JobAdjudicateRoundSnapshot 质量裁决轮次快照（无 prompt 字段，内置不可见）。
// NOTE: 无 FallbackShrink 字段——adjudicate 不需要缩批（仅 translate 实现）。
// 若未来需要，在此结构体加 FallbackShrink float64，并在 validateAndSnapshotWith 赋值。
type JobAdjudicateRoundSnapshot = execution.JobAdjudicateRoundSnapshot

// JobSemanticQARoundSnapshot 语义质检轮次快照（无 prompt 字段，内置不可见）。
// NOTE: 无 FallbackShrink 字段——semantic_qa 不需要缩批（仅 translate 实现）。
// 若未来需要，在此结构体加 FallbackShrink float64，并在 snapshotSemanticQARound 赋值。
type JobSemanticQARoundSnapshot = execution.JobSemanticQARoundSnapshot

// JobReviseRoundSnapshot LLM 修订轮次快照（无 prompt 字段，内置不可见）。
// NOTE: 无 FallbackShrink 字段——revise 不需要缩批（仅 translate 实现）。
// 若未来需要，在此结构体加 FallbackShrink float64，并在 snapshotReviseRound 赋值。
type JobReviseRoundSnapshot = execution.JobReviseRoundSnapshot

// JobCorrectRoundSnapshot 本地改写轮次快照（纯本地、不调 LLM，无 prompt/backend 字段）。
// NOTE: 无 FallbackShrink — correct 不接缩批（与 extract/adjudicate/semantic_qa 一致）。
// NOTE: 无 Retry — schema 层 CorrectRoundConfig 无 Retry（纯本地、无外部 I/O、无重试语义）。
// NOTE: 无 Enabled — 是否执行由轮次是否出现在 rounds 数组决定（与其他轮次一致）。
type JobCorrectRoundSnapshot = execution.JobCorrectRoundSnapshot

// JobCorrectRuleSnapshot 单条本地改写规则快照。
type JobCorrectRuleSnapshot = execution.JobCorrectRuleSnapshot

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
	issueCodes := slices.Clone(r.IssueCodes)
	if scope == "with_issues" && (issueCodes == nil || len(issueCodes) > 0) {
		// with_issues 下省略列表或提供非空子集均使用完整语义白名单，
		// 显式空数组保留为不处理任何问题。执行链只消费冻结后的 codes。
		issueCodes = append([]string(nil), qa.SemanticQACodes()...)
	}
	return &JobReviseRoundSnapshot{
		TemplateContent:  templates.EmbeddedReviseTemplate(),
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
		TemplateContent:  templates.EmbeddedSemanticQATemplate(),
		BatchSize:        s.BatchSize,
		MaxWordsPerBatch: s.MaxWordsPerBatch,
		Concurrency:      s.Concurrency,
		SegmentScope:     scope,
		IssueCodes:       append([]string(nil), s.IssueCodes...),
		Retry:            s.Retry,
	}
}

// SegmentFilterSnapshot 翻译轮次段落过滤快照。
type SegmentFilterSnapshot = execution.SegmentFilterSnapshot

// BackendSnapshot 后端配置快照。
type BackendSnapshot = execution.BackendSnapshot

// PromptSnapshot 翻译提示词模板快照。
type PromptSnapshot = execution.PromptSnapshot

// BootstrapPromptSnapshot 术语抽取提示词模板快照。
type BootstrapPromptSnapshot = execution.BootstrapPromptSnapshot

// StrategySnapshot 策略模板快照。
type StrategySnapshot = execution.StrategySnapshot

// --- CRUD 方法 ---

// CreateManualJob 创建手动翻译任务。
func (s *JobService) CreateManualJob(ctx context.Context, actorUserID, projectID int, input CreateJobInput) (*ent.Job, error) {
	// 1. 校验项目访问权限
	projectRow, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true)
	if err != nil {
		return nil, err
	}

	// 2. 加载、校验执行计划并生成不可变快照。
	snapshot, release, err := s.prepareExecutionSnapshot(ctx, actorUserID, projectRow, input.ExecutionPlanID, input.SegmentFilter)
	if err != nil {
		return nil, err
	}
	defer release()
	snapshot.AutoApprove = input.AutoApprove
	snapshot.ExplicitSegmentSelection = len(input.SegmentGroupKeys) == 0 && len(input.SegmentIDs) > 0

	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot: %w", err)
	}

	// 5. 解析任务选择
	sourceRows, err := s.client.Resource.Query().Where(resource.ProjectIDEQ(projectID)).All(ctx)
	if err != nil {
		return nil, err
	}
	sources := make(map[int]*ent.Resource, len(sourceRows))
	for _, row := range sourceRows {
		sources[row.ID] = row
	}
	selection, err := resolveJobSelection(ctx, s.client, projectID, input)
	if err != nil {
		return nil, err
	}
	if len(selection) == 0 {
		return nil, ErrJobEmpty
	}

	resourceIDs := make([]int, 0, len(selection))
	for resourceID := range selection {
		resourceIDs = append(resourceIDs, resourceID)
	}
	sort.Ints(resourceIDs)

	// 聚合每个资源的工作量权重：SUM(LENGTH(CAST(source_text AS BLOB))) 字节口径，
	// 在事务外以只读查询完成（选择解析同样在事务外）。动态选择（segment_ids 为空）
	// 留 0，由 worker 侧 back-fill。
	weights := make(map[int]int64, len(resourceIDs))
	for _, resourceID := range resourceIDs {
		weight, err := sumSegmentWorkWeight(ctx, s.client, selection[resourceID])
		if err != nil {
			return nil, fmt.Errorf("sum work weight for resource %d: %w", resourceID, err)
		}
		weights[resourceID] = weight
	}

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
		Save(ctx)
	if err != nil {
		return nil, err
	}

	// 预建 resource×round 完整矩阵（含动态选择资源）：每资源每轮一行 pending
	// JobRound。矩阵是进度的唯一事实源，Job.progress_total/progress_completed
	// 是其派生缓存（DBReporter 首次揭示工作量时累加分母）。
	roundBuilders := make([]*ent.JobRoundCreate, 0, len(resourceIDs)*len(snapshot.Rounds))
	for _, resourceID := range resourceIDs {
		segmentIDs := append([]int(nil), selection[resourceID]...)
		sort.Ints(segmentIDs)
		source := sources[resourceID]
		if source == nil {
			return nil, ErrSourceRevisionConflict
		}
		if err := GuardSourceGeneration(ctx, tx.Client(), resourceID, source.SourceGeneration); err != nil {
			return nil, err
		}
		jr, err := tx.JobResource.Create().
			SetJobID(created.ID).
			SetResourceID(resourceID).
			SetSourceGeneration(source.SourceGeneration).
			SetNillableSourceRevisionID(source.CurrentSourceRevisionID).
			SetStatus(JobResourceStatusPending).
			SetSegmentIds(segmentIDs).
			SetSegmentCount(len(segmentIDs)).
			SetWorkWeight(weights[resourceID]).
			Save(ctx)
		if err != nil {
			return nil, err
		}
		for i, rd := range snapshot.Rounds {
			roundBuilders = append(roundBuilders, tx.JobRound.Create().
				SetJobID(created.ID).
				SetJobResourceID(jr.ID).
				SetRoundIndex(i).
				SetMode(rd.Mode).
				SetStatus(JobRoundStatusPending))
		}
	}
	if len(roundBuilders) > 0 {
		if err := bulkCreateJobRounds(ctx, roundBuilders, tx.JobRound.CreateBulk); err != nil {
			return nil, err
		}
	}

	if credentials := s.backends.Credentials(); credentials != nil {
		if err := credentials.RetainJob(ctx, tx, created.ID, snapshot.Bindings()); err != nil {
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

// prepareExecutionSnapshot 加载执行计划，并在请求创建时冻结
// job 与同步预览所用的每一项运行时依赖。
func (s *JobService) prepareExecutionSnapshot(
	ctx context.Context,
	actorUserID int,
	projectRow *ent.Project,
	executionPlanID int,
	overrideSegmentFilter string,
) (*JobExecutionSnapshot, func(), error) {
	ctx, leases := withExecutionLeases(ctx)
	successful := false
	defer func() {
		if !successful {
			leases.release()
		}
	}()
	plan, err := s.executionPlans.GetByID(ctx, actorUserID, executionPlanID)
	if err != nil {
		return nil, nil, fmt.Errorf("execution plan: %w", err)
	}
	snapshot, err := s.validateAndSnapshot(ctx, actorUserID, projectRow, plan, overrideSegmentFilter)
	if err != nil {
		return nil, nil, err
	}
	snapshot.SourceLang = projectRow.SourceLang
	snapshot.TargetLang = projectRow.TargetLang
	snapshot.GlossaryEnabled = projectRow.GlossaryEnabled
	resolved, err := execution.Resolve(*snapshot)
	if err != nil {
		return nil, nil, err
	}
	successful = true
	return resolved, leases.release, nil
}

// validateAndSnapshotWith 校验执行计划中的每轮配置并生成完整快照，backend 可访问性由 check 注入，
// 策略访问权经 snapshotProfile 以 actorUserID 复核。
func (s *JobService) validateAndSnapshotWith(
	ctx context.Context,
	actorUserID int,
	plan *ent.ExecutionPlanTemplate,
	overrideSegmentFilter string,
	check func(backendID int) error,
) (*JobExecutionSnapshot, error) {
	snapshot := &JobExecutionSnapshot{
		SchemaVersion: execution.SnapshotSchemaVersion, DefaultsVersion: execution.SnapshotDefaultsVersion,
		RubyProtocolVersion: execution.RubyProtocolVersion, RubyValidatorVersion: execution.RubyValidatorVersion,
		RubyTemplates:     execution.RubyTemplates{JSON: prompt.RubyAlignmentJSONTemplate, Text: prompt.RubyAlignmentTextTemplate},
		ExecutionPlanID:   plan.ID,
		ExecutionPlanName: plan.Name,
		Rounds:            make([]JobRoundSnapshot, 0, len(plan.Rounds)),
	}

	// 计划级策略快照：在轮次循环前物化一次，为全管道（所有改写型轮次与
	// 引擎级行为）供 protect/ruby 等行为预设。
	strategySnap, err := s.snapshotProfile(ctx, actorUserID, plan.ProfileID)
	if err != nil {
		return nil, fmt.Errorf("snapshot strategy profile: %w", err)
	}
	snapshot.Strategy = *strategySnap

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
			promptSnap, err := s.snapshotPromptTemplate(ctx, actorUserID, t.PromptTemplateID)
			if err != nil {
				return nil, fmt.Errorf("rounds[%d] snapshot prompt: %w", i, err)
			}

			// 校验翻译模板必填
			if promptSnap.Content == "" {
				return nil, fmt.Errorf("rounds[%d] prompt_template %q has no system_prompt_content (translation prompt is required)", i, promptSnap.TemplateName)
			}

			snapshot.Rounds = append(snapshot.Rounds, JobRoundSnapshot{
				Mode:    "translate",
				Backend: *backendSnap,
				Translate: &JobTranslateRoundSnapshot{
					Prompt:               *promptSnap,
					BatchSize:            t.BatchSize,
					MaxWordsPerBatch:     t.MaxWordsPerBatch,
					Concurrency:          t.Concurrency,
					FallbackShrink:       t.FallbackShrink,
					SegmentFilter:        snapshotSegmentFilter(t.SegmentFilter, overrideSegmentFilter),
					InlineTermExtraction: t.InlineTermExtraction,
					Retry:                t.Retry,
				},
			})

		case "extract":
			if round.Extract == nil {
				return nil, fmt.Errorf("rounds[%d] extract config is nil", i)
			}
			e := round.Extract

			// 快照自举提示词模板
			bootstrapSnap, err := s.snapshotBootstrapTemplate(ctx, actorUserID, e.BootstrapTemplateID)
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
			if codes == nil {
				codes = qa.DefaultAdjudicateCodes()
			}
			snapshot.Rounds = append(snapshot.Rounds, JobRoundSnapshot{
				Mode:    "adjudicate",
				Backend: *backendSnap,
				Adjudicate: &JobAdjudicateRoundSnapshot{
					TemplateContent:  templates.EmbeddedAdjudicationTemplate(),
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

	// 注音对齐重试快照。
	// backend_id=0 表示复用翻译主后端（文档承诺）：从已构建的 snapshot.Rounds 中取
	// 第一条 translate 轮的 Backend 快照回填，避免 RubyRetry 因缺后端留空导致重试
	// 静默失效；无 translate 轮可回退时维持留 nil（与既有行为一致）。
	rr := &plan.RubyRetry
	rubyConcurrency, err := execution.ResolveRubyRetryConcurrency(rr.Concurrency)
	if err != nil {
		return nil, err
	}
	var rrBackendSnap *BackendSnapshot
	if rr.Enabled && rr.BackendID > 0 {
		if err := check(rr.BackendID); err != nil {
			return nil, fmt.Errorf("ruby retry backend: %w", err)
		}

		snap, err := s.snapshotBackend(ctx, rr.BackendID)
		if err != nil {
			return nil, fmt.Errorf("ruby retry snapshot backend: %w", err)
		}
		rrBackendSnap = snap
	} else if rr.Enabled {
		for i := range snapshot.Rounds {
			if snapshot.Rounds[i].Mode == "translate" {
				backend := snapshot.Rounds[i].Backend
				rrBackendSnap = &backend
				break
			}
		}
	}
	if rrBackendSnap != nil {
		snapshot.RubyRetry = &ExecutionPlanRubyRetrySnapshot{
			Enabled:     true,
			Backend:     *rrBackendSnap,
			MaxAttempts: NormalizeRubyRetryAttempts(rr.MaxAttempts),
			Concurrency: rubyConcurrency,
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
	if err := s.validateSnapshotReferences(ctx, actorUserID, projectRow, plan); err != nil {
		return nil, err
	}
	return s.validateAndSnapshotWith(ctx, actorUserID, plan, overrideSegmentFilter, func(backendID int) error {
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
) (*JobExecutionSnapshot, func(), error) {
	ctx, leases := withExecutionLeases(ctx)
	successful := false
	defer func() {
		if !successful {
			leases.release()
		}
	}()
	plan, err := s.executionPlans.GetByID(ctx, actorUserID, executionPlanID)
	if err != nil {
		return nil, nil, fmt.Errorf("execution plan: %w", err)
	}
	if err := s.validateSnapshotReferences(ctx, actorUserID, projectRow, plan); err != nil {
		return nil, nil, err
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
	snapshot, err := s.validateAndSnapshotWith(ctx, actorUserID, plan, overrideSegmentFilter, check)
	if err != nil {
		return nil, nil, err
	}
	snapshot.SourceLang = sourceLang
	snapshot.TargetLang = targetLang
	snapshot.GlossaryEnabled = glossaryEnabled
	resolved, err := execution.Resolve(*snapshot)
	if err != nil {
		return nil, nil, err
	}
	successful = true
	return resolved, leases.release, nil
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
	credentials := s.backends.Credentials()
	if credentials == nil {
		return nil, credential.ErrUnavailable
	}
	binding, release, err := credentials.AcquireBackend(ctx, backendID)
	if err != nil {
		return nil, err
	}
	if leases, ok := ctx.Value(executionLeaseKey{}).(*executionLeaseScope); ok {
		leases.releases = append(leases.releases, release)
	} else {
		defer release()
	}
	options, err := execution.ResolveBackendOptions(string(b.BackendType), b.Options)
	if err != nil {
		return nil, err
	}
	if err := credentials.Check(ctx, binding, backendID, string(b.BackendType), options["base_url"].(string)); err != nil {
		return nil, err
	}
	return &BackendSnapshot{
		Credential:         binding,
		ID:                 b.ID,
		Scope:              b.Scope,
		Name:               b.Name,
		Type:               string(b.BackendType),
		Options:            options,
		RateLimitPerMinute: b.RateLimitPerMinute,
	}, nil
}

// snapshotPromptTemplate 快照翻译提示词模板。
func (s *JobService) snapshotPromptTemplate(ctx context.Context, actorUserID, templateID int) (*PromptSnapshot, error) {
	pt, err := s.translationPromptTemplates.GetByID(ctx, actorUserID, templateID)
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
func (s *JobService) snapshotBootstrapTemplate(ctx context.Context, actorUserID, templateID int) (*BootstrapPromptSnapshot, error) {
	pt, err := s.bootstrapPromptTemplates.GetByID(ctx, actorUserID, templateID)
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

// snapshotProfile 快照策略模板。userID 为触发快照的调用者：正数策略在物化前经
// CheckAccess 复核访问权（与轮次 backend 的 check 注入对齐——计划创建后属主或
// 组织资格可能已变更），内置策略（scope=system 虚拟实体）对全体放行。
func (s *JobService) snapshotProfile(ctx context.Context, userID, profileID int) (*StrategySnapshot, error) {
	tp, err := s.profiles.GetByID(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	if err := s.profiles.CheckAccess(ctx, userID, tp); err != nil {
		return nil, err
	}

	id := tp.ID
	return &StrategySnapshot{
		ProfileID:   &id,
		ProfileName: tp.Name,
		Protect:     tp.Config.Protect,
		Postprocess: tp.Config.Postprocess,
		Repair:      tp.Config.Repair,
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
		Where(job.StatusIn(JobStatusPending, JobStatusRunning, JobStatusPausing)).
		WithJobResources().
		Order(ent.Asc(job.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(jobs))
	for _, current := range jobs {
		recovered, err := s.recoverLegacyJob(ctx, current.ID)
		if err != nil {
			return nil, err
		}
		if recovered {
			ids = append(ids, current.ID)
		}
	}
	return ids, nil
}

func (s *JobService) recoverLegacyJob(ctx context.Context, id int) (bool, error) {
	guard, err := s.lifecycle.Lock(ctx, "translation", id)
	if err != nil {
		return false, err
	}
	defer guard.Release()
	if guard.Active() {
		return false, tasklife.ErrBusy
	}
	current, err := s.client.Job.Query().Where(job.IDEQ(id)).WithJobResources().Only(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current.Status != JobStatusPending && current.Status != JobStatusRunning && current.Status != JobStatusPausing {
		return false, nil
	}
	var sourceErr error
	for _, item := range current.Edges.JobResources {
		if item.Status != JobResourceStatusPending && item.Status != JobResourceStatusRunning {
			continue
		}
		if sourceErr = ValidateJobResourceSource(ctx, s.client, item); sourceErr != nil {
			break
		}
	}
	if sourceErr != nil {
		if !errors.Is(sourceErr, ErrSourceRevisionConflict) {
			return false, sourceErr
		}
		return false, s.failRecoveryJob(ctx, current.ID, "source_revision_conflict: source changed before recovery")
	}
	if err := s.checkExecutionPolicies(ctx, current); err != nil {
		return false, s.failRecoveryJob(ctx, current.ID, "execution recovery refused: "+err.Error())
	}
	if err := s.prepareRecoveredJobState(ctx, current.ID); err != nil {
		return false, err
	}
	return !current.PauseRequested && current.Status != JobStatusPausing, nil
}

// Called under the lifecycle guard, only for a newly failed recovery attempt.
func (s *JobService) failRecoveryJob(ctx context.Context, id int, message string) error {
	now := timeutil.NowUTC()
	return s.client.Job.Update().Where(job.IDEQ(id), job.StatusIn(JobStatusPending, JobStatusRunning)).
		SetStatus(JobStatusFailed).SetErrorMessage(message).SetFinishedAt(now).SetRetentionAnchorAt(now).Exec(ctx)
}

// backfillJobRoundsForRecovery 仅从完整、经过校验的快照重建缺失的矩阵。
// 它绝不解释缺失或过时的格式。
func (s *JobService) backfillJobRoundsForRecovery(ctx context.Context, jobID int) error {
	count, err := s.client.JobRound.Query().
		Where(jobround.JobIDEQ(jobID)).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("count job rounds: %w", err)
	}
	if count > 0 {
		return nil
	}
	jobRow, err := s.client.Job.Get(ctx, jobID)
	if err != nil {
		return fmt.Errorf("load job: %w", err)
	}
	resources, err := s.client.JobResource.Query().
		Where(jobresource.HasJobWith(job.IDEQ(jobID))).
		All(ctx)
	if err != nil {
		return fmt.Errorf("load job resources: %w", err)
	}
	if len(resources) == 0 {
		return nil
	}
	// 缺失或不受支持的快照绝不被解释为默认轮次。
	type backfillRound struct {
		index int
		mode  string
	}
	rounds := make([]backfillRound, 0, 1)
	snapshot, err := GetSnapshot(jobRow)
	if err != nil {
		return err
	}
	if snapshot != nil && len(snapshot.Rounds) > 0 {
		for i, rd := range snapshot.Rounds {
			rounds = append(rounds, backfillRound{index: i, mode: rd.Mode})
		}
	}
	builders := make([]*ent.JobRoundCreate, 0, len(resources)*len(rounds))
	for _, jr := range resources {
		for _, rd := range rounds {
			builders = append(builders, s.client.JobRound.Create().
				SetJobID(jobID).
				SetJobResourceID(jr.ID).
				SetRoundIndex(rd.index).
				SetMode(rd.mode).
				SetStatus(JobRoundStatusPending))
		}
	}
	if err := bulkCreateJobRounds(ctx, builders, s.client.JobRound.CreateBulk); err != nil {
		return fmt.Errorf("create job rounds: %w", err)
	}
	return nil
}

// jobRoundBulkChunk JobRound 批量插入分片大小。
const jobRoundBulkChunk = 500

// bulkCreateJobRounds 分批执行 JobRound 插入：ent 的 CreateBulk 把全部行
// 拼进单条多行 INSERT（每行 10 个绑定列），受 SQLite 绑定变量上限 32766
// 约束（与 selection.go 的 selectionQueryChunkSize 同类）——不分片时约
// 3300 行即报 "too many SQL variables"，使创建/回填整体失败。
// createBulk 由调用方绑定各自的 client/tx。
func bulkCreateJobRounds(ctx context.Context, builders []*ent.JobRoundCreate, createBulk func(...*ent.JobRoundCreate) *ent.JobRoundCreateBulk) error {
	for start := 0; start < len(builders); start += jobRoundBulkChunk {
		end := start + jobRoundBulkChunk
		if end > len(builders) {
			end = len(builders)
		}
		if _, err := createBulk(builders[start:end]...).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

// --- JobRound 矩阵重算 ---

// jobProgressStore 抽象矩阵重算所需的最小能力，使重算既能在事务外（*ent.Client，
// Recover/Retry/Reconcile）也能在事务内（*ent.Tx，ResumeJob）执行。ent 生成的
// Client/Tx 以字段暴露实体客户端，不直接满足该接口，经下方两个薄适配器接入。
type jobProgressStore interface {
	JobRoundQuery() *ent.JobRoundQuery
	JobUpdateOneID(id int) *ent.JobUpdateOne
}

// clientProgressStore 适配事务外的 *ent.Client。
type clientProgressStore struct{ client *ent.Client }

func (a clientProgressStore) JobRoundQuery() *ent.JobRoundQuery {
	return a.client.JobRound.Query()
}

func (a clientProgressStore) JobUpdateOneID(id int) *ent.JobUpdateOne {
	return a.client.Job.UpdateOneID(id)
}

// txProgressStore 适配事务内的 *ent.Tx。
type txProgressStore struct{ tx *ent.Tx }

func (a txProgressStore) JobRoundQuery() *ent.JobRoundQuery {
	return a.tx.JobRound.Query()
}

func (a txProgressStore) JobUpdateOneID(id int) *ent.JobUpdateOne {
	return a.tx.Job.UpdateOneID(id)
}

// jobRoundProgressSums JobRound 矩阵聚合计数的扫描载体（列名经 sql tag 对齐）。
type jobRoundProgressSums struct {
	SegmentTotal     int64  `sql:"segment_total"`
	SegmentCompleted int64  `sql:"segment_completed"`
	Status           string `sql:"status"`
}

// isJobRoundClosed 报告轮次状态是否为闭合终态：completed/skipped 按定义不再有
// 待办，其已揭示工作量全额计入进度；pending/running/failed 只计实际完成量，
// 未完成部分交由重置路径续跑。
func isJobRoundClosed(status string) bool {
	return status == JobRoundStatusCompleted || status == JobRoundStatusSkipped
}

// jobRoundProgress 返回单个轮次计入 Job.progress_completed 的值——矩阵求和与
// 终态闭合增量共用的唯一口径：
//   - 闭合终态取 segment_total（定义性闭合，同时吸收「同一段被前后同模式轮
//     各揭示一次」造成的分母重复：残留段由后续同模式轮重扫）；
//   - 其余状态取 segment_completed（≡ 该轮断点集合基数）。
//
// 补齐只在读侧发生，绝不回写 segment_completed：该列是 DBReporter 的独占写入
// 面，也是恢复重跑的进度基线（StageStart 锚定断点集合基数），写入非断点派生的
// 值会让基线在重置后回退、并使 Job 缓存相对矩阵求和超计。
func jobRoundProgress(status string, segmentTotal, segmentCompleted int64) int64 {
	if isJobRoundClosed(status) && segmentTotal > segmentCompleted {
		return segmentTotal
	}
	return segmentCompleted
}

// sumJobRoundProgress 计算任务矩阵的求和口径（核心不变式）：
//
//	progress_total    = Σ segment_total   （所有 JobRound 行，无状态过滤）
//	progress_completed = Σ jobRoundProgress(status, segment_total, segment_completed)
//
// completed 侧不是裸求和 segment_completed：闭合终态（completed/skipped）按定义
// 显示为满量，读侧取 segment_total——这吸收了「同一段被前后同模式轮各揭示一次」
// 的分母重复，同时避免把补齐值回写进计数列破坏断点基线。无状态过滤是刻意的：
// fresh pending 行为 0/0、skipped 行 0/0 或满量、reset-with-history 行（resume/
// recovery/retry 后回到 pending）有意保留断点计数，该口径天然覆盖。
func sumJobRoundProgress(ctx context.Context, store jobProgressStore, jobID int) (total, completed int64, err error) {
	var rows []jobRoundProgressSums
	if err := store.JobRoundQuery().
		Where(jobround.JobIDEQ(jobID)).
		Select(jobround.FieldSegmentTotal, jobround.FieldSegmentCompleted, jobround.FieldStatus).
		Scan(ctx, &rows); err != nil {
		return 0, 0, fmt.Errorf("sum job rounds: %w", err)
	}
	for _, row := range rows {
		total += row.SegmentTotal
		completed += jobRoundProgress(row.Status, row.SegmentTotal, row.SegmentCompleted)
	}
	return total, completed, nil
}

// recomputeJobProgress 用矩阵求和口径覆盖 Job.progress_total/progress_completed
// （派生缓存）。任何 reset 路径（Resume/Recover/Retry）之后必须调用。
func recomputeJobProgress(ctx context.Context, store jobProgressStore, jobID int) error {
	switch s := store.(type) {
	case txProgressStore:
		if err := workstate.LockJob(ctx, s.tx.Client(), jobID); err != nil {
			return err
		}
		return workstate.ReconcileJob(ctx, s.tx.Client(), jobID)
	case clientProgressStore:
		return workstate.Transaction(ctx, s.client, func(tx *ent.Client) error {
			if err := workstate.LockJob(ctx, tx, jobID); err != nil {
				return err
			}
			return workstate.ReconcileJob(ctx, tx, jobID)
		})
	default:
		return fmt.Errorf("unsupported progress store")
	}
}

// --- JobRound 状态机 ---
//
// 全部为条件更新：WHERE status IN (...) 不命中（0 行受影响）视为良性 no-op。
// 并发语义由条件更新保证：DBReporter 仅在 pending/skipped→running 首次揭示时
// 累加 progress_total 分母。

// MarkJobRoundRunning 将轮次行 pending|skipped→running 并记录开始时间。
// jobID 目前仅用于签名对称（未来事件发布），更新本身按行 ID 条件执行。
func (s *JobService) MarkJobRoundRunning(ctx context.Context, jobID, roundRowID int) error {
	_, err := s.client.JobRound.Update().
		Where(
			jobround.IDEQ(roundRowID),
			jobround.StatusIn(JobRoundStatusPending, JobRoundStatusSkipped),
		).
		SetStatus(JobRoundStatusRunning).
		SetStartedAt(timeutil.NowUTC()).
		ClearFinishedAt().
		ClearErrorMessage().
		Save(ctx)
	return err
}

// MarkJobRoundCompleted 将轮次行 running→completed 并记录完成时间。
// 终态闭合是状态语义：闭合口径（completed 计满量）由读侧按状态派生，不改写
// segment_completed——该列 ≡ 断点集合基数，是 DBReporter 的独占写入面与恢复
// 重跑的进度基线。segment_completed ≤ segment_total 是矩阵不变式，因此闭合口径
// 只会向上、不超计；failed 轮不闭合，保留部分进度交由重试/恢复续跑。
func (s *JobService) MarkJobRoundCompleted(ctx context.Context, roundRowID int) error {
	return s.markJobRoundTerminal(ctx, roundRowID, []string{JobRoundStatusRunning}, JobRoundStatusCompleted)
}

// markJobRoundTerminal 在一个事务内完成轮次终态转换与任务进度缓存维护。
// 先读 job_id/status/segment_total/segment_completed 按口径计算增量，再用原状态
// 条件更新防止并发调用重复计费；状态不满足原前置条件（包括行不存在）时保持原语义，
// 即 0 行受影响、返回 nil。轮次和 Job 缓存同事务提交，避免业务结果已落库而
// 进度 flush 未落库时，终态轮永久停在未完成进度。
func (s *JobService) markJobRoundTerminal(ctx context.Context, roundRowID int, fromStatuses []string, targetStatus string) error {
	row, err := s.client.JobRound.Get(ctx, roundRowID)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return workstate.Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := workstate.LockJob(ctx, tx, row.JobID); err != nil {
			return err
		}
		return workstate.SetRoundStatus(ctx, tx, roundRowID, fromStatuses, targetStatus)
	})
}

// MarkJobRoundFailed 将轮次行 running→failed 并记录错误信息与完成时间。
func (s *JobService) MarkJobRoundFailed(ctx context.Context, roundRowID int, failure error) error {
	message := "job round failed"
	if failure != nil {
		message = failure.Error()
	}
	_, err := s.client.JobRound.Update().
		Where(
			jobround.IDEQ(roundRowID),
			jobround.StatusEQ(JobRoundStatusRunning),
		).
		SetStatus(JobRoundStatusFailed).
		SetErrorMessage(message).
		SetFinishedAt(timeutil.NowUTC()).
		Save(ctx)
	return err
}

// MarkJobRoundSkipped 将轮次行标记为 skipped 并记录完成时间（本轮无段可处理）。
// 接受 pending 与 running：runner 先 MarkJobRoundRunning 再做空段检查，
// 此时行已是 running（仅 pending 会 0 行命中且不报错，行将永久停留 running）。
// skipped 同样是闭合终态：闭合口径由读侧按状态派生（skipped 计满量），不改写
// segment_completed（该列 ≡ 断点集合基数）；矩阵不变式 segment_completed ≤
// segment_total 保证闭合口径只会向上，fresh skipped 轮 0/0 的口径为 0。
func (s *JobService) MarkJobRoundSkipped(ctx context.Context, roundRowID int) error {
	return s.markJobRoundTerminal(ctx, roundRowID,
		[]string{JobRoundStatusPending, JobRoundStatusRunning}, JobRoundStatusSkipped)
}

// GetJobRoundStatus 返回轮次行当前状态。
// 投影只取状态列，不加载断点关联行等无关数据。
func (s *JobService) GetJobRoundStatus(ctx context.Context, roundRowID int) (string, error) {
	row, err := s.client.JobRound.Query().
		Where(jobround.IDEQ(roundRowID)).
		Select(jobround.FieldStatus).
		Only(ctx)
	if err != nil {
		return "", err
	}
	return row.Status, nil
}

func (s *JobService) LoadJobExecution(ctx context.Context, jobID int) (*JobExecution, error) {
	// worker 执行路径不预载轮次行：断点恢复走 loadResolved、轮次路由走
	// loadJobRounds（各自带投影），WithRounds 全列预载会把断点 blob 读出即弃。
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
	guard, err := s.lifecycle.Lock(ctx, "translation", jobID)
	if err != nil {
		return err
	}
	defer guard.Release()
	// 条件更新：任务在入队后、执行前被并发暂停/取消时不命中（0 行受影响），
	// 返回 ErrJobNotRunnable 供 processJob 在派发资源前中止。
	n, err := s.client.Job.Update().
		Where(job.IDEQ(jobID), job.StatusIn(JobStatusPending, JobStatusRunning)).
		SetStatus(JobStatusRunning).
		ClearFinishedAt().ClearRetentionAnchorAt().
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrJobNotRunnable
	}
	s.publishEvent(jobID, "job_started", "info", "", "任务开始执行")
	return nil
}

// publishEvent 向 Broker 发布生命周期事件。broker 为 nil 时不执行任何操作。
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
		CreatedAt: timeutil.NowUTC(),
	})
}

// MarkJobStarted 记录任务开始时间。
func (s *JobService) MarkJobStarted(ctx context.Context, jobID int) error {
	now := timeutil.NowUTC()
	return s.client.Job.UpdateOneID(jobID).
		SetStartedAt(now).
		Exec(ctx)
}

// MarkJobResourceStarted 记录资源开始时间。
func (s *JobService) MarkJobResourceStarted(ctx context.Context, jobResourceID int) error {
	now := timeutil.NowUTC()
	return s.client.JobResource.UpdateOneID(jobResourceID).
		SetStartedAt(now).
		Exec(ctx)
}

func (s *JobService) MarkJobResourceRunning(ctx context.Context, jobID, jobResourceID int) error {
	// 条件更新：资源在入队后、派发前被并发取消/置终态时不命中（0 行受影响），
	// 返回 ErrJobResourceNotRunnable 供 worker 静默跳过该资源。
	n, err := s.client.JobResource.Update().
		Where(
			jobresource.IDEQ(jobResourceID),
			jobresource.StatusIn(JobResourceStatusPending, JobResourceStatusRunning),
		).
		SetStatus(JobResourceStatusRunning).
		ClearErrorMessage().
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrJobResourceNotRunnable
	}
	s.publishEvent(jobID, "resource_started", "info", "", "开始处理资源")
	return nil
}

func (s *JobService) MarkJobResourceCompleted(ctx context.Context, jobID, jobResourceID int, outputPath string, completedSegments, skippedSegments int, warning string) error {
	// 条件更新（best-effort）：允许 pending/running→completed 及对已 completed
	// 资源的幂等重写（如清除 warning）；cancelled/failed 终态不被覆盖，未命中
	// 时良性返回 nil。
	// 资源终态与轮次收敛放同一事务：两写间隙崩溃会留下 completed 资源名下
	// 的非终态轮次行，而重置/恢复路径均不会碰 completed 资源的轮——该行
	// 将成为永不执行的孤儿（已揭示 segment_total 使进度永久虚高）。
	warning = strings.TrimSpace(warning)
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := workstate.LockJob(ctx, tx.Client(), jobID); err != nil {
		return err
	}
	current, err := tx.JobResource.Get(ctx, jobResourceID)
	if err != nil {
		return err
	}
	if current.Status == JobResourceStatusPending || current.Status == JobResourceStatusRunning {
		if err := guardJobResourceSource(ctx, tx.Client(), current); err != nil {
			_ = tx.Rollback()
			if errors.Is(err, ErrSourceRevisionConflict) {
				return s.MarkJobResourceFailed(ctx, jobID, jobResourceID, err)
			}
			return err
		}
	}
	update := tx.JobResource.Update().
		Where(
			jobresource.IDEQ(jobResourceID),
			jobresource.StatusIn(JobResourceStatusPending, JobResourceStatusRunning, JobResourceStatusCompleted),
		).
		SetStatus(JobResourceStatusCompleted).
		SetOutputPath(strings.TrimSpace(outputPath)).
		SetCompletedSegments(completedSegments).
		SetSkippedSegments(skippedSegments).
		ClearErrorMessage()
	if warning != "" {
		update = update.SetWarningMessage(warning)
	} else {
		update = update.ClearWarningMessage()
	}
	n, err := update.Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	// 收敛该资源名下的异常残留 running 轮：runner 正常路径已把每个执行过的轮
	// 落终态（成功轮 completed、空段轮 skipped、失败轮 failed），能走到这里的是
	// 状态写入失败等异常残留。本收敛只保证终态化、不发明完成——按计数缺口分流：
	// 无缺口（segment_completed ≥ segment_total）说明工作确实做完、只是状态没
	// 落盘，收敛为 completed；有缺口则缺口段未完成，闭合成 completed 会把它们
	// 记成完成、且断点续传（runner 只跳过 completed|skipped）会永久跳过该轮，
	// 故收敛为 failed 并写明缺口。两分支读侧口径都不变（无缺口分支的闭合值 ==
	// segment_completed；failed 不闭合），因此不产生 Job 缓存增量。
	rounds, err := tx.JobRound.Query().
		Where(
			jobround.HasJobResourceWith(jobresource.IDEQ(jobResourceID)),
			jobround.StatusEQ(JobRoundStatusRunning),
		).
		Select(
			jobround.FieldID,
			jobround.FieldSegmentTotal,
			jobround.FieldSegmentCompleted,
		).
		All(ctx)
	if err != nil {
		return err
	}
	for _, round := range rounds {
		updateRound := tx.JobRound.Update().
			Where(
				jobround.IDEQ(round.ID),
				jobround.StatusEQ(JobRoundStatusRunning),
			).
			SetFinishedAt(timeutil.NowUTC())
		if round.SegmentCompleted >= round.SegmentTotal {
			updateRound = updateRound.SetStatus(JobRoundStatusCompleted)
		} else {
			updateRound = updateRound.
				SetStatus(JobRoundStatusFailed).
				SetErrorMessage(fmt.Sprintf("资源收尾时轮次仍未闭合，缺口段未完成（已完成 %d/%d）", round.SegmentCompleted, round.SegmentTotal))
		}
		if _, err := updateRound.Save(ctx); err != nil {
			return err
		}
	}

	if err := workstate.ReconcileJob(ctx, tx.Client(), jobID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
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
	// 条件更新（best-effort）：pending/running→failed，允许对已 failed 资源重写
	// 错误信息；cancelled 终态不被覆盖，未命中时良性返回 nil。
	_, err := s.client.JobResource.Update().
		Where(
			jobresource.IDEQ(jobResourceID),
			jobresource.StatusIn(JobResourceStatusPending, JobResourceStatusRunning, JobResourceStatusFailed),
		).
		SetStatus(JobResourceStatusFailed).
		SetErrorMessage(message).
		Save(ctx)
	if err != nil {
		return err
	}
	s.publishEvent(jobID, "resource_failed", "error", "", fmt.Sprintf("资源处理失败: %s", message))
	return nil
}

func (s *JobService) MarkJobResourceCancelled(ctx context.Context, jobID, jobResourceID int) error {
	// 条件更新（best-effort）：pending/running→cancelled；completed/failed 终态
	// 不被覆盖，未命中时良性返回 nil。
	_, err := s.client.JobResource.Update().
		Where(
			jobresource.IDEQ(jobResourceID),
			jobresource.StatusIn(JobResourceStatusPending, JobResourceStatusRunning),
		).
		SetStatus(JobResourceStatusCancelled).
		Save(ctx)
	if err != nil {
		return err
	}
	s.publishEvent(jobID, "resource_cancelled", "info", "", "资源处理取消")
	return nil
}

func (s *JobService) CancelJob(ctx context.Context, actorUserID, jobID int) (*ent.Job, error) {
	guard, err := s.lifecycle.Lock(ctx, "translation", jobID)
	if err != nil {
		return nil, err
	}
	defer guard.Release()
	current, err := s.GetJob(ctx, actorUserID, jobID)
	if err != nil {
		return nil, err
	}
	// 仅 pending/running/paused 可取消；completed/failed/cancelled 为终态，取消会篡改状态。
	if current.Status != JobStatusPending && current.Status != JobStatusRunning && current.Status != JobStatusPausing && current.Status != JobStatusPaused {
		return nil, ErrJobNotCancellable
	}
	apply := func(client *ent.Client) error {
		// Existing job controls are available to project readers, including
		// organization members. Fence revocation and project mutations without
		// raising this permission to the project-editing administrator role.
		if _, err := client.Project.Update().Where(project.IDEQ(current.ProjectID)).SetUpdatedAt(timeutil.NowUTC()).Save(ctx); err != nil {
			return err
		}
		projects := NewProjectService(client, NewUserService(client, nil))
		if _, err := projects.requireProjectAccess(ctx, actorUserID, current.ProjectID, false); err != nil {
			return err
		}
		now := timeutil.NowUTC()
		count, err := client.Job.Update().
			Where(job.IDEQ(current.ID), job.StatusIn(JobStatusPending, JobStatusRunning, JobStatusPausing, JobStatusPaused)).
			SetStatus(JobStatusCancelled).SetFinishedAt(now).SetRetentionAnchorAt(now).Save(ctx)
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrJobNotCancellable
		}
		return client.JobResource.Update().
			Where(jobresource.HasJobWith(job.IDEQ(current.ID)), jobresource.StatusIn(JobResourceStatusPending, JobResourceStatusRunning)).
			SetStatus(JobResourceStatusCancelled).
			Exec(ctx)
	}
	if orgID := EffectiveProjectOrgID(current.Edges.Project); orgID != nil {
		err = withOrganizationMutation(ctx, s.client, *orgID, apply)
	} else {
		err = withOrganizationTransaction(ctx, s.client, apply)
	}
	if err != nil {
		return nil, err
	}
	if s.cancelTask != nil {
		s.cancelTask(jobID)
	}
	s.publishEvent(jobID, "job_cancelled", "info", "", "任务已取消")
	return s.GetJob(ctx, actorUserID, current.ID)
}

// PauseResult PauseJob 的结果。NeedsDrain=true 表示任务仍在排空：服务已在
// 生命周期 guard 内通知 worker，调用方不应重复通知。worker 在持久化完成后
// 调用 MarkJobPaused；未注入任务控制器的离线调用只返回所需动作。
type PauseResult struct {
	Job        *ent.Job
	NeedsDrain bool
}

// PauseJob 优雅暂停任务。
//   - pending：直接翻转 paused（未派发、无需排空），发布 job_paused 事件；
//   - running：不改状态，返回 NeedsDrain=true 交由 worker 排空后落终态；
//   - 终态（completed/failed/cancelled/paused）：ErrJobNotPausable。
func (s *JobService) PauseJob(ctx context.Context, actorUserID, jobID int) (PauseResult, error) {
	guard, err := s.lifecycle.Lock(ctx, "translation", jobID)
	if err != nil {
		return PauseResult{}, err
	}
	defer guard.Release()
	current, err := s.GetJob(ctx, actorUserID, jobID)
	if err != nil {
		return PauseResult{}, err
	}
	if current.Status != JobStatusPending && current.Status != JobStatusRunning {
		return PauseResult{}, ErrJobNotPausable
	}
	target := JobStatusPaused
	if current.Status == JobStatusRunning {
		target = JobStatusPausing
	}
	err = workstate.Transaction(ctx, s.client, func(tx *ent.Client) error {
		n, err := tx.Job.Update().Where(job.IDEQ(jobID), job.StatusEQ(current.Status)).SetPauseRequested(true).SetStatus(target).ClearFinishedAt().ClearRetentionAnchorAt().Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrJobNotPausable
		}
		return nil
	})
	if err != nil {
		return PauseResult{}, err
	}
	// The lifecycle guard prevents a new owner from registering between saving
	// intent and closing this process's dispatch gate.
	if target == JobStatusPausing && s.pauseTask != nil {
		s.pauseTask(jobID)
	}
	current, err = s.GetJob(ctx, actorUserID, jobID)
	if err != nil {
		return PauseResult{}, err
	}
	eventName, message := "job_paused", "任务已暂停"
	if target == JobStatusPausing {
		eventName, message = "job_pausing", "任务正在暂停，等待已派发请求保存"
	}
	s.publishEvent(jobID, eventName, "info", "", message)
	s.publishStageCounts(ctx, jobID)
	return PauseResult{Job: current, NeedsDrain: target == JobStatusPausing}, nil
}

// MarkJobPaused 由 worker 在暂停排空后调用：条件翻转 running→paused 并发布
// job_paused 事件。已被并发取消/暂停时未命中（0 行受影响），良性 no-op。
func (s *JobService) MarkJobPaused(ctx context.Context, jobID int) error {
	guard, err := s.lifecycle.Lock(ctx, "translation", jobID)
	if err != nil {
		return err
	}
	defer guard.Release()
	n, err := s.client.Job.Update().
		Where(job.IDEQ(jobID), job.StatusIn(JobStatusRunning, JobStatusPausing)).
		SetStatus(JobStatusPaused).SetPauseRequested(true).
		ClearFinishedAt().ClearRetentionAnchorAt().
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	s.publishEvent(jobID, "job_paused", "info", "", "任务已暂停（已等待在途请求完成）")
	s.publishStageCounts(ctx, jobID)
	return nil
}

// ResumeJob 从轮次断点恢复已暂停的任务：仅 paused 可恢复（否则
// ErrJobNotResumable）。事务内批量条件重置——资源 running→pending、轮次
// running→pending（保留 segment_total/segment_completed 与 job_round_segments
// 断点关联行）——任务 paused→pending，随后从矩阵重算进度计数器（无条件求和）。
// 重置绝不清理断点数据，保证恢复后从断点继续、求和保持正确。
func (s *JobService) ResumeJob(ctx context.Context, actorUserID, jobID int) (*ent.Job, error) {
	guard, err := s.lifecycle.Lock(ctx, "translation", jobID)
	if err != nil {
		return nil, err
	}
	defer guard.Release()
	current, err := s.GetJob(ctx, actorUserID, jobID)
	if err != nil {
		return nil, err
	}
	if current.Status != JobStatusPaused {
		return nil, ErrJobNotResumable
	}
	if guard.Active() {
		return nil, tasklife.ErrBusy
	}
	if err := s.checkExecutionPolicies(ctx, current); err != nil {
		return nil, err
	}
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
	if err := workstate.LockJob(ctx, tx.Client(), current.ID); err != nil {
		return nil, err
	}
	for _, item := range current.Edges.JobResources {
		if item.Status != JobResourceStatusPending && item.Status != JobResourceStatusRunning {
			continue
		}
		if err := guardJobResourceSource(ctx, tx.Client(), item); err != nil {
			return nil, err
		}
	}
	if err := tx.JobResource.Update().
		Where(jobresource.HasJobWith(job.IDEQ(current.ID)), jobresource.StatusEQ(JobResourceStatusRunning)).
		SetStatus(JobResourceStatusPending).
		Exec(ctx); err != nil {
		return nil, err
	}
	// 轮次行 failed|running|skipped→pending：仅翻状态，断点字段原样保留。
	// segment_completed 是断点集合的派生缓存（DBReporter 独占写入），重置后
	// 重跑以断点集合为基线继续推进（StageStart 锚定集合基数），计数不回退。
	// skipped 是「当时无段可处理」的时点判断——暂停期间用户可经段落编辑
	// API 把段置回 pending 使其失效，重置后空段检查自然重新判定；failed
	// 来自 MarkJobRoundFailed 与 MarkJobResourceFailed 两写间隙的崩溃窗口
	//（轮落 failed 而资源留 running），runner 重跑时本就会重新执行 failed
	// 轮（只跳过 completed|skipped），重置集与其对齐避免矩阵永久失真。
	// 按「将被重跑的资源」收窄：completed/failed/cancelled 资源的轮不重置
	//（resume 只重新入队 pending/running 资源），避免制造永不执行的孤儿行。
	if err := tx.JobRound.Update().
		Where(
			jobround.HasJobWith(job.IDEQ(current.ID)),
			jobround.HasJobResourceWith(jobresource.StatusIn(JobResourceStatusPending, JobResourceStatusRunning)),
			jobround.StatusIn(JobRoundStatusFailed, JobRoundStatusRunning, JobRoundStatusSkipped),
		).
		SetStatus(JobRoundStatusPending).
		Exec(ctx); err != nil {
		return nil, err
	}
	// 最终翻转查受影响行数（与 PauseJob/MarkJobRunning 等其余状态转换一致）：
	// GetJob 与事务之间任务被并发取消（paused 可取消）时 0 行命中，回滚并
	// 拒绝——不发布恢复事件、不重新入队已取消任务。
	n, err := tx.Job.Update().
		Where(job.IDEQ(current.ID), job.StatusEQ(JobStatusPaused)).
		SetStatus(JobStatusPending).SetPauseRequested(false).
		ClearFinishedAt().ClearRetentionAnchorAt().
		Save(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrJobNotResumable
	}
	if err := recomputeJobProgress(ctx, txProgressStore{tx}, current.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	s.publishEvent(jobID, "job_resumed", "info", "", "任务已恢复")
	return s.GetJob(ctx, actorUserID, jobID)
}

func (s *JobService) RetryJob(ctx context.Context, actorUserID, jobID int) (*ent.Job, error) {
	guard, err := s.lifecycle.Lock(ctx, "translation", jobID)
	if err != nil {
		return nil, err
	}
	defer guard.Release()
	current, err := s.GetJob(ctx, actorUserID, jobID)
	if err != nil {
		return nil, err
	}
	// failed/cancelled 任务可重试；对 completed 任务重试会把它重新入队空耗 worker。
	if current.Status != JobStatusFailed && current.Status != JobStatusCancelled {
		return nil, ErrJobNotRetryable
	}
	if guard.Active() {
		return nil, tasklife.ErrBusy
	}
	// 必须存在 failed/cancelled 资源才有可重试的对象，从本次读取的资源实时计数。
	retryableResources := 0
	for _, item := range current.Edges.JobResources {
		if item.Status == JobResourceStatusFailed || item.Status == JobResourceStatusCancelled {
			retryableResources++
		}
	}
	if retryableResources == 0 {
		return nil, ErrJobNoFailedResource
	}
	if err := s.checkExecutionPolicies(ctx, current); err != nil {
		return nil, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := workstate.LockJob(ctx, tx.Client(), current.ID); err != nil {
		return nil, err
	}
	for _, item := range current.Edges.JobResources {
		if item.Status != JobResourceStatusFailed && item.Status != JobResourceStatusCancelled {
			continue
		}
		if err := guardJobResourceSource(ctx, tx.Client(), item); err != nil {
			return nil, err
		}
	}
	// 轮次行 failed|running|skipped→pending（条件更新）：保留 segment_total/
	// segment_completed 与 job_round_segments 断点关联行；completed 轮不动，重跑时
	// 按断点跳过。segment_completed 是断点集合的派生缓存（DBReporter 独占写入），
	// 重置后重跑以断点集合为基线继续推进（StageStart 锚定集合基数），计数不回退。
	// running 轮来自有未解决段的轮次与取消打断（成功分支不置
	// completed 保持 running）；skipped 是「当时无段可处理」的时点判断——
	// 失败期间用户可经段落编辑 API 把段置回 pending 使其失效，重置后空段
	// 检查会自然重新判定（无段则再次 skip，代价一次空扫描）。
	// 按「将被重跑的资源」收窄：failed/cancelled 资源之外的轮次行（如
	// completed 资源的轮）不重置，避免误翻造成矩阵与资源状态矛盾。
	// 必须先于下方资源重置执行：过滤依赖重置前的 failed/cancelled 资源状态。
	if err := tx.JobRound.Update().
		Where(
			jobround.HasJobWith(job.IDEQ(current.ID)),
			jobround.HasJobResourceWith(jobresource.StatusIn(JobResourceStatusFailed, JobResourceStatusCancelled)),
			jobround.StatusIn(JobRoundStatusFailed, JobRoundStatusRunning, JobRoundStatusSkipped),
		).
		SetStatus(JobRoundStatusPending).
		Exec(ctx); err != nil {
		return nil, err
	}
	if err := tx.JobResource.Update().
		Where(jobresource.HasJobWith(job.IDEQ(current.ID)), jobresource.StatusIn(JobResourceStatusFailed, JobResourceStatusCancelled)).
		SetStatus(JobResourceStatusPending).
		SetSkippedSegments(0).
		ClearErrorMessage().
		ClearWarningMessage().
		Exec(ctx); err != nil {
		return nil, err
	}
	n, err := tx.Job.Update().Where(job.IDEQ(current.ID), job.StatusEQ(current.Status)).
		SetStatus(JobStatusPending).SetPauseRequested(false).AddRetryEpoch(1).
		ClearFinishedAt().ClearRetentionAnchorAt().
		SetFailedResources(0).
		ClearErrorMessage().
		Save(ctx)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrJobNotRetryable
	}
	// 从矩阵重算进度计数器（无条件求和）：reset 保留计数，求和天然一致。
	if err := resetWorkEpoch(ctx, tx.Client(), current.ID, current.RetryEpoch+1); err != nil {
		return nil, err
	}
	if err := recomputeJobProgress(ctx, txProgressStore{tx}, current.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetJob(ctx, actorUserID, current.ID)
}

func (s *JobService) ReconcileJob(ctx context.Context, jobID int) error {
	guard, err := s.lifecycle.Lock(ctx, "translation", jobID)
	if err != nil {
		return err
	}
	defer guard.Release()
	var finalStatus, firstFailure string
	var warnings []string
	var notify bool
	err = workstate.Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := workstate.LockJob(ctx, tx, jobID); err != nil {
			return err
		}
		current, err := tx.Job.Query().Where(job.IDEQ(jobID)).WithJobResources().Only(ctx)
		if ent.IsNotFound(err) {
			return ErrJobNotFound
		}
		if err != nil {
			return err
		}
		var pending, running, completed, failed, cancelled int
		warnings = nil
		firstFailure = ""
		for _, item := range current.Edges.JobResources {
			switch item.Status {
			case JobResourceStatusPending:
				pending++
			case JobResourceStatusRunning:
				running++
			case JobResourceStatusCompleted:
				completed++
			case JobResourceStatusCancelled:
				cancelled++
			default:
				failed++
				if firstFailure == "" && item.ErrorMessage != nil {
					firstFailure = *item.ErrorMessage
				}
			}
			if item.WarningMessage != nil && strings.TrimSpace(*item.WarningMessage) != "" {
				warnings = append(warnings, *item.WarningMessage)
			}
		}
		status := deriveJobStatus(len(current.Edges.JobResources), pending, running, completed, failed, cancelled)
		wasTerminal := isJobTerminal(current.Status)
		if wasTerminal || current.Status == JobStatusPaused || current.Status == JobStatusPausing || current.PauseRequested {
			status = current.Status
		}
		update := tx.Job.UpdateOneID(jobID).SetStatus(status).SetResourceCount(len(current.Edges.JobResources)).SetCompletedResources(completed).SetFailedResources(failed)
		if !wasTerminal && isJobTerminal(status) {
			now := timeutil.NowUTC()
			update.SetFinishedAt(now).SetRetentionAnchorAt(now)
		}
		if !wasTerminal {
			if firstFailure != "" && status == JobStatusFailed {
				update.SetErrorMessage(firstFailure)
			} else {
				update.ClearErrorMessage()
			}
		}
		if err := update.Exec(ctx); err != nil {
			return err
		}
		if err := workstate.ReconcileJob(ctx, tx, jobID); err != nil {
			return err
		}
		finalStatus = status
		notify = !wasTerminal
		return nil
	})
	if err != nil {
		return err
	}
	if !notify {
		return nil
	}
	switch finalStatus {
	case JobStatusCompleted:
		s.publishEvent(jobID, "job_completed", "info", "", "任务完成")
		if len(warnings) > 0 {
			s.publishEvent(jobID, "job_warning", "warning", "", strings.Join(warnings, "; "))
		}
	case JobStatusFailed:
		if firstFailure == "" {
			firstFailure = "任务失败"
		}
		s.publishEvent(jobID, "job_failed", "error", "", firstFailure)
	case JobStatusCancelled:
		s.publishEvent(jobID, "job_cancelled", "info", "", "任务已取消")
	}
	return nil
}

func isJobTerminal(status string) bool {
	return status == JobStatusCompleted || status == JobStatusFailed || status == JobStatusCancelled
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
			q.WithResource().WithRounds(func(rq *ent.JobRoundQuery) {
				// 详情视图不消费断点数据（jobRoundResponse 不含该字段），
				// 投影只取响应所需列——断点存于 job_round_segments
				// 关联行，本接口是前端轮询主路径，避免逐轮加载关联数据。
				rq.Select(
					jobround.FieldID,
					jobround.FieldRoundIndex,
					jobround.FieldMode,
					jobround.FieldStatus,
					jobround.FieldSegmentTotal,
					jobround.FieldSegmentCompleted,
					jobround.FieldErrorMessage,
					jobround.FieldStartedAt,
					jobround.FieldFinishedAt,
				)
				rq.Order(ent.Asc(jobround.FieldRoundIndex))
			}).Order(ent.Asc(jobresource.FieldID))
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

// GetSnapshot 从 Job 的 ExecutionConfig 字段解析快照。
func GetSnapshot(job *ent.Job) (*JobExecutionSnapshot, error) {
	if job == nil || len(job.ExecutionConfig) == 0 {
		return nil, errors.New("missing execution snapshot")
	}
	raw, err := json.Marshal(job.ExecutionConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal execution config: %w", err)
	}
	var snap JobExecutionSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	if err := execution.ValidateSpec(&snap); err != nil {
		return nil, err
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
