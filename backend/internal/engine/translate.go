package engine

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// ExecuteRound 执行单轮（翻译或抽取）。
func (e *Engine) ExecuteRound(ctx context.Context, roundIdx int, doc *pipeline.Document, opts ...ExecuteOption) (pipeline.TranslateResult, error) {
	start := time.Now()

	if roundIdx >= len(e.rounds) {
		return pipeline.TranslateResult{}, fmt.Errorf("engine: round %d out of range", roundIdx)
	}

	cfg := &executeConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	if doc == nil {
		return pipeline.TranslateResult{}, fmt.Errorf("engine: document is nil")
	}
	if len(doc.Segments) == 0 {
		return pipeline.TranslateResult{}, nil
	}

	round := e.rounds[roundIdx]
	handler := round.Handler

	// 流水线注入：站位信号量与暂停闸门（每次执行按选项覆盖；nil 保持单资源
	// 语义）。Round 为值类型副本，不污染引擎持有的基准配置。
	round.Slots = cfg.station
	round.Gate = cfg.gate
	// 退避重试等待中止信号同步到 handler（各 LLM handler 的 backoff select）。
	if cfg.gate != nil {
		if setter, ok := handler.(interface{ SetGate(*pipeline.PauseGate) }); ok {
			setter.SetGate(cfg.gate)
		}
	}

	// Prepare document
	e.PrepareDocument(doc, nil)
	if len(cfg.segmentFilter) > 0 {
		applySegmentSelection(doc, cfg.segmentFilter)
	}
	// 注入跨轮增量载体（非翻译轮据此排除已解决段）。
	// 无条件赋值（含 nil/空）：doc 跨轮共享时，清空上一轮残留，避免跨模式污染
	// （否则前一同模式轮注入的非空集合会被后续不同模式首轮继承，致其静默跳过段落）。
	// translate 轮不调用 WithResolvedIndices → cfg.resolvedIndices=nil → 清空；其 BuildBatches 不读此字段。
	doc.ResolvedIndices = cfg.resolvedIndices

	e.logger.Info("execute round start",
		"round", roundIdx,
		"mode", handler.ModeName(),
		"segments", len(doc.Segments),
		"source_lang", doc.SourceLang,
		"target_lang", doc.TargetLang)

	// 运行轮次
	result, err := pipeline.RunRound(ctx, round, doc, cfg.batchHandler, e.logger, e.reporter)
	if err != nil {
		return pipeline.TranslateResult{}, err
	}

	// 构建结果
	roundResult := buildRoundResult(doc)
	roundResult.InputTokens = atomic.LoadInt64(&doc.InputTokens)
	roundResult.OutputTokens = atomic.LoadInt64(&doc.OutputTokens)
	roundResult.FailedBatchCount = result.FailedBatches
	roundResult.FailedSegmentCount = len(result.FailedSegments)
	roundResult.Resolved = result.Resolved
	roundResult.Unresolved = result.Unresolved

	e.logger.Info("execute round done",
		"round", roundIdx,
		"mode", handler.ModeName(),
		"segments", len(doc.Segments),
		"unresolved", roundResult.UnresolvedCount,
		"failed_batches", roundResult.FailedBatchCount,
		"failed_segments", roundResult.FailedSegmentCount,
		"duration", time.Since(start).Round(time.Millisecond))

	return roundResult, nil
}

// buildRoundResult 从实际段落状态构建翻译结果。
func buildRoundResult(doc *pipeline.Document) pipeline.TranslateResult {
	failedSet := pipeline.ParseFailedIndices(doc.Vars)
	skippedCount := 0
	if v, ok := doc.Vars["_skipped_count"].(int); ok {
		skippedCount = v
	}

	result := pipeline.TranslateResult{
		SegmentCount:    len(doc.Segments),
		SkippedCount:    skippedCount,
		UnresolvedCount: len(failedSet),
	}
	result.Segments = make([]pipeline.SegmentResult, len(doc.Segments))
	for i, seg := range doc.Segments {
		source := seg.OriginalSource
		if source == "" {
			source = seg.Source
		}
		_, isFailed := failedSet[i]
		result.Segments[i] = pipeline.SegmentResult{
			Index:      i,
			SourceText: source,
			TargetText: seg.Target,
			Failed:     isFailed,
		}
	}
	return result
}
