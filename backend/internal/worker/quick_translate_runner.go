package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/preview"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// QuickTranslateRunner 执行单段即时翻译，不触碰 DB 段落/资源。
// 镜像 PreviewRunner.RunPreview，但输入为裸文本，无 baseline/apply-token/sourceOverride。
type QuickTranslateRunner struct {
	logger  *slog.Logger
	client  *ent.Client
	factory *EngineFactory
}

// NewQuickTranslateRunner 创建 QuickTranslateRunner。
func NewQuickTranslateRunner(logger *slog.Logger, client *ent.Client, limiterPool *backend.LimiterPool) *QuickTranslateRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &QuickTranslateRunner{
		logger:  logger,
		client:  client,
		factory: NewEngineFactory(logger, limiterPool),
	}
}

// Run 执行单段即时翻译。输入来自 QuickTranslateService 已组装好的 snapshot 与术语表，
// 构造单段内存 Document，跑完所有轮次后返回纯内存结果。
func (r *QuickTranslateRunner) Run(ctx context.Context, in service.QuickTranslateRunnerInput) (*service.QuickTranslateResult, error) {
	if len(in.Snapshot.Rounds) == 0 {
		return nil, errors.New("quick translate: execution plan has no rounds")
	}

	hasTranslate := false
	for _, rs := range in.Snapshot.Rounds {
		if rs.Mode == "translate" {
			hasTranslate = true
			break
		}
	}
	if !hasTranslate {
		return nil, errors.New("quick translate: execution plan must have at least one translate round")
	}

	engineCfg := BuildEngineConfig(in.Snapshot)

	var qaEngine *qa.Engine
	if engineCfg.QA.Enabled {
		qaCfg := engineCfg.QA
		qaCfg.Glossary = in.Glossary
		qaCfg.Format = in.Format
		qaEngine = qa.NewEngine(qaCfg, r.logger)
	}

	collector := preview.NewMemoryCollector()
	resources := engine.RuntimeResources{Glossary: in.Glossary, TM: preview.NoopTM{}}
	eng, err := r.factory.BuildEngine(ctx, in.Snapshot, resources, collector)
	if err != nil {
		return nil, fmt.Errorf("quick translate: build engine: %w", err)
	}
	defer func() { _ = eng.Close() }()

	// 构造单段内存 Document。
	inputs := []pipeline.SegmentInput{{
		ID:         prompt.SingleID,
		SourceText: in.SourceText,
		Status:     string(service.SegmentStatusPending),
	}}
	doc := pipeline.BuildDocumentFromSegments(inputs, in.SourceLang, in.TargetLang, in.Format)
	targetDocIdx := 0
	doc.Segments[0].Translate = true

	// 确定首个 translate 轮索引（即时翻译单段，不执行多段同文异译检查，
	// 故无需 lastTranslateRoundIdx）。
	firstTranslateRoundIdx := -1
	for i := range in.Snapshot.Rounds {
		if in.Snapshot.Rounds[i].Mode == "translate" {
			if firstTranslateRoundIdx < 0 {
				firstTranslateRoundIdx = i
			}
		}
	}

	// 跨轮增量载体（in-memory）：per-mode 已解决段索引集合。
	// 与 JobRunner/PreviewRunner 保持一致；单段场景下集合退化为空或 {targetDocIdx}。
	resolvedByMode := engine.NewResolvedByMode()

	var warnings []string
	var roundSummaries []service.PreviewRoundSummary

	// 轮次循环。
	for roundIdx := range in.Snapshot.Rounds {
		if ctx.Err() != nil {
			warnings = append(warnings, "quick translate cancelled during round execution")
			break
		}

		round := in.Snapshot.Rounds[roundIdx]
		roundStart := time.Now()

		// 从当前内存状态重建 Document（使裁决/语义 QA 结果对后续轮次可见）。
		if roundIdx > 0 {
			prev := doc.Segments[0]
			inputs := []pipeline.SegmentInput{{
				ID:         prev.ID,
				SourceText: prev.Source,
				Meta:       prev.Meta,
				TargetText: prev.Target,
				Issues:     prev.Issues,
				Status:     prev.Status,
			}}
			doc = pipeline.BuildDocumentFromSegments(inputs, in.SourceLang, in.TargetLang, in.Format)
		}

		// 决定目标段是否本轮翻译。
		var segmentIndexes []int
		skippedThisRound := false
		switch round.Mode {
		case "translate":
			if roundIdx == firstTranslateRoundIdx {
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
					doc.Segments[targetDocIdx].Translate = false
					segmentIndexes = nil
					skippedThisRound = true
				}
			}
		default:
			// extract/adjudicate/semantic_qa/revise：始终处理目标段。
			doc.Segments[targetDocIdx].Translate = true
			segmentIndexes = []int{targetDocIdx}
		}

		// 构建本轮 batch handler。
		var batchHandler func(ctx context.Context, batchResult pipeline.BatchResult) error
		switch round.Mode {
		case "translate":
			batchHandler = buildTranslateBatchHandlerCommon(doc, qaEngine, engineCfg, targetDocIdx)
		case "adjudicate":
			batchHandler = buildAdjudicateBatchHandlerCommon(doc, targetDocIdx)
		case "semantic_qa":
			batchHandler = buildSemanticQABatchHandlerCommon(doc, targetDocIdx)
		case "revise":
			// 写回时移除的 issue 集合与送进 prompt 的目标集合严格一致
			//（计划校验保证只能是语义 code）。
			var reviseCodes []string
			if round.Revise != nil {
				reviseCodes = round.Revise.IssueCodes
			}
			batchHandler = buildReviseBatchHandlerCommon(doc, qaEngine, targetDocIdx, reviseCodes)
		case "correct":
			batchHandler = buildCorrectBatchHandlerCommon(doc, targetDocIdx)
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
			// translate/adjudicate 失败为终端错误。
			metrics := CollectMeterMetrics(eng)
			return &service.QuickTranslateResult{
				Status:       "failed",
				SourceText:   in.SourceText,
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
		engine.AccumulateResolved(resolvedByMode, round.Mode, result.Resolved)

		// 注意：单段即时翻译不执行 duplicate_source_divergence 检查——
		// 该检查需要多段对比，单段无意义。
		// （见计划 §6.4）
	}

	// 收集用量指标。
	metrics := CollectMeterMetrics(eng)

	// 构建最终结果。
	target := doc.Segments[targetDocIdx]
	targetText := target.Target
	finalIssues := target.Issues

	resultStatus := derivePreviewStatus(targetText, roundSummaries, warnings)

	return &service.QuickTranslateResult{
		Status:        resultStatus,
		SourceText:    in.SourceText,
		TargetText:    targetText,
		QualityIssues: finalIssues,
		RoundSummary:  roundSummaries,
		Metrics:       metrics,
		Collector:     collector,
		Warnings:      warnings,
	}, nil
}

// buildTranslateBatchHandlerCommon 构建翻译轮的 batch handler，对目标段执行 QA 检查
// 并更新内存 Document。PreviewRunner 与 QuickTranslateRunner 共享此实现以避免漂移。
func buildTranslateBatchHandlerCommon(
	doc *pipeline.Document,
	qaEngine *qa.Engine,
	engineCfg *engine.Config,
	targetDocIdx int,
) func(ctx context.Context, batchResult pipeline.BatchResult) error {
	return func(_ context.Context, batchResult pipeline.BatchResult) error {
		var allIssues []qa.QualityIssue
		if qaEngine != nil {
			inputs := buildQACheckInputs(batchResult)
			allIssues = qaEngine.Run(context.Background(), inputs)
		}

		for _, ts := range batchResult.Segments {
			if ts.Index != targetDocIdx {
				continue
			}
			if ts.TargetText == "" {
				continue
			}
			// 合并 pipeline 产出的守恒 issue（如注音还原不完整）与 QA 扫描结果，
			// 与 job_runner 的 translate 轮落库口径一致。新建切片避免对 QA
			// 内部切片的别名依赖；ts.Issues 为 nil 时 append 跳过。
			segIssues := make([]qa.QualityIssue, 0, len(ts.Issues)+4)
			segIssues = append(segIssues, ts.Issues...)
			segIssues = append(segIssues, qa.IssuesFor(ts.Index, allIssues)...)
			doc.Segments[ts.Index].Target = ts.TargetText
			doc.Segments[ts.Index].Issues = segIssues
			segStatus := string(service.SegmentStatusTranslated)
			if qa.HasErrors(segIssues) && engineCfg.QA.AutoReject {
				segStatus = string(service.SegmentStatusRejected)
			}
			doc.Segments[ts.Index].Status = segStatus
		}
		return nil
	}
}

// buildAdjudicateBatchHandlerCommon 构建裁决轮的 batch handler，
// 将裁决结果写入内存 Document 的目标段。两个 runner 共享以避免漂移。
func buildAdjudicateBatchHandlerCommon(
	doc *pipeline.Document,
	targetDocIdx int,
) func(ctx context.Context, batchResult pipeline.BatchResult) error {
	return func(_ context.Context, batchResult pipeline.BatchResult) error {
		for _, ts := range batchResult.Segments {
			if ts.Index != targetDocIdx {
				continue
			}
			if len(ts.Issues) > 0 {
				doc.Segments[ts.Index].Issues = ts.Issues
			}
		}
		return nil
	}
}

// buildSemanticQABatchHandlerCommon 构建语义 QA 轮的 batch handler，
// 将语义 QA 结果合并到内存 Document 的目标段。两个 runner 共享以避免漂移。
func buildSemanticQABatchHandlerCommon(
	doc *pipeline.Document,
	targetDocIdx int,
) func(ctx context.Context, batchResult pipeline.BatchResult) error {
	return func(_ context.Context, batchResult pipeline.BatchResult) error {
		for _, ts := range batchResult.Segments {
			if ts.Index != targetDocIdx {
				continue
			}
			if len(ts.Issues) == 0 {
				continue
			}
			existing := doc.Segments[ts.Index].Issues
			merged := mergeSemanticQAIssues(existing, ts.Issues)
			doc.Segments[ts.Index].Issues = merged
		}
		return nil
	}
}

// buildReviseBatchHandlerCommon 构建 LLM 修订轮的 batch handler，按 correct 轮同款
// 声明式契约（"声明修什么，就移除什么，其余判决不动"）将修订后的译文与最终 issues
// 写回内存 Document：targetedCodes 命中且仍 pending 的 issue 视为本轮已修复而移除；
// dismissed 记录与范围外 pending 一律保留；qaRan（qaEngine 非 nil）时确定性 issue 以
// fresh 重算（ReconcileIssues 按指纹继承旧裁决），范围外语义 issue 不由确定性 QA 维护、
// 原样保留；qaRan=false 时非目标 issue 原样保留。修订是声明性修复，LLM 实际未修复时
// 仅当后续 semantic_qa 轮会重扫该段（scope=all；with_issues/with_issue_codes 作用域
// 会跳过已无 issue 的段落）时才会重新检出；否则与手动编辑/重译清除旧语义 issue 的
// 既有语义一致——译文已变更，旧 issue 视为失效。
// 该 handler 由 RevisionPreviewRunner / PreviewRunner / QuickTranslateRunner 共享；
// 不修改段落状态。
func buildReviseBatchHandlerCommon(
	doc *pipeline.Document,
	qaEngine *qa.Engine,
	targetDocIdx int,
	targetedCodes []string,
) func(ctx context.Context, batchResult pipeline.BatchResult) error {
	return func(ctx context.Context, batchResult pipeline.BatchResult) error {
		for _, ts := range batchResult.Segments {
			if ts.Index != targetDocIdx {
				continue
			}
			// no-op 修订（LLM 判定无需改动，或返回空译文）：跳过写回与任何 issue 变更，完整保留既有 issue。
			if ts.TargetText == "" || ts.TargetText == doc.Segments[ts.Index].Target {
				continue
			}
			doc.Segments[ts.Index].Target = ts.TargetText
			var fresh []qa.QualityIssue
			qaRan := qaEngine != nil
			if qaRan {
				fresh = qa.IssuesFor(ts.Index, qaEngine.Run(ctx, buildQACheckInputs(batchResult)))
			}
			doc.Segments[ts.Index].Issues = qa.ReviseFinalIssues(doc.Segments[ts.Index].Issues, fresh, targetedCodes, qaRan)
		}
		return nil
	}
}

// buildCorrectBatchHandlerCommon 构建本地改写轮的 batch handler，
// 将改写后的译文与 issues 写回内存 Document 的目标段。两个 runner 共享以避免漂移。
func buildCorrectBatchHandlerCommon(
	doc *pipeline.Document,
	targetDocIdx int,
) func(ctx context.Context, batchResult pipeline.BatchResult) error {
	return func(_ context.Context, batchResult pipeline.BatchResult) error {
		for _, ts := range batchResult.Segments {
			if ts.Index != targetDocIdx {
				continue
			}
			if ts.TargetText != "" {
				doc.Segments[ts.Index].Target = ts.TargetText
			}
			// correct handler 总是携带最终 issues（改写过滤后的集合；no-op 段为原 issues 拷贝）。
			// nil = 该段原无 issues 且未改动，跳过；非 nil（含空切片）= 写回以反映已解决。
			if ts.Issues != nil {
				doc.Segments[ts.Index].Issues = ts.Issues
			}
		}
		return nil
	}
}
