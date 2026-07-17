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

	// Prepare document
	e.PrepareDocument(doc, nil)
	if len(cfg.segmentFilter) > 0 {
		applySegmentSelection(doc, cfg.segmentFilter)
	}

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

	e.logger.Info("execute round done",
		"round", roundIdx,
		"mode", handler.ModeName(),
		"segments", len(doc.Segments),
		"unresolved", roundResult.UnresolvedCount,
		"duration", time.Since(start).Round(time.Millisecond))

	_ = result // RunRound 的 Unresolved 已通过 Finalize 写入 doc.Vars

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
