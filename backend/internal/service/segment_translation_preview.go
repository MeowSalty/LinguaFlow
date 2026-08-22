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

// PreviewInput is the input for a single segment translation preview.
type PreviewInput struct {
	ActorUserID     int
	ProjectID       int
	ResourceID      int
	SegmentID       int
	ExecutionPlanID int
	SourceText      string // optional override; empty means use DB value
	SourceTextSet   bool   // distinguishes omitted source_text from blank input
}

// PreviewBaseline captures the database state of the target segment at the
// start of the preview so that the apply step can detect conflicts.
type PreviewBaseline struct {
	ResourceID    int
	SourceText    string
	TargetText    *string
	Status        string
	QualityIssues []qa.QualityIssue
}

// PreviewRoundSummary is a high-level summary of one round's execution.
type PreviewRoundSummary struct {
	Index    int
	Mode     string
	Backend  string
	Status   string // "success" | "partial" | "failed" | "skipped"
	Duration time.Duration
}

// PreviewResult is the concrete result type produced by a preview run.
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

// PreviewRunner is the interface for executing a single segment preview.
// Implemented by worker.PreviewRunner.
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

// PreviewOutput is the output of a single segment translation preview.
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

// UsageSummary summarizes API usage for a preview run.
type UsageSummary struct {
	APICalls     int64
	InputTokens  int64
	OutputTokens int64
}

// PreviewService orchestrates single segment translation previews.
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

// NewPreviewSemaphore creates the concurrency gate shared by translation and
// revision previews.
func NewPreviewSemaphore(maxConcurrency int) chan struct{} {
	if maxConcurrency <= 0 {
		maxConcurrency = 2
	}
	return make(chan struct{}, maxConcurrency)
}

// NewPreviewService creates a PreviewService using a private concurrency gate.
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

// NewPreviewServiceWithSemaphore creates a PreviewService with an optionally
// shared concurrency gate.
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

// RunPreview validates input, executes the preview, records usage, and returns
// the result with an optional apply token.
func (s *PreviewService) RunPreview(ctx context.Context, input PreviewInput) (*PreviewOutput, error) {
	// Acquire semaphore slot.
	select {
	case s.semaphore <- struct{}{}:
	default:
		return nil, ErrPreviewBusy
	}
	defer func() { <-s.semaphore }()
	if input.SourceTextSet && strings.TrimSpace(input.SourceText) == "" {
		return nil, ErrInvalidInput
	}

	// Apply preview timeout.
	previewCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// 1. Validate project write access.
	projectRow, err := s.projects.requireProjectAccess(previewCtx, input.ActorUserID, input.ProjectID, true)
	if err != nil {
		return nil, fmt.Errorf("preview: project access: %w", err)
	}

	// 2. Validate resource and segment ownership.
	segRow, err := s.client.Segment.Query().
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

	// 3. Load, validate, and freeze the same execution snapshot used by jobs.
	snapshot, err := s.jobs.prepareExecutionSnapshot(previewCtx, input.ActorUserID, projectRow, input.ExecutionPlanID, "")
	if err != nil {
		return nil, err
	}

	// 5. Preview forces explicit target segment; ignore plan segment filter and auto approve.
	// Reject plans without a translate round.
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

	// 6. Load all resource segments ordered by segment_index.
	allSegments, err := s.client.Segment.Query().
		Where(segment.ResourceIDEQ(input.ResourceID)).
		Order(segment.BySegmentIndex(), segment.ByID()).
		All(previewCtx)
	if err != nil {
		return nil, fmt.Errorf("preview: load segments: %w", err)
	}

	// Find the target segment's index in the ordered list.
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

	// 7. Determine source override.
	sourceOverride := input.SourceText
	if sourceOverride == segRow.SourceText {
		sourceOverride = "" // no change needed
	}

	// 8. Execute preview.
	result, err := s.previewRunner.RunPreview(
		previewCtx,
		snapshot,
		projectRow,
		resRow,
		allSegments,
		targetSegmentIdx,
		sourceOverride,
	)
	if err != nil {
		return nil, fmt.Errorf("preview: execute: %w", err)
	}

	// 9. Record usage (best-effort, even on errors).
	usageMetrics := aggregateMetrics(result.Metrics)
	if usageMetrics.APICalls > 0 {
		usageCtx, usageCancel := context.WithTimeout(context.WithoutCancel(previewCtx), 5*time.Second)
		if err := s.recordUsage(usageCtx, input, projectRow, usageMetrics); err != nil {
			s.logger.Warn("preview: failed to record usage", "err", err)
			result.Warnings = append(result.Warnings, "预览用量记录失败")
		}
		usageCancel()
	}

	// 10. Build apply token if we have a target.
	var applyToken string
	var applyExpiresAt time.Time
	if result.TargetText != "" {
		targetHash := sha256Hex(result.TargetText)
		sourceHash := sha256Hex(result.SourceText)
		qaCfg := qaConfigFromSnapshot(snapshot, resRow.Format)
		claims := previewtoken.ApplyClaims{
			ActorUserID:     input.ActorUserID,
			ProjectID:       input.ProjectID,
			ResourceID:      input.ResourceID,
			SegmentID:       input.SegmentID,
			ExecutionPlanID: input.ExecutionPlanID,
			Kind:            previewtoken.KindTranslate,
			SourceHash:      sourceHash,
			PreviewSource:   result.SourceText,
			TargetHash:      targetHash,
			BaselineSource:  result.Baseline.SourceText,
			BaselineTarget:  result.Baseline.TargetText,
			BaselineStatus:  result.Baseline.Status,
			FinalIssues:     result.QualityIssues,
			QAConfig:        qaCfg,
		}
		applyToken, applyExpiresAt, err = s.tokenCodec.Encode(claims)
		if err != nil {
			s.logger.Warn("preview: failed to encode apply token", "err", err)
		}
	}

	// 11. Preserve complete batch diagnostics for the HTTP adapter.
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

// ApplyPreview applies a preview translation result to the database using a
// conditional CAS update.
func (s *PreviewService) ApplyPreview(
	ctx context.Context,
	actorUserID, projectID, resourceID, segmentID int,
	applyToken, targetText string,
) (*ent.Segment, error) {
	targetText = strings.TrimSpace(targetText)
	if targetText == "" {
		return nil, ErrPreviewTargetBlank
	}

	// 1. Decode and verify token.
	claims, err := s.tokenCodec.Decode(applyToken)
	if err != nil {
		if errors.Is(err, previewtoken.ErrTokenExpired) {
			return nil, ErrPreviewTokenExpired
		}
		return nil, ErrPreviewTokenInvalid
	}

	// 2. Verify ownership.
	if err := previewtoken.VerifyOwnership(claims, actorUserID, projectID, resourceID, segmentID); err != nil {
		return nil, ErrPreviewTokenInvalid
	}
	if claims.SourceHash != sha256Hex(claims.PreviewSource) {
		return nil, ErrPreviewTokenInvalid
	}
	projectRow, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true)
	if err != nil {
		return nil, err
	}

	// 3. Load the current segment to check baseline.
	currentSeg, err := s.client.Segment.Query().
		Where(segment.IDEQ(segmentID), segment.ResourceIDEQ(resourceID)).
		Only(ctx)
	if err != nil {
		return nil, ErrSegmentNotFound
	}

	// 4. Check baseline: source, nullable target, status must match.
	baselineSource := claims.BaselineSource
	baselineTarget := claims.BaselineTarget
	baselineStatus := claims.BaselineStatus

	sourceMatch := currentSeg.SourceText == baselineSource
	targetMatch := ptrStringEqual(currentSeg.TargetText, baselineTarget)
	statusMatch := string(currentSeg.Status) == baselineStatus

	if !sourceMatch || !targetMatch || !statusMatch {
		return nil, ErrPreviewConflict
	}

	// 5. Determine final issues.
	targetChanged := sha256Hex(targetText) != claims.TargetHash
	var finalIssues []qa.QualityIssue
	if !targetChanged {
		finalIssues = claims.FinalIssues
	}

	// If target was modified, re-run deterministic QA.
	if targetChanged {
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
			finalIssues = qa.IssuesFor(currentSeg.SegmentIndex, allIssues)

			// Re-run duplicate-source-divergence only when it was enabled by
			// the frozen deterministic QA configuration.
			if duplicateSourceDivergenceEnabledForClaims(claims.QAConfig) {
				s.rerunDuplicateSourceDivergence(ctx, resourceID, segmentID, currentSeg.SegmentIndex, claims.PreviewSource, targetText, &finalIssues)
			}
		}
	}

	// 6. CAS update.
	// Use a conditional update matching the baseline.
	update := s.client.Segment.Update().
		Where(
			segment.IDEQ(segmentID),
			segment.ResourceIDEQ(resourceID),
			segment.SourceTextEQ(baselineSource),
			segment.StatusEQ(segment.Status(baselineStatus)),
		)
	if baselineTarget != nil {
		update = update.Where(segment.TargetTextEQ(*baselineTarget))
	} else {
		update = update.Where(segment.TargetTextIsNil())
	}

	update = update.
		SetSourceText(claims.PreviewSource).
		SetTargetText(targetText).
		SetStatus(SegmentStatusEdited).
		SetReviewedByID(actorUserID).
		ClearReviewComment()

	// quality_issues 写回：ApplyPreview 应用新译文（预览或用户改后），新译文导致
	// 指纹基本全变，旧裁决不跨文本存活，故不对账。若未来引入"同译文手动重算 QA"
	// 功能（如用户改了非译文字段后重跑 QA），必须接入 qa.ReconcileIssues。
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

	action := "resource.segment.translation_preview.apply"
	message := fmt.Sprintf("Applied preview translation to segment %d", segmentID)
	if claims.Kind == previewtoken.KindRevision {
		action = "resource.segment.revision_preview.apply"
		message = fmt.Sprintf("Applied preview revision to segment %d", segmentID)
	}
	auditEvent := AuditEvent{
		ActorUserID:  actorUserID,
		ProjectID:    &projectID,
		ResourceID:   resourceID,
		Action:       action,
		ResourceType: "segment",
		Message:      message,
		Metadata: map[string]any{
			"execution_plan_id": claims.ExecutionPlanID,
			"target_changed":    targetChanged,
			"preview_kind":      claims.Kind,
		},
	}
	if projectRow.OwnerOrgID != nil {
		auditEvent.OrgID = projectRow.OwnerOrgID
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, auditEvent)
	}

	// 8. Return refreshed segment.
	return s.client.Segment.Query().
		Where(segment.IDEQ(segmentID)).
		WithReviewedBy().
		WithResource().
		Only(ctx)
}

func (s *PreviewService) recordUsage(ctx context.Context, input PreviewInput, projectRow *ent.Project, metrics backend.MeterMetrics) error {
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
	for _, rs := range snapshot.Rounds {
		if rs.Mode == "translate" && rs.Translate != nil {
			s := rs.Translate.Strategy
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
	}
	return previewtoken.QAConfigClaims{}
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

// duplicateSourceDivergenceEnabledForClaims mirrors the worker's
// duplicateSourceDivergenceEnabled but operates on the frozen QA config
// captured in the apply token.
func duplicateSourceDivergenceEnabledForClaims(cfg previewtoken.QAConfigClaims) bool {
	if !cfg.Enabled {
		return false
	}
	if cfg.Checks == nil {
		return true
	}
	for _, name := range cfg.Checks {
		if name == qa.CodeDuplicateSourceDivergence {
			return true
		}
	}
	return false
}

// rerunDuplicateSourceDivergence recomputes duplicate-source-divergence issues
// for the whole resource snapshot, overriding the target segment's source/target
// with the preview values, and merges only the target segment's resulting issues.
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
