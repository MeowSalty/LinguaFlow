package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/preview"
	"github.com/MeowSalty/LinguaFlow/backend/internal/previewtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

var (
	// ErrRevisionPreviewBusy 表示修订预览已达到全局并发上限。
	ErrRevisionPreviewBusy = errors.New("revision preview: concurrency limit reached")
	// ErrRevisionNoTarget 表示段落没有可供修订的译文，或状态不允许修订。
	ErrRevisionNoTarget = errors.New("revision preview: segment has no revision target")
	// ErrRevisionNoIssues 表示过滤后没有待修复的语义质量问题。
	ErrRevisionNoIssues = errors.New("revision preview: segment has no pending semantic issues")
	// ErrRevisionNoBackend 表示执行计划没有可用于修订的后端。
	ErrRevisionNoBackend = errors.New("revision preview: execution plan has no revision backend")
	// ErrRevisionInvalidIssueCodes 表示请求中的 issue code 不是语义质检 code。
	ErrRevisionInvalidIssueCodes = errors.New("revision preview: invalid issue code")
)

// RevisionPreviewInput 是单段交互式修订预览的输入。
type RevisionPreviewInput struct {
	ActorUserID     int
	ProjectID       int
	ResourceID      int
	SegmentID       int
	ExecutionPlanID int
	// IssueCodes 为空表示不收窄，使用段落上全部 pending 语义问题。
	IssueCodes []string
}

// RevisionRoundSummary 是修订预览实际执行轮次的摘要。
type RevisionRoundSummary struct {
	Index       int
	Mode        string
	Backend     string
	Synthesized bool
	Status      string
}

// RevisionPreviewResult 是 RevisionPreviewRunner 返回的内存结果。
type RevisionPreviewResult struct {
	Status        string
	SegmentID     int
	SourceText    string
	TargetText    string
	QualityIssues []qa.QualityIssue
	Metrics       []backend.MeterMetrics
	Collector     *preview.MemoryCollector
	RoundSummary  []RevisionRoundSummary
	Warnings      []string
}

// RevisionPreviewRunner 执行不落库的单段修订轮。
// 由 worker 包实现；接口定义在使用方以保持服务层与执行层解耦。
type RevisionPreviewRunner interface {
	RunRevisionPreview(
		ctx context.Context,
		snapshot *JobExecutionSnapshot,
		projectRow *ent.Project,
		resourceRow *ent.Resource,
		allSegments []*ent.Segment,
		targetSegmentIdx int,
		qaConfig qa.Config,
		repairOpts repair.Options,
		synthesized bool,
	) (*RevisionPreviewResult, error)
}

// RevisionPreviewOutput 是修订预览 API 的服务层输出。
type RevisionPreviewOutput struct {
	Status             string
	SegmentID          int
	SourceText         string
	OriginalTargetText string
	TargetText         string
	FixIssues          []qa.QualityIssue
	QualityIssues      []qa.QualityIssue
	Snapshot           *JobExecutionSnapshot
	ApplyToken         string
	ApplyExpiresAt     time.Time
	Warnings           []string
	RoundSummary       []RevisionRoundSummary
	Usage              UsageSummary
	BatchEvents        []progress.BatchEvent
}

// RevisionPreviewService 编排单段交互式修订预览。
type RevisionPreviewService struct {
	logger        *slog.Logger
	client        *ent.Client
	projects      *ProjectService
	jobs          *JobService
	previewRunner RevisionPreviewRunner
	tokenCodec    *previewtoken.Codec
	semaphore     chan struct{}
	timeout       time.Duration
}

// NewRevisionPreviewService 创建修订预览服务。semaphore 应与 PreviewService 共用。
func NewRevisionPreviewService(
	logger *slog.Logger,
	client *ent.Client,
	projects *ProjectService,
	jobs *JobService,
	previewRunner RevisionPreviewRunner,
	jwtSecret string,
	tokenTTL time.Duration,
	maxConcurrency int,
	timeout time.Duration,
	semaphore chan struct{},
) *RevisionPreviewService {
	if logger == nil {
		logger = slog.Default()
	}
	if semaphore == nil {
		semaphore = NewPreviewSemaphore(maxConcurrency)
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if tokenTTL <= 0 {
		tokenTTL = 15 * time.Minute
	}
	return &RevisionPreviewService{
		logger:        logger,
		client:        client,
		projects:      projects,
		jobs:          jobs,
		previewRunner: previewRunner,
		tokenCodec:    previewtoken.NewCodec(jwtSecret, tokenTTL),
		semaphore:     semaphore,
		timeout:       timeout,
	}
}

// RunRevisionPreview validates the segment and execution plan, runs one revise
// round against an in-memory resource snapshot, and issues an apply token.
func (s *RevisionPreviewService) RunRevisionPreview(ctx context.Context, input RevisionPreviewInput) (*RevisionPreviewOutput, error) {
	select {
	case s.semaphore <- struct{}{}:
	default:
		return nil, ErrRevisionPreviewBusy
	}
	defer func() { <-s.semaphore }()

	previewCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	projectRow, err := s.projects.requireProjectAccess(previewCtx, input.ActorUserID, input.ProjectID, true)
	if err != nil {
		return nil, fmt.Errorf("revision preview: project access: %w", err)
	}

	segRow, err := s.client.Segment.Query().
		Where(segment.IDEQ(input.SegmentID), segment.ResourceIDEQ(input.ResourceID)).
		Only(previewCtx)
	if err != nil {
		return nil, ErrSegmentNotFound
	}
	resRow, err := s.client.Resource.Get(previewCtx, input.ResourceID)
	if err != nil || resRow.ProjectID == nil || *resRow.ProjectID != input.ProjectID {
		return nil, ErrResourceNotFound
	}

	if (segRow.Status != SegmentStatusTranslated && segRow.Status != SegmentStatusEdited) ||
		segRow.TargetText == nil || strings.TrimSpace(*segRow.TargetText) == "" {
		return nil, ErrRevisionNoTarget
	}

	requested := make(map[string]struct{}, len(input.IssueCodes))
	for _, code := range input.IssueCodes {
		if !qa.IsSemanticQACode(code) {
			return nil, fmt.Errorf("%w: %s", ErrRevisionInvalidIssueCodes, code)
		}
		requested[code] = struct{}{}
	}
	fixIssues := make([]qa.QualityIssue, 0, len(segRow.QualityIssues))
	for _, issue := range segRow.QualityIssues {
		if !issue.IsPending() || !qa.IsSemanticQACode(issue.Code) {
			continue
		}
		if len(requested) > 0 {
			if _, ok := requested[issue.Code]; !ok {
				continue
			}
		}
		fixIssues = append(fixIssues, issue)
	}
	if len(fixIssues) == 0 {
		return nil, ErrRevisionNoIssues
	}

	snapshot, err := s.jobs.prepareExecutionSnapshot(previewCtx, input.ActorUserID, projectRow, input.ExecutionPlanID, "")
	if err != nil {
		return nil, err
	}

	revisionRound, synthesized, err := revisionRoundFromSnapshot(snapshot, fixIssues)
	if err != nil {
		return nil, err
	}
	executionSnapshot := *snapshot
	executionSnapshot.Rounds = []JobRoundSnapshot{revisionRound}
	executionSnapshot.AutoApprove = false
	executionSnapshot.ExplicitSegmentSelection = true

	allSegments, err := s.client.Segment.Query().
		Where(segment.ResourceIDEQ(input.ResourceID)).
		Order(segment.BySegmentIndex(), segment.ByID()).
		All(previewCtx)
	if err != nil {
		return nil, fmt.Errorf("revision preview: load segments: %w", err)
	}
	targetSegmentIdx := -1
	for i, row := range allSegments {
		if row.ID == input.SegmentID {
			targetSegmentIdx = i
			break
		}
	}
	if targetSegmentIdx < 0 {
		return nil, ErrSegmentNotFound
	}

	qaClaims := qaConfigFromSnapshot(snapshot, resRow.Format)
	qaConfig := qa.Config{
		Enabled:        qaClaims.Enabled,
		Checks:         qaClaims.Checks,
		LengthMethod:   qa.LengthMethod(qaClaims.LengthMethod),
		LengthRatioMin: qaClaims.LengthRatioMin,
		LengthRatioMax: qaClaims.LengthRatioMax,
		SourceLang:     qaClaims.SourceLang,
		TargetLang:     qaClaims.TargetLang,
		Format:         qaClaims.Format,
	}
	// 执行 snapshot 只含单个 revise 轮，worker 侧的 BuildEngineConfig 无法再从
	// translate 轮派生 Repair，需在截断前从完整 snapshot 抢救（与 QA 同法），
	// 否则预览的响应修复行为会与同一计划的真实作业分叉。
	repairOpts := repairOptionsFromSnapshot(snapshot)
	result, err := s.previewRunner.RunRevisionPreview(previewCtx, &executionSnapshot, projectRow, resRow, allSegments, targetSegmentIdx, qaConfig, repairOpts, synthesized)
	if err != nil {
		return nil, fmt.Errorf("revision preview: execute: %w", err)
	}
	if result == nil {
		return nil, errors.New("revision preview: runner returned nil result")
	}

	usageMetrics := aggregateMetrics(result.Metrics)
	if usageMetrics.APICalls > 0 {
		usageCtx, usageCancel := context.WithTimeout(context.WithoutCancel(previewCtx), 5*time.Second)
		usageInput := PreviewInput{ActorUserID: input.ActorUserID, ProjectID: input.ProjectID, ResourceID: input.ResourceID, SegmentID: input.SegmentID, ExecutionPlanID: input.ExecutionPlanID}
		if err := s.recordUsage(usageCtx, usageInput, projectRow, usageMetrics); err != nil {
			s.logger.Warn("revision preview: failed to record usage", "err", err)
			result.Warnings = append(result.Warnings, "预览用量记录失败")
		}
		usageCancel()
	}

	var applyToken string
	var applyExpiresAt time.Time
	// failed 与 partial 轮的 TargetText 都可能是回退的原译文（非空）：渲染失败经
	// preserveResult 写回原文、重试耗尽落入 unresolved 不回调、no-op 修订被批处理
	// 器跳过写回。应用"未产生修订"的令牌会把原译文盖上 edited/reviewed_by 戳并
	// 清空 review_comment，因此除状态外还必须确认译文发生了实质变化。
	if result.TargetText != "" && result.Status != "failed" && sha256Hex(result.TargetText) != sha256Hex(*segRow.TargetText) {
		claims := previewtoken.ApplyClaims{
			ActorUserID:     input.ActorUserID,
			ProjectID:       input.ProjectID,
			ResourceID:      input.ResourceID,
			SegmentID:       input.SegmentID,
			ExecutionPlanID: input.ExecutionPlanID,
			Kind:            previewtoken.KindRevision,
			SourceHash:      sha256Hex(segRow.SourceText),
			PreviewSource:   segRow.SourceText,
			TargetHash:      sha256Hex(result.TargetText),
			BaselineSource:  segRow.SourceText,
			BaselineTarget:  segRow.TargetText,
			BaselineStatus:  string(segRow.Status),
			FinalIssues:     result.QualityIssues,
			// 声明已修复的 code 集合与喂给 LLM 的修复目标同源（均为请求交集收窄
			// 后的 Revise.IssueCodes），apply 改写文本时按同一契约剔除命中 pending。
			ResolvedCodes: revisionRound.Revise.IssueCodes,
			QAConfig:      qaClaims,
		}
		applyToken, applyExpiresAt, err = s.tokenCodec.Encode(claims)
		if err != nil {
			s.logger.Warn("revision preview: failed to encode apply token", "err", err)
		}
	}

	var events []progress.BatchEvent
	if result.Collector != nil {
		events = result.Collector.Events()
	}
	return &RevisionPreviewOutput{
		Status:             result.Status,
		SegmentID:          result.SegmentID,
		SourceText:         segRow.SourceText,
		OriginalTargetText: *segRow.TargetText,
		TargetText:         result.TargetText,
		FixIssues:          fixIssues,
		QualityIssues:      result.QualityIssues,
		Snapshot:           &executionSnapshot,
		ApplyToken:         applyToken,
		ApplyExpiresAt:     applyExpiresAt,
		Warnings:           result.Warnings,
		RoundSummary:       result.RoundSummary,
		Usage:              UsageSummary{APICalls: usageMetrics.APICalls, InputTokens: usageMetrics.InputTokens, OutputTokens: usageMetrics.OutputTokens},
		BatchEvents:        events,
	}, nil
}

func (s *RevisionPreviewService) recordUsage(ctx context.Context, input PreviewInput, projectRow *ent.Project, metrics backend.MeterMetrics) error {
	usage := s.client.UsageRecord.Create().
		SetProjectID(input.ProjectID).
		SetSource("preview").
		SetSegmentCount(1).
		SetAPICalls(clampInt64ToInt(metrics.APICalls)).
		SetInputTokens(clampInt64ToInt(metrics.InputTokens)).
		SetOutputTokens(clampInt64ToInt(metrics.OutputTokens)).
		SetNote(fmt.Sprintf("preview:plan=%d,segment=%d", input.ExecutionPlanID, input.SegmentID))
	if input.ActorUserID > 0 {
		usage.SetUserID(input.ActorUserID)
	}
	if projectRow.OwnerOrgID != nil {
		usage.SetOrganizationID(*projectRow.OwnerOrgID)
	}
	return usage.Exec(ctx)
}

func revisionRoundFromSnapshot(snapshot *JobExecutionSnapshot, fixIssues []qa.QualityIssue) (JobRoundSnapshot, bool, error) {
	codes := make([]string, 0, len(fixIssues))
	seen := make(map[string]struct{}, len(fixIssues))
	for _, issue := range fixIssues {
		if _, ok := seen[issue.Code]; !ok {
			seen[issue.Code] = struct{}{}
			codes = append(codes, issue.Code)
		}
	}
	for _, round := range snapshot.Rounds {
		if round.Mode == "revise" && round.Revise != nil {
			round.Revise = cloneReviseSnapshot(round.Revise)
			round.Revise.IssueCodes = codes
			fillReviseBorrowedStrategy(round.Revise, snapshot)
			return round, false, nil
		}
	}
	for i := len(snapshot.Rounds) - 1; i >= 0; i-- {
		if snapshot.Rounds[i].Mode != "translate" {
			continue
		}
		revise := &JobReviseRoundSnapshot{
			// 合成轮采用固定的低并发、适中批量与默认重试参数：
			// 这些值与交互式修订的成本/响应时间平衡约定一致；IssueCodes
			// 与已配置 revise 轮同样收窄为请求交集，保持一致的修复目标语义。
			BatchSize:        20,
			MaxWordsPerBatch: 0,
			Concurrency:      1,
			SegmentScope:     "with_issues",
			IssueCodes:       append([]string(nil), codes...),
			Retry:            schema.RetryConfig{MaxAttempts: 3, BackoffMs: 2000, Jitter: true},
		}
		fillReviseBorrowedStrategy(revise, snapshot)
		return JobRoundSnapshot{
			Mode:    "revise",
			Backend: snapshot.Rounds[i].Backend,
			Revise:  revise,
		}, true, nil
	}
	return JobRoundSnapshot{}, false, ErrRevisionNoBackend
}

// fillReviseBorrowedStrategy 把从完整快照借用的 protect/ruby 策略物化进修订轮
// 快照。快照随后被裁剪为单 revise 轮，worker 的工厂级借用（扫 translate 轮）在
// 裁剪后必然落空；预物化使单轮预览与真实 revise 轮的输入保护行为一致。
func fillReviseBorrowedStrategy(revise *JobReviseRoundSnapshot, snapshot *JobExecutionSnapshot) {
	revise.ProtectRules, revise.RubyEnabled, revise.RubyPreserveKinds = BorrowTranslateProtectRuby(snapshot)
}

func cloneReviseSnapshot(in *JobReviseRoundSnapshot) *JobReviseRoundSnapshot {
	out := *in
	out.IssueCodes = append([]string(nil), in.IssueCodes...)
	return &out
}

// repairOptionsFromSnapshot 从计划内首个 translate 轮的策略派生修复选项，
// 供修订预览在截断轮次前抢救 Repair 配置。
func repairOptionsFromSnapshot(snapshot *JobExecutionSnapshot) repair.Options {
	for _, rs := range snapshot.Rounds {
		if rs.Mode != "translate" || rs.Translate == nil {
			continue
		}
		return RepairOptionsFromStrategy(rs.Translate.Strategy)
	}
	return repair.Options{}
}

// RepairOptionsFromStrategy 将 translate 策略快照映射为修复选项。worker 的
// BuildEngineConfig 与修订预览共享此实现，保证作业与预览的 Repair 配置同源。
func RepairOptionsFromStrategy(s StrategySnapshot) repair.Options {
	return repair.Config{
		Enabled:              s.Repair.Enabled,
		JSONStructural:       s.Repair.JSONStructural,
		SchemaAliases:        s.Repair.SchemaAliases,
		PlaceholderNormalize: s.Repair.PlaceholderNormalize,
		PromptUpgrade:        s.Repair.PromptUpgrade,
	}.ToOptions()
}
