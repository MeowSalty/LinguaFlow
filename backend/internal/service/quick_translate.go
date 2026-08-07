package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/preview"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

var (
	ErrQuickTranslateBusy        = errors.New("quick translate: concurrency limit reached")
	ErrQuickTranslateNoTranslate = errors.New("quick translate: execution plan has no translate round")
)

// QuickGlossaryEntryInput 即时翻译请求中的一条内联临时术语表条目。
// 纯内存，翻译完即丢，绝不落库。
type QuickGlossaryEntryInput struct {
	Source        string
	Target        string
	CaseSensitive bool
	Forbidden     bool
	Mandatory     bool
	Notes         string
}

// QuickTranslateInput 即时翻译的输入参数。
type QuickTranslateInput struct {
	ActorUserID     int
	SourceText      string
	SourceLang      string
	TargetLang      string
	ExecutionPlanID int
	ProjectID       *int // nil = 无项目（纯即时翻译）
	Glossary        []QuickGlossaryEntryInput
}

// QuickTranslateOutput 即时翻译的输出结果。
type QuickTranslateOutput struct {
	Status        string // "success" | "partial" | "failed"
	SourceText    string
	TargetText    string
	SourceLang    string
	TargetLang    string
	QualityIssues []qa.QualityIssue
	RoundSummary  []PreviewRoundSummary
	BatchEvents   []progress.BatchEvent
	Usage         UsageSummary
	Warnings      []string
}

// QuickTranslateRunnerInput 是 runner 的输入：已冻结的快照与已构建好的术语表。
// 由 service 组装，runner 不再做授权/解析。
type QuickTranslateRunnerInput struct {
	Snapshot   *JobExecutionSnapshot
	SourceLang string
	TargetLang string
	SourceText string
	Glossary   glossary.Glossary
	Format     string
}

// QuickTranslateResult 是 runner 返回的同步执行结果（纯内存，不写 DB）。
type QuickTranslateResult struct {
	Status        string
	SourceText    string
	TargetText    string
	QualityIssues []qa.QualityIssue
	RoundSummary  []PreviewRoundSummary
	Metrics       []backend.MeterMetrics
	Collector     *preview.MemoryCollector
	Warnings      []string
}

// QuickTranslateRunner 是执行即时翻译的接口，由 worker.QuickTranslateRunner 实现。
type QuickTranslateRunner interface {
	Run(ctx context.Context, in QuickTranslateRunnerInput) (*QuickTranslateResult, error)
}

// QuickTranslateService 编排即时翻译：授权、快照、术语表构建、执行、用量与审计。
type QuickTranslateService struct {
	logger         *slog.Logger
	client         *ent.Client
	projects       *ProjectService
	jobs           *JobService
	backends       *BackendService
	executionPlans *ExecutionPlanService
	audit          *AuditService
	runner         QuickTranslateRunner
	maxConcurrency int
	timeout        time.Duration

	// 全局并发上限：所有 actor 共享，避免 (活跃 actor 数 × maxConcurrency) 击穿
	// 后端 AI 速率/成本预算。容量由 NewQuickTranslateService 设置。
	globalSema chan struct{}

	mu      sync.Mutex
	semas   map[int]*actorSemaphore // per-actor 信号量，避免单用户耗尽全局限流
	metrics int                     // 当前在用 actor 条目数，用于诊断
}

// actorSemaphore 是某 actor 的 per-actor 信号量及其在用计数。
// inUse == 0 时该条目可被回收，避免 map 随用户流转单调增长。
type actorSemaphore struct {
	ch    chan struct{}
	inUse int
}

// NewQuickTranslateService 创建 QuickTranslateService。
func NewQuickTranslateService(
	logger *slog.Logger, client *ent.Client,
	projects *ProjectService, jobs *JobService, backends *BackendService,
	executionPlans *ExecutionPlanService, audit *AuditService,
	runner QuickTranslateRunner,
	maxConcurrency int, timeout time.Duration,
) *QuickTranslateService {
	if logger == nil {
		logger = slog.Default()
	}
	if maxConcurrency <= 0 {
		maxConcurrency = 2
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	// 全局并发上限 = 4 × per-actor 上限，既保留 per-actor 隔离，又给总并发一个上界。
	globalCap := maxConcurrency * 4
	return &QuickTranslateService{
		logger:         logger,
		client:         client,
		projects:       projects,
		jobs:           jobs,
		backends:       backends,
		executionPlans: executionPlans,
		audit:          audit,
		runner:         runner,
		maxConcurrency: maxConcurrency,
		timeout:        timeout,
		globalSema:     make(chan struct{}, globalCap),
		semas:          make(map[int]*actorSemaphore),
	}
}

// acquirePerActor 占用 actor 的一个槽位，返回该信号量；占用失败返回 nil。
func (s *QuickTranslateService) acquirePerActor(actorUserID int) *actorSemaphore {
	s.mu.Lock()
	defer s.mu.Unlock()
	as, ok := s.semas[actorUserID]
	if !ok {
		as = &actorSemaphore{ch: make(chan struct{}, s.maxConcurrency)}
		s.semas[actorUserID] = as
	}
	select {
	case as.ch <- struct{}{}:
		as.inUse++
		return as
	default:
		return nil
	}
}

// releasePerActor 释放 actor 的一个槽位；若该 actor 已无在用请求则回收 map 条目，
// 避免 map 随用户流转单调增长。
func (s *QuickTranslateService) releasePerActor(actorUserID int, as *actorSemaphore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	<-as.ch
	as.inUse--
	if as.inUse <= 0 {
		delete(s.semas, actorUserID)
	}
}

// Translate 执行一次即时翻译。译文与术语表纯临时，绝不落库；
// 用量记录与审计日志仍正常写入。
func (s *QuickTranslateService) Translate(ctx context.Context, in QuickTranslateInput) (*QuickTranslateOutput, error) {
	// 1. Acquire per-actor slot, then a global slot. Global cap bounds the
	// total concurrent AI calls (#actors × maxConcurrency) regardless of how
	// many distinct actors are active.
	as := s.acquirePerActor(in.ActorUserID)
	if as == nil {
		return nil, ErrQuickTranslateBusy
	}
	defer s.releasePerActor(in.ActorUserID, as)

	select {
	case s.globalSema <- struct{}{}:
	default:
		return nil, ErrQuickTranslateBusy
	}
	defer func() { <-s.globalSema }()

	// 2. Validate source text.
	if strings.TrimSpace(in.SourceText) == "" {
		return nil, ErrInvalidInput
	}

	// 3. Apply quick translate timeout.
	runCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// 4. Determine source/target languages and glossary base flag.
	sourceLang, targetLang := in.SourceLang, in.TargetLang
	var projectRow *ent.Project
	var glossaryBaseEnabled bool
	if in.ProjectID != nil {
		// 有项目：校验读访问，语言缺省值取项目配置，术语表以项目 DB 为基底。
		var err error
		projectRow, err = s.projects.requireProjectAccess(runCtx, in.ActorUserID, *in.ProjectID, false)
		if err != nil {
			return nil, fmt.Errorf("quick translate: project access: %w", err)
		}
		if sourceLang == "" {
			sourceLang = projectRow.SourceLang
		}
		if targetLang == "" {
			targetLang = projectRow.TargetLang
		}
		glossaryBaseEnabled = projectRow.GlossaryEnabled
	} else {
		// 无项目：纯即时翻译，语言缺省 auto/zh，术语表仅来自请求体。
		if sourceLang == "" {
			sourceLang = "auto"
		}
		if targetLang == "" {
			targetLang = "zh"
		}
		glossaryBaseEnabled = false
	}

	// 5. Build the frozen execution snapshot. Backend authorization is done by
	// prepareExecutionSnapshotForActor: with a project it reuses the project-level
	// validateBackendAccess (same semantics as job/preview); without a project it
	// authorizes against the actor's own identity.
	snapshot, err := s.jobs.prepareExecutionSnapshotForActor(runCtx, in.ActorUserID, in.ExecutionPlanID, "", sourceLang, targetLang, glossaryBaseEnabled, projectRow)
	if err != nil {
		return nil, err
	}

	// 6. Reject plans without a translate round.
	hasTranslate := false
	for _, rs := range snapshot.Rounds {
		if rs.Mode == "translate" {
			hasTranslate = true
			break
		}
	}
	if !hasTranslate {
		return nil, ErrQuickTranslateNoTranslate
	}

	// 7. Force glossary enabled when there is an extract round or inline
	// glossary provided, so the engine actually applies them.
	hasExtract := false
	for _, rs := range snapshot.Rounds {
		if rs.Mode == "extract" {
			hasExtract = true
			break
		}
	}
	if hasExtract || len(in.Glossary) > 0 {
		snapshot.GlossaryEnabled = true
	}

	// 8. Build the runtime glossary. Guaranteed in-memory — never persisted.
	// Project DB glossary is wrapped in OverlayGlossary so extract-round Add
	// stays in memory.
	var runtimeGlossary glossary.Glossary = glossary.Nop{}
	var warnings []string
	if snapshot.GlossaryEnabled {
		if projectRow != nil {
			dbGloss, err := NewDatabaseGlossary(runCtx, s.client, projectRow)
			if err != nil {
				s.logger.Warn("quick translate: failed to load database glossary", "err", err)
				runtimeGlossary = preview.NewOverlayGlossary(nil)
			} else {
				runtimeGlossary = preview.NewOverlayGlossary(dbGloss)
			}
		} else {
			runtimeGlossary = preview.NewOverlayGlossary(nil)
		}
		if len(in.Glossary) > 0 {
			entries := make([]glossary.Entry, 0, len(in.Glossary))
			for _, g := range in.Glossary {
				entries = append(entries, glossary.Entry{
					Source:        g.Source,
					Target:        g.Target,
					CaseSensitive: g.CaseSensitive,
					Forbidden:     g.Forbidden,
					Mandatory:     g.Mandatory,
					Notes:         g.Notes,
				})
			}
			addResult, addErr := runtimeGlossary.Add(runCtx, entries...)
			if addErr != nil {
				s.logger.Warn("quick translate: inline glossary add failed", "err", addErr)
			}
			if len(addResult.Skipped) > 0 {
				warnings = append(warnings, fmt.Sprintf("内联术语表跳过 %d 条（source/target 为空或冲突）", len(addResult.Skipped)))
			}
		}
	}

	// 9. Execute.
	result, err := s.runner.Run(runCtx, QuickTranslateRunnerInput{
		Snapshot:   snapshot,
		SourceLang: sourceLang,
		TargetLang: targetLang,
		SourceText: in.SourceText,
		Glossary:   runtimeGlossary,
		Format:     "txt",
	})
	if err != nil {
		return nil, fmt.Errorf("quick translate: execute: %w", err)
	}
	warnings = append(warnings, result.Warnings...)

	// 10. Record usage (best-effort, do not block return).
	usageMetrics := aggregateMetrics(result.Metrics)
	if usageMetrics.APICalls > 0 {
		usageCtx, usageCancel := context.WithTimeout(context.WithoutCancel(runCtx), 5*time.Second)
		if err := s.recordQuickUsage(usageCtx, in, projectRow, usageMetrics); err != nil {
			s.logger.Warn("quick translate: failed to record usage", "err", err)
			warnings = append(warnings, "即时翻译用量记录失败")
		}
		usageCancel()
	}

	// 11. Record audit event (best-effort, bounded context — held under the semaphore slot).
	if s.audit != nil {
		metadata := map[string]any{"execution_plan_id": in.ExecutionPlanID}
		var projectIDPtr *int
		if in.ProjectID != nil {
			p := *in.ProjectID
			projectIDPtr = &p
			metadata["project_id"] = p
		}
		auditEvent := AuditEvent{
			ActorUserID:  in.ActorUserID,
			ProjectID:    projectIDPtr,
			Action:       "quick_translate",
			ResourceType: "quick_translate",
			Message:      fmt.Sprintf("Instant translate (plan=%d)", in.ExecutionPlanID),
			Metadata:     metadata,
		}
		if projectRow != nil && projectRow.OwnerOrgID != nil {
			auditEvent.OrgID = projectRow.OwnerOrgID
		}
		auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(runCtx), 5*time.Second)
		if err := s.audit.Record(auditCtx, auditEvent); err != nil {
			s.logger.Warn("quick translate: audit record failed", "err", err)
		}
		auditCancel()
	}

	// 12. Collect batch diagnostics.
	var events []progress.BatchEvent
	if result.Collector != nil {
		events = result.Collector.Events()
	}

	// 13. Return.
	return &QuickTranslateOutput{
		Status:        result.Status,
		SourceText:    result.SourceText,
		TargetText:    result.TargetText,
		SourceLang:    sourceLang,
		TargetLang:    targetLang,
		QualityIssues: result.QualityIssues,
		RoundSummary:  result.RoundSummary,
		BatchEvents:   events,
		Usage: UsageSummary{
			APICalls:     usageMetrics.APICalls,
			InputTokens:  usageMetrics.InputTokens,
			OutputTokens: usageMetrics.OutputTokens,
		},
		Warnings: warnings,
	}, nil
}

// recordQuickUsage 记录即时翻译的用量（best-effort）。有项目时带 ProjectID 与
// OrganizationID，无项目时均留空。
func (s *QuickTranslateService) recordQuickUsage(ctx context.Context, in QuickTranslateInput, projectRow *ent.Project, metrics backend.MeterMetrics) error {
	usage := s.client.UsageRecord.Create().
		SetSource("quick_translate").
		SetSegmentCount(1).
		SetAPICalls(clampInt64ToInt(metrics.APICalls)).
		SetInputTokens(clampInt64ToInt(metrics.InputTokens)).
		SetOutputTokens(clampInt64ToInt(metrics.OutputTokens)).
		SetNote(fmt.Sprintf("quick_translate:plan=%d", in.ExecutionPlanID))
	if in.ActorUserID > 0 {
		usage.SetUserID(in.ActorUserID)
	}
	if projectRow != nil {
		usage.SetProjectID(*in.ProjectID)
		if projectRow.OwnerOrgID != nil {
			usage.SetOrganizationID(*projectRow.OwnerOrgID)
		}
	}
	return usage.Exec(ctx)
}
