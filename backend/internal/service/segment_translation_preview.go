package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/markup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/preview"
	"github.com/MeowSalty/LinguaFlow/backend/internal/previewtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

var (
	ErrPreviewNoTranslate  = errors.New("preview: execution plan has no translate round")
	ErrPreviewBusy         = errors.New("preview: concurrency limit reached")
	ErrPreviewConflict     = errors.New("preview: segment baseline changed since preview")
	ErrPreviewTokenExpired = errors.New("preview: apply token expired")
	ErrPreviewTokenInvalid = errors.New("preview: apply token invalid")
	ErrPreviewTargetBlank  = errors.New("preview: target text must be non-blank")
)

// PreviewInput 是单段翻译预览的输入。
type PreviewInput struct {
	ActorUserID     int
	ProjectID       int
	ResourceID      int
	SegmentID       int
	ExecutionPlanID int
	SourceText      string // 仅为调用方保留；不接受改写 source
	SourceTextSet   bool   // 区分“未提供 source_text”与“显式传入空白 source_text”
}

// PreviewBaseline 记录预览开始时目标分段在数据库中的状态，
// 供 apply 步骤检测冲突。
type PreviewBaseline struct {
	ResourceID    int
	SourceText    string
	TargetText    *string
	Status        string
	QualityIssues []qa.QualityIssue
}

// PreviewRoundSummary 是单轮执行的高层摘要。
type PreviewRoundSummary struct {
	Index    int
	Mode     string
	Backend  string
	Status   string // "success" | "partial" | "failed" | "skipped"
	Duration time.Duration
}

// PreviewResult 是预览运行产出的具体结果类型。
type PreviewResult struct {
	Status        string // "success" | "partial" | "failed"
	SegmentID     int
	SourceText    string
	TargetText    string
	QualityIssues []qa.QualityIssue
	Baseline      *PreviewBaseline
	Snapshot      *JobExecutionSnapshot
	Metrics       []backend.MeterMetrics
	Collector     *preview.MemoryCollector
	RoundSummary  []PreviewRoundSummary
	Warnings      []string
}

// PreviewRunner 是执行单段预览的接口。
// 由 worker.PreviewRunner 实现。
type PreviewRunner interface {
	RunPreview(
		ctx context.Context,
		snapshot *JobExecutionSnapshot,
		projectRow *ent.Project,
		resourceRow *ent.Resource,
		allSegments []*ent.Segment,
		targetSegmentIdx int,
		sourceOverride string,
	) (*PreviewResult, error)
}

// PreviewOutput 是单段翻译预览的输出。
type PreviewOutput struct {
	Status         string // "success" | "partial" | "failed"
	SegmentID      int
	SourceText     string
	TargetText     string
	QualityIssues  []qa.QualityIssue
	Snapshot       *JobExecutionSnapshot
	ApplyToken     string
	ApplyExpiresAt time.Time
	Warnings       []string
	RoundSummary   []PreviewRoundSummary
	Usage          UsageSummary
	BatchEvents    []progress.BatchEvent
}

// UsageSummary 汇总一次预览运行的 API 用量。
type UsageSummary struct {
	APICalls     int64
	InputTokens  int64
	OutputTokens int64
}

// PreviewService 编排单段翻译预览流程。
type PreviewService struct {
	logger        *slog.Logger
	client        *ent.Client
	projects      *ProjectService
	jobs          *JobService
	audit         *AuditService
	previewRunner PreviewRunner
	tokenCodec    *previewtoken.Codec
	semaphore     chan struct{}
	timeout       time.Duration
}

// NewPreviewSemaphore 创建由翻译预览与修订预览共享的并发闸门。
func NewPreviewSemaphore(maxConcurrency int) chan struct{} {
	if maxConcurrency <= 0 {
		maxConcurrency = 2
	}
	return make(chan struct{}, maxConcurrency)
}

// NewPreviewService 创建使用私有并发闸门的 PreviewService。
func NewPreviewService(
	logger *slog.Logger,
	client *ent.Client,
	projects *ProjectService,
	jobs *JobService,
	audit *AuditService,
	previewRunner PreviewRunner,
	jwtSecret string,
	tokenTTL time.Duration,
	maxConcurrency int,
	timeout time.Duration,
) *PreviewService {
	return NewPreviewServiceWithSemaphore(logger, client, projects, jobs, audit, previewRunner, jwtSecret, tokenTTL, maxConcurrency, timeout, nil)
}

// NewPreviewServiceWithSemaphore 创建 PreviewService，可选用共享的并发闸门。
func NewPreviewServiceWithSemaphore(
	logger *slog.Logger,
	client *ent.Client,
	projects *ProjectService,
	jobs *JobService,
	audit *AuditService,
	previewRunner PreviewRunner,
	jwtSecret string,
	tokenTTL time.Duration,
	maxConcurrency int,
	timeout time.Duration,
	semaphore chan struct{},
) *PreviewService {
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
	return &PreviewService{
		logger:        logger,
		client:        client,
		projects:      projects,
		jobs:          jobs,
		audit:         audit,
		previewRunner: previewRunner,
		tokenCodec:    previewtoken.NewCodec(jwtSecret, tokenTTL),
		semaphore:     semaphore,
		timeout:       timeout,
	}
}

// RunPreview 校验输入、执行预览、记录用量，并返回结果与可选的 apply 令牌。
func (s *PreviewService) RunPreview(ctx context.Context, input PreviewInput) (*PreviewOutput, error) {
	if input.SourceTextSet || input.SourceText != "" {
		return nil, ErrSourceReadOnly
	}
	// 占用一个信号量槽位。
	select {
	case s.semaphore <- struct{}{}:
	default:
		return nil, ErrPreviewBusy
	}
	defer func() { <-s.semaphore }()

	// 为预览应用超时限制。
	previewCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// 1. 校验项目写入权限。
	projectRow, err := s.projects.requireProjectAccess(previewCtx, input.ActorUserID, input.ProjectID, true)
	if err != nil {
		return nil, fmt.Errorf("preview: project access: %w", err)
	}

	// 2. 校验资源与分段的归属。
	_, err = s.client.Segment.Query().
		Where(segment.IDEQ(input.SegmentID), segment.ResourceIDEQ(input.ResourceID)).
		Only(previewCtx)
	if err != nil {
		return nil, ErrSegmentNotFound
	}

	resRow, err := s.client.Resource.Get(previewCtx, input.ResourceID)
	if err != nil {
		return nil, ErrResourceNotFound
	}
	if resRow.ProjectID == nil || *resRow.ProjectID != input.ProjectID {
		return nil, ErrResourceNotFound
	}

	// 3. 加载、校验并冻结与任务（job）相同的执行快照。
	snapshot, release, err := s.jobs.prepareExecutionSnapshot(previewCtx, input.ActorUserID, projectRow, input.ExecutionPlanID, "")
	if err != nil {
		return nil, err
	}
	defer release()

	// 5. 预览强制指定目标分段：忽略计划的分段过滤与自动批准。
	// 没有翻译轮次的计划直接拒绝。
	hasTranslate := false
	for _, rs := range snapshot.Rounds {
		if rs.Mode == "translate" {
			hasTranslate = true
			break
		}
	}
	if !hasTranslate {
		return nil, ErrPreviewNoTranslate
	}

	snapshot.AutoApprove = false
	snapshot.ExplicitSegmentSelection = true

	// 6. 按 segment_index 顺序加载资源的全部分段。
	allSegments, err := s.client.Segment.Query().
		Where(segment.ResourceIDEQ(input.ResourceID)).
		Order(segment.BySegmentIndex(), segment.ByID()).
		All(previewCtx)
	if err != nil {
		return nil, fmt.Errorf("preview: load segments: %w", err)
	}

	// 在有序列表中定位目标分段的下标。
	targetSegmentIdx := -1
	for i, seg := range allSegments {
		if seg.ID == input.SegmentID {
			targetSegmentIdx = i
			break
		}
	}
	if targetSegmentIdx < 0 {
		return nil, ErrSegmentNotFound
	}

	// 8. 执行预览。
	result, err := s.previewRunner.RunPreview(
		previewCtx,
		snapshot,
		projectRow,
		resRow,
		allSegments,
		targetSegmentIdx,
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("preview: execute: %w", err)
	}

	// 9. 记录用量（尽力而为，即使出错也继续）。
	usageMetrics := aggregateMetrics(result.Metrics)
	if usageMetrics.APICalls > 0 {
		usageCtx, usageCancel := context.WithTimeout(context.WithoutCancel(previewCtx), 5*time.Second)
		if err := s.recordUsage(usageCtx, input, projectRow, usageMetrics); err != nil {
			s.logger.Warn("preview: failed to record usage", "err", err)
			result.Warnings = append(result.Warnings, "预览用量记录失败")
		}
		usageCancel()
	}

	// 10. 若已有目标译文，则构建 apply 令牌。
	if err := previewCtx.Err(); err != nil {
		return nil, fmt.Errorf("preview: execution deadline: %w", err)
	}

	var applyToken string
	var applyExpiresAt time.Time
	if result.TargetText != "" {
		targetHash := sha256Hex(result.TargetText)
		sourceHash := sha256Hex(result.SourceText)
		qaCfg := qaConfigFromSnapshot(snapshot, resRow.Format)
		claims := previewtoken.ApplyClaims{
			ActorUserID:      input.ActorUserID,
			ProjectID:        input.ProjectID,
			ResourceID:       input.ResourceID,
			SourceRevisionID: resRow.CurrentSourceRevisionID,
			SourceGeneration: resRow.SourceGeneration,
			SegmentID:        input.SegmentID,
			ExecutionPlanID:  input.ExecutionPlanID,
			Kind:             previewtoken.KindTranslate,
			SourceHash:       sourceHash,
			PreviewSource:    result.SourceText,
			TargetHash:       targetHash,
			BaselineSource:   result.Baseline.SourceText,
			BaselineTarget:   result.Baseline.TargetText,
			BaselineStatus:   result.Baseline.Status,
			BaselineVersion:  allSegments[targetSegmentIdx].ContentVersion,
			FinalIssues:      result.QualityIssues,
			QAConfig:         qaCfg,
		}
		applyToken, applyExpiresAt, err = s.tokenCodec.Encode(claims)
		if err != nil {
			s.logger.Warn("preview: failed to encode apply token", "err", err)
		}
	}

	// 11. 为 HTTP 适配层保留完整的批次诊断信息。
	events := result.Collector.Events()

	return &PreviewOutput{
		Status:         result.Status,
		SegmentID:      result.SegmentID,
		SourceText:     result.SourceText,
		TargetText:     result.TargetText,
		QualityIssues:  result.QualityIssues,
		Snapshot:       result.Snapshot,
		ApplyToken:     applyToken,
		ApplyExpiresAt: applyExpiresAt,
		Warnings:       result.Warnings,
		RoundSummary:   result.RoundSummary,
		Usage: UsageSummary{
			APICalls:     usageMetrics.APICalls,
			InputTokens:  usageMetrics.InputTokens,
			OutputTokens: usageMetrics.OutputTokens,
		},
		BatchEvents: events,
	}, nil
}

// ApplyPreview 通过条件式 CAS 更新，把预览翻译结果写入数据库。
func (s *PreviewService) ApplyPreview(
	ctx context.Context,
	actorUserID, projectID, resourceID, segmentID int,
	applyToken, targetText string,
) (*ent.Segment, error) {
	targetText = strings.TrimSpace(targetText)
	if targetText == "" {
		return nil, ErrPreviewTargetBlank
	}

	// 1. 解码并校验令牌。
	claims, err := s.tokenCodec.Decode(applyToken)
	if err != nil {
		if errors.Is(err, previewtoken.ErrTokenExpired) {
			return nil, ErrPreviewTokenExpired
		}
		return nil, ErrPreviewTokenInvalid
	}

	// 2. 校验归属。
	if err := previewtoken.VerifyOwnership(claims, actorUserID, projectID, resourceID, segmentID); err != nil {
		return nil, ErrPreviewTokenInvalid
	}
	if claims.SourceHash != sha256Hex(claims.PreviewSource) {
		return nil, ErrPreviewTokenInvalid
	}
	if claims.PreviewSource != claims.BaselineSource {
		return nil, ErrSourceReadOnly
	}
	projectRow, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true)
	if err != nil {
		return nil, err
	}

	// 3. 加载当前分段以核对基线。
	currentSeg, err := s.client.Segment.Query().
		Where(segment.IDEQ(segmentID), segment.ResourceIDEQ(resourceID)).
		Only(ctx)
	if err != nil {
		return nil, ErrSegmentNotFound
	}

	// 4. 核对基线：source、可空的 target、status 都必须一致。
	baselineSource := claims.BaselineSource
	baselineTarget := claims.BaselineTarget
	baselineStatus := claims.BaselineStatus
	baselineVersion := claims.BaselineVersion
	if baselineVersion == 0 {
		baselineVersion = 1
	} // pre-version tokens can only apply to an untouched migrated row

	sourceMatch := currentSeg.SourceText == baselineSource
	targetMatch := ptrStringEqual(currentSeg.TargetText, baselineTarget)
	statusMatch := string(currentSeg.Status) == baselineStatus

	if !sourceMatch || !targetMatch || !statusMatch || currentSeg.ContentVersion != baselineVersion {
		return nil, ErrPreviewConflict
	}

	// 结构守卫：与手动编辑同一硬口径（见 UpdateResourceSegment）。ApplyPreview 是
	// 交互式单段写入，语义与手动编辑一致——应当场报错，而不是等导出预检才发现
	// 段落无法交付。校验必须在 CAS update 之前，否则要么破坏 CAS 语义、要么引入
	// 先写库再回滚的复杂度。格式必须查库取：令牌里冻结的 QAConfig.Format 是 QA
	// 配置的载体而非资源格式的事实来源（历史令牌可能为空），结构门禁不应依赖它。
	resRow, err := s.client.Resource.Get(ctx, resourceID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrResourceNotFound
		}
		return nil, fmt.Errorf("apply: load resource: %w", err)
	}
	if markup.RequiresWellFormedTargets(resRow.Format) {
		if verr := markup.ValidateFragment(targetText); verr != nil {
			return nil, &SegmentMarkupError{Err: verr}
		}
	}
	if resRow.SourceGeneration != claims.SourceGeneration || !ptrIntEq(resRow.CurrentSourceRevisionID, claims.SourceRevisionID) {
		return nil, ErrSourceRevisionConflict
	}

	// 5. 确定最终的质检 issue。
	targetChanged := sha256Hex(targetText) != claims.TargetHash
	var finalIssues []qa.QualityIssue
	if !targetChanged {
		finalIssues = claims.FinalIssues
	} else {
		// 用户在 apply 前改写了文本：按令牌冻结配置重跑确定性 QA。
		var fresh []qa.QualityIssue
		qaRan := false
		qaCfg := qaConfigFromClaims(claims)
		qaCfg.SourceLang = claims.QAConfig.SourceLang
		qaCfg.TargetLang = claims.QAConfig.TargetLang
		if qaCfg.Enabled {
			runtimeGlossary, err := NewDatabaseGlossary(ctx, s.client, projectRow)
			if err != nil {
				s.logger.Warn("apply: failed to load glossary for re-qa", "err", err)
			} else {
				qaCfg.Glossary = runtimeGlossary
			}
			qaEngine := qa.NewEngine(qaCfg, s.logger)
			inputs := []qa.CheckInput{{
				Index:      currentSeg.SegmentIndex,
				SourceText: claims.PreviewSource,
				TargetText: targetText,
			}}
			allIssues := qaEngine.Run(ctx, inputs)
			fresh = qa.IssuesFor(currentSeg.SegmentIndex, allIssues)
			qaRan = true

			// 仅当冻结的确定性 QA 配置启用了同文异译检查时，才重跑该项。
			if duplicateSourceDivergenceEnabledForClaims(claims.QAConfig) {
				s.rerunDuplicateSourceDivergence(ctx, resourceID, segmentID, currentSeg.SegmentIndex, claims.PreviewSource, targetText, &fresh)
			}
		}
		if claims.Kind == previewtoken.KindRevision && len(claims.ResolvedCodes) > 0 {
			// 修订令牌：剔除声明已修复的 pending（范围外与 dismissed 保留），
			// 确定性 issue 以重算结果为准；qaRan=false 时保留既有非目标 issue。
			finalIssues = qa.ReviseFinalIssues(currentSeg.QualityIssues, fresh, claims.ResolvedCodes, qaRan)
		} else if qaRan {
			finalIssues = fresh
		}
		// else：翻译预览 + QA 未启用 → finalIssues 为 nil → 清空（既有行为，保持不变）。
	}

	// 6. CAS 更新。
	// 使用与基线匹配的条件更新。
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := AdvanceTranslationGeneration(ctx, tx.Client(), resourceID, claims.SourceGeneration); err != nil {
		return nil, err
	}
	update := tx.Segment.Update().
		Where(
			segment.IDEQ(segmentID),
			segment.ResourceIDEQ(resourceID),
			segment.SourceTextEQ(baselineSource),
			segment.StatusEQ(segment.Status(baselineStatus)),
			segment.ContentVersionEQ(baselineVersion),
		)
	if baselineTarget != nil {
		update = update.Where(segment.TargetTextEQ(*baselineTarget))
	} else {
		update = update.Where(segment.TargetTextIsNil())
	}

	update = update.
		SetTargetText(targetText).
		SetStatus(SegmentStatusEdited).
		SetReviewedByID(actorUserID).
		ClearReviewComment()

	// quality_issues 写回：ApplyPreview 应用新译文（预览或用户改后），新译文导致
	// 指纹基本全变，旧裁决不跨文本存活，故不对账。若未来引入"同译文手动重算 QA"
	// 功能（如用户改了非译文字段后重跑 QA），必须接入 qa.ReconcileIssues。
	// 修订令牌例外：按 ResolvedCodes 剔除声明已修复的 pending，其余判决保留。
	if len(finalIssues) > 0 {
		update = update.SetQualityIssues(finalIssues)
	} else {
		update = update.ClearQualityIssues()
	}

	rowsAffected, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("apply: update segment: %w", err)
	}
	if rowsAffected == 0 {
		return nil, ErrPreviewConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	action := "resource.segment.translation_preview.apply"
	message := fmt.Sprintf("Applied preview translation to segment %d", segmentID)
	if claims.Kind == previewtoken.KindRevision {
		action = "resource.segment.revision_preview.apply"
		message = fmt.Sprintf("Applied preview revision to segment %d", segmentID)
	}
	metadata := map[string]any{
		"execution_plan_id": claims.ExecutionPlanID,
		"target_changed":    targetChanged,
		"preview_kind":      claims.Kind,
	}
	if claims.Kind == previewtoken.KindRevision {
		// 翻译令牌无 ResolvedCodes，不加该字段避免审计噪音。
		metadata["resolved_codes"] = claims.ResolvedCodes
	}
	auditEvent := AuditEvent{
		ActorUserID:  actorUserID,
		ProjectID:    &projectID,
		ResourceID:   segmentID,
		Action:       action,
		ResourceType: "segment",
		Message:      message,
		Metadata:     metadata,
	}
	auditEvent.OrgID = EffectiveProjectOrgID(projectRow)
	if s.audit != nil {
		_ = s.audit.Record(ctx, auditEvent)
	}

	// 8. 返回刷新后的分段。
	return s.client.Segment.Query().
		Where(segment.IDEQ(segmentID)).
		WithReviewedBy().
		WithResource().
		Only(ctx)
}

func (s *PreviewService) recordUsage(ctx context.Context, input PreviewInput, projectRow *ent.Project, metrics backend.MeterMetrics) error {
	usage := s.client.UsageRecord.Create().
		SetVisibilityScope("project").
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
	if orgID := EffectiveProjectOrgID(projectRow); orgID != nil {
		usage.SetOrganizationID(*orgID)
	}
	return usage.Exec(ctx)
}

func aggregateMetrics(metrics []backend.MeterMetrics) backend.MeterMetrics {
	var total backend.MeterMetrics
	for _, m := range metrics {
		total.APICalls += m.APICalls
		total.InputTokens += m.InputTokens
		total.OutputTokens += m.OutputTokens
	}
	return total
}

func qaConfigFromSnapshot(snapshot *JobExecutionSnapshot, resourceFormat string) previewtoken.QAConfigClaims {
	// QA 配置位于计划级策略快照（snapshot.Strategy），不再扫描轮次。
	s := snapshot.Strategy
	return previewtoken.QAConfigClaims{
		Enabled:        s.QA.Enabled,
		Checks:         s.QA.Checks,
		LengthMethod:   s.QA.LengthMethod,
		LengthRatioMin: s.QA.LengthRatioMin,
		LengthRatioMax: s.QA.LengthRatioMax,
		SourceLang:     snapshot.SourceLang,
		TargetLang:     snapshot.TargetLang,
		Format:         resourceFormat,
	}
}

func qaConfigFromClaims(claims *previewtoken.ApplyClaims) qa.Config {
	return qa.Config{
		Enabled:        claims.QAConfig.Enabled,
		Checks:         claims.QAConfig.Checks,
		LengthMethod:   qa.LengthMethod(claims.QAConfig.LengthMethod),
		LengthRatioMin: claims.QAConfig.LengthRatioMin,
		LengthRatioMax: claims.QAConfig.LengthRatioMax,
		SourceLang:     claims.QAConfig.SourceLang,
		TargetLang:     claims.QAConfig.TargetLang,
		Format:         claims.QAConfig.Format,
	}
}

// duplicateSourceDivergenceEnabledForClaims 判定冻结在 apply token 中的 QA 配置
// 是否启用文档级同文异译检查；判定统一委托 qa 包的共享实现，避免多份漂移。
func duplicateSourceDivergenceEnabledForClaims(cfg previewtoken.QAConfigClaims) bool {
	return cfg.Enabled && qa.DuplicateSourceDivergenceEnabled(cfg.Checks)
}

// rerunDuplicateSourceDivergence 对整个资源快照重算同文异译 issue：
// 用预览值覆盖目标分段的 source/target，且只合并目标分段产生的 issue。
func (s *PreviewService) rerunDuplicateSourceDivergence(
	ctx context.Context,
	resourceID, segmentID, targetSegmentIndex int,
	previewSource, targetText string,
	finalIssues *[]qa.QualityIssue,
) {
	allSegments, err := s.client.Segment.Query().
		Where(segment.ResourceIDEQ(resourceID)).
		Order(segment.BySegmentIndex(), segment.ByID()).
		All(ctx)
	if err != nil {
		s.logger.Warn("apply: load segments for duplicate source QA failed", "err", err)
		return
	}
	divergenceInputs := make([]qa.CheckInput, 0, len(allSegments))
	for _, seg := range allSegments {
		tgt := ""
		if seg.TargetText != nil {
			tgt = *seg.TargetText
		}
		divergenceInputs = append(divergenceInputs, qa.CheckInput{
			Index:      seg.SegmentIndex,
			SourceText: seg.SourceText,
			TargetText: tgt,
		})
	}
	for i := range divergenceInputs {
		if allSegments[i].ID == segmentID {
			divergenceInputs[i].TargetText = targetText
			divergenceInputs[i].SourceText = previewSource
			break
		}
	}
	divergenceIssues := qa.CheckDuplicateSourceDivergence(divergenceInputs)
	for _, di := range divergenceIssues {
		if di.SegmentIndex == targetSegmentIndex {
			*finalIssues = append(*finalIssues, di)
		}
	}
	*finalIssues = qa.DedupIssues(*finalIssues)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func ptrStringEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func clampInt64ToInt(v int64) int {
	if v > int64(^uint32(0)>>1) {
		return int(^uint32(0) >> 1)
	}
	if v < 0 {
		return 0
	}
	return int(v)
}
