package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/preview"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// PreviewRunner executes a single segment translation preview in-memory
// without persisting any results to the database (except UsageRecord via the
// caller). It reuses the same EngineFactory as JobRunner for identical
// execution configuration.
type PreviewRunner struct {
	logger  *slog.Logger
	client  *ent.Client
	factory *EngineFactory
}

// NewPreviewRunner creates a PreviewRunner.
func NewPreviewRunner(logger *slog.Logger, client *ent.Client, limiterPool *backend.LimiterPool) *PreviewRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &PreviewRunner{
		logger:  logger,
		client:  client,
		factory: NewEngineFactory(logger, limiterPool),
	}
}

// RunPreview executes a single segment translation preview.
//
// Parameters:
//   - snapshot: the validated JobExecutionSnapshot (must have at least one translate round)
//   - projectRow: the project (for glossary and language config)
//   - resourceRow: the resource (for format detection)
//   - allSegments: all segments of the resource, ordered by segment_index
//   - targetSegmentIdx: the 0-based index within allSegments for the target segment
//   - sourceOverride: optional source text to use instead of the database value
//
// The caller must provide the in-memory resource snapshot. This function never
// reads from the database itself.
func (r *PreviewRunner) RunPreview(
	ctx context.Context,
	snapshot *service.JobExecutionSnapshot,
	projectRow *ent.Project,
	resourceRow *ent.Resource,
	allSegments []*ent.Segment,
	targetSegmentIdx int,
	sourceOverride string,
) (*service.PreviewResult, error) {
	if len(snapshot.Rounds) == 0 {
		return nil, errors.New("preview: execution plan has no rounds")
	}

	hasTranslate := false
	for _, rs := range snapshot.Rounds {
		if rs.Mode == "translate" {
			hasTranslate = true
			break
		}
	}
	if !hasTranslate {
		return nil, errors.New("preview: execution plan must have at least one translate round")
	}

	if targetSegmentIdx < 0 || targetSegmentIdx >= len(allSegments) {
		return nil, fmt.Errorf("preview: target segment index %d out of range (0..%d)", targetSegmentIdx, len(allSegments)-1)
	}

	targetSeg := allSegments[targetSegmentIdx]
	previewSource := targetSeg.SourceText
	if sourceOverride != "" {
		previewSource = sourceOverride
	}
	baseline := &service.PreviewBaseline{
		ResourceID:    resourceRow.ID,
		SourceText:    targetSeg.SourceText,
		TargetText:    targetSeg.TargetText,
		Status:        string(targetSeg.Status),
		QualityIssues: cloneIssues(targetSeg.QualityIssues),
	}

	engineCfg := BuildEngineConfig(snapshot)
	runtimeGlossary := r.buildPreviewGlossary(ctx, projectRow, engineCfg.Glossary.Enabled)
	translationMemory := r.buildPreviewTM(engineCfg.TMEnabled)

	var qaEngine *qa.Engine
	if engineCfg.QA.Enabled {
		qaCfg := engineCfg.QA
		qaCfg.Glossary = runtimeGlossary
		qaCfg.Format = resourceRow.Format
		qaEngine = qa.NewEngine(qaCfg, r.logger)
	}

	collector := preview.NewMemoryCollector()
	resources := engine.RuntimeResources{Glossary: runtimeGlossary, TM: translationMemory}
	eng, err := r.factory.BuildEngine(ctx, snapshot, resources, collector)
	if err != nil {
		return nil, fmt.Errorf("preview: build engine: %w", err)
	}
	defer func() { _ = eng.Close() }()

	// Build the in-memory Document from the full resource snapshot.
	// All segments are included as context; only the target translates.
	inputs := r.buildSegmentInputs(allSegments, targetSegmentIdx, sourceOverride)
	doc := pipeline.BuildDocumentFromSegments(inputs, snapshot.SourceLang, snapshot.TargetLang, resourceRow.Format)

	// Mark only the target segment for translation.
	targetDocIdx := -1
	for i := range doc.Segments {
		// The document segments are built from allSegments in order, so the
		// target segment is at position targetSegmentIdx.
		if i == targetSegmentIdx {
			targetDocIdx = i
			doc.Segments[i].Translate = true
		} else {
			doc.Segments[i].Translate = false
		}
	}
	if targetDocIdx < 0 {
		return nil, fmt.Errorf("preview: target segment not found in document at index %d", targetSegmentIdx)
	}

	// Determine the last translate round index.
	lastTranslateRoundIdx := -1
	firstTranslateRoundIdx := -1
	for i := range snapshot.Rounds {
		if snapshot.Rounds[i].Mode == "translate" {
			if firstTranslateRoundIdx < 0 {
				firstTranslateRoundIdx = i
			}
			lastTranslateRoundIdx = i
		}
	}

	// 跨轮增量载体（in-memory）：per-mode 已解决段索引集合。
	// 与 JobRunner 保持一致，使 preview 诊断行为与正式作业对齐。
	// translate 不参与（由 DB status 驱动增量）。单段场景下集合退化为空或 {targetDocIdx}。
	resolvedByMode := engine.NewResolvedByMode()

	var warnings []string
	var roundSummaries []service.PreviewRoundSummary

	// Round loop.
	for roundIdx := range snapshot.Rounds {
		if ctx.Err() != nil {
			warnings = append(warnings, "preview cancelled during round execution")
			break
		}

		round := snapshot.Rounds[roundIdx]
		roundStart := time.Now()

		// For translate rounds, build a fresh document from the current
		// in-memory state so that adjudicate/semantic QA results are visible
		// to subsequent rounds, emulating JobRunner's "reload from DB" pattern.
		if roundIdx > 0 {
			inputs := r.buildSegmentInputsFromDoc(doc)
			doc = pipeline.BuildDocumentFromSegments(inputs, snapshot.SourceLang, snapshot.TargetLang, resourceRow.Format)
		}

		// 非目标段恒为上下文（Translate=false）。
		for i := range doc.Segments {
			if i != targetDocIdx {
				doc.Segments[i].Translate = false
			}
		}

		// 决定目标段是否本轮翻译。
		var segmentIndexes []int
		skippedThisRound := false
		switch round.Mode {
		case "translate":
			if roundIdx == firstTranslateRoundIdx {
				// 首个 translate 轮强制纳入 target，无视其初始 status
				// （保留"预览=强制重译目标段"的产品语义）。
				doc.Segments[targetDocIdx].Translate = true
				segmentIndexes = []int{targetDocIdx}
			} else {
				var filter *service.SegmentFilterSnapshot
				if round.Translate != nil {
					filter = round.Translate.SegmentFilter
				}
				if translateStatusAllowed(filter, doc.Segments[targetDocIdx].Status) {
					doc.Segments[targetDocIdx].Translate = true
					segmentIndexes = []int{targetDocIdx}
				} else {
					// 跳过本轮 translate；target 保持上一轮译文作为上下文。
					doc.Segments[targetDocIdx].Translate = false
					segmentIndexes = nil
					skippedThisRound = true
				}
			}
		default:
			// extract/adjudicate/semantic_qa/revise：始终处理 target；其筛选逻辑由
			// 各自 handler 按 status 自行完成，不受本次改动影响。
			doc.Segments[targetDocIdx].Translate = true
			segmentIndexes = []int{targetDocIdx}
		}

		// Build the batch handler for this round.
		var batchHandler func(ctx context.Context, batchResult pipeline.BatchResult) error
		switch round.Mode {
		case "translate":
			batchHandler = r.buildTranslateBatchHandler(doc, qaEngine, engineCfg, targetDocIdx)
		case "adjudicate":
			batchHandler = r.buildAdjudicateBatchHandler(doc, targetDocIdx)
		case "semantic_qa":
			batchHandler = r.buildSemanticQABatchHandler(doc, targetDocIdx)
		case "revise":
			// 写回时移除的 issue 集合与送进 prompt 的目标集合严格一致
			//（计划校验保证只能是语义 code）。
			var reviseCodes []string
			if round.Revise != nil {
				reviseCodes = round.Revise.IssueCodes
			}
			batchHandler = buildReviseBatchHandlerCommon(doc, qaEngine, targetDocIdx, reviseCodes)
		case "correct":
			batchHandler = r.buildCorrectBatchHandler(doc, targetDocIdx)
		}

		execOpts := []engine.ExecuteOption{
			engine.WithSegmentFilter(segmentIndexes),
		}
		if batchHandler != nil {
			execOpts = append(execOpts, engine.WithBatchHandler(batchHandler))
		}
		// 非翻译轮注入跨轮增量载体：BuildBatches 据此排除上一同模式轮已解决的段。
		if round.Mode != pipeline.RoundModeTranslate {
			execOpts = append(execOpts, engine.WithResolvedIndices(resolvedByMode[round.Mode]))
		}

		result, roundErr := eng.ExecuteRound(ctx, roundIdx, doc, execOpts...)
		duration := time.Since(roundStart)

		summary := service.PreviewRoundSummary{
			Index:    roundIdx,
			Mode:     round.Mode,
			Backend:  round.Backend.Name,
			Duration: duration,
		}

		if roundErr != nil {
			if errors.Is(roundErr, context.Canceled) {
				summary.Status = "failed"
				roundSummaries = append(roundSummaries, summary)
				warnings = append(warnings, fmt.Sprintf("round %d (%s) cancelled", roundIdx, round.Mode))
				break
			}
			summary.Status = "failed"
			roundSummaries = append(roundSummaries, summary)
			if round.Mode == "semantic_qa" || round.Mode == "revise" || round.Mode == "extract" {
				warnings = append(warnings, fmt.Sprintf("round %d (%s) failed: %s", roundIdx, round.Mode, roundErr))
				continue
			}
			// Translate/adjudicate failures are terminal.
			metrics := CollectMeterMetrics(eng)
			return &service.PreviewResult{
				Status:       "failed",
				SegmentID:    targetSeg.ID,
				SourceText:   previewSource,
				Baseline:     baseline,
				Snapshot:     snapshot,
				Metrics:      metrics,
				Collector:    collector,
				RoundSummary: append(roundSummaries, summary),
				Warnings:     append(warnings, fmt.Sprintf("terminal round %d (%s) failed: %s", roundIdx, round.Mode, roundErr)),
			}, nil
		}

		if result.FailedBatchCount > 0 {
			summary.Status = "partial"
		} else if len(result.Unresolved) > 0 {
			// 非翻译轮（semantic_qa/extract/adjudicate）的未解决段体现在 Unresolved 切片
			// （跨轮传播 / 解析失败 / 瞬时错误耗尽）；translate 轮不会设置该切片。
			summary.Status = "partial"
		} else if result.UnresolvedCount > 0 {
			// translate 轮的终态失败通过 Finalize 计入 _translate_failed_indices，
			// 体现在 UnresolvedCount；translate handler 不会递增 FailedBatchCount，
			// 故需显式检查 UnresolvedCount 才能避免漏报失败轮次。
			summary.Status = "partial"
		} else if skippedThisRound {
			summary.Status = "skipped"
		} else {
			summary.Status = "success"
		}
		roundSummaries = append(roundSummaries, summary)

		// 累加本轮成功段到对应模式的 resolved 集合（跨轮增量）。
		// 单段场景下集合退化为空或 {targetDocIdx}；第二个同模式轮据此跳过已 resolved 的目标。
		engine.AccumulateResolved(resolvedByMode, round.Mode, result.Resolved)

		// Run duplicate-source-divergence check after the last translate round.
		if roundIdx == lastTranslateRoundIdx && duplicateSourceDivergenceEnabled(engineCfg.QA) {
			divergenceIssues := r.runDuplicateSourceDivergence(doc, targetDocIdx)
			if len(divergenceIssues) > 0 {
				existing := doc.Segments[targetDocIdx].Issues
				merged, _ := replaceQualityIssuesByCode(existing, divergenceIssues, qa.CodeDuplicateSourceDivergence)
				doc.Segments[targetDocIdx].Issues = merged
			}
		}
	}

	// Collect metering metrics.
	metrics := CollectMeterMetrics(eng)

	// Build the final result.
	target := doc.Segments[targetDocIdx]
	targetText := target.Target
	finalIssues := target.Issues

	resultStatus := derivePreviewStatus(targetText, roundSummaries, warnings)

	return &service.PreviewResult{
		Status:        resultStatus,
		SegmentID:     targetSeg.ID,
		SourceText:    previewSource,
		TargetText:    targetText,
		QualityIssues: finalIssues,
		Baseline:      baseline,
		Snapshot:      snapshot,
		Metrics:       metrics,
		Collector:     collector,
		RoundSummary:  roundSummaries,
		Warnings:      warnings,
	}, nil
}

func (r *PreviewRunner) buildTranslateBatchHandler(
	doc *pipeline.Document,
	qaEngine *qa.Engine,
	engineCfg *engine.Config,
	targetDocIdx int,
) func(ctx context.Context, batchResult pipeline.BatchResult) error {
	return buildTranslateBatchHandlerCommon(doc, qaEngine, engineCfg, targetDocIdx)
}

func (r *PreviewRunner) buildAdjudicateBatchHandler(
	doc *pipeline.Document,
	targetDocIdx int,
) func(ctx context.Context, batchResult pipeline.BatchResult) error {
	return buildAdjudicateBatchHandlerCommon(doc, targetDocIdx)
}

func (r *PreviewRunner) buildSemanticQABatchHandler(
	doc *pipeline.Document,
	targetDocIdx int,
) func(ctx context.Context, batchResult pipeline.BatchResult) error {
	return buildSemanticQABatchHandlerCommon(doc, targetDocIdx)
}

func (r *PreviewRunner) buildCorrectBatchHandler(
	doc *pipeline.Document,
	targetDocIdx int,
) func(ctx context.Context, batchResult pipeline.BatchResult) error {
	return buildCorrectBatchHandlerCommon(doc, targetDocIdx)
}

func (r *PreviewRunner) runDuplicateSourceDivergence(doc *pipeline.Document, targetDocIdx int) []qa.QualityIssue {
	inputs := make([]qa.CheckInput, 0, len(doc.Segments))
	for i, seg := range doc.Segments {
		inputs = append(inputs, qa.CheckInput{
			Index:      i, // doc array index; matches targetDocIdx filter below
			SourceText: seg.Source,
			TargetText: seg.Target,
		})
	}
	allIssues := qa.CheckDuplicateSourceDivergence(inputs)
	var targetIssues []qa.QualityIssue
	for _, iss := range allIssues {
		if iss.SegmentIndex == targetDocIdx {
			targetIssues = append(targetIssues, iss)
		}
	}
	return targetIssues
}

func (r *PreviewRunner) buildPreviewGlossary(ctx context.Context, projectRow *ent.Project, enabled bool) glossary.Glossary {
	if !enabled {
		return glossary.Nop{}
	}
	base, err := service.NewDatabaseGlossary(ctx, r.client, projectRow)
	if err != nil {
		r.logger.Warn("preview: failed to load database glossary, using empty base", "err", err)
		return preview.NewOverlayGlossary(nil)
	}
	return preview.NewOverlayGlossary(base)
}

func (r *PreviewRunner) buildPreviewTM(enabled bool) tm.TranslationMemory {
	if !enabled {
		return preview.NoopTM{}
	}
	return preview.NoopTM{}
}

// buildSegmentInputs converts DB segments to pipeline.SegmentInput, applying
// sourceOverride for the target segment if provided.
func (r *PreviewRunner) buildSegmentInputs(rows []*ent.Segment, targetIdx int, sourceOverride string) []pipeline.SegmentInput {
	inputs := make([]pipeline.SegmentInput, len(rows))
	for i, row := range rows {
		var meta map[string]any
		if row.Meta != nil {
			_ = json.Unmarshal([]byte(*row.Meta), &meta)
		}
		target := ""
		if row.TargetText != nil {
			target = *row.TargetText
		}
		source := row.SourceText
		issues := row.QualityIssues
		status := string(row.Status)
		if i == targetIdx && sourceOverride != "" {
			source = sourceOverride
			target = ""
			issues = nil
			status = string(service.SegmentStatusPending)
		}
		inputs[i] = pipeline.SegmentInput{
			ID:         strconv.Itoa(row.SegmentIndex),
			SourceText: source,
			Meta:       meta,
			TargetText: target,
			Issues:     issues,
			Status:     status,
		}
	}
	return inputs
}

// buildSegmentInputsFromDoc converts an in-memory Document back to SegmentInputs
// for the next round's document rebuild.
func (r *PreviewRunner) buildSegmentInputsFromDoc(doc *pipeline.Document) []pipeline.SegmentInput {
	inputs := make([]pipeline.SegmentInput, len(doc.Segments))
	for i, seg := range doc.Segments {
		inputs[i] = pipeline.SegmentInput{
			ID:         seg.ID,
			SourceText: seg.Source,
			Meta:       seg.Meta,
			TargetText: seg.Target,
			Issues:     seg.Issues,
			Status:     seg.Status,
		}
	}
	return inputs
}

func cloneIssues(issues []qa.QualityIssue) []qa.QualityIssue {
	if issues == nil {
		return nil
	}
	out := make([]qa.QualityIssue, len(issues))
	copy(out, issues)
	return out
}

func derivePreviewStatus(targetText string, summaries []service.PreviewRoundSummary, warnings []string) string {
	if targetText == "" {
		return "failed"
	}
	for _, s := range summaries {
		// "failed"（轮次执行报错）与 "partial"（批次/段落未完全成功，如 semantic_qa
		// 解析失败落 unresolved、translate 段失败）均应传导为整体 partial。
		if s.Status == "failed" || s.Status == "partial" {
			return "partial"
		}
	}
	if len(warnings) > 0 {
		return "partial"
	}
	return "success"
}
