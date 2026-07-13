package engine

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// TranslateRound 执行单轮翻译。
func (e *Engine) TranslateRound(ctx context.Context, roundIdx int, doc *pipeline.Document, opts ...TranslateOption) (pipeline.TranslateResult, error) {
	start := time.Now()

	if roundIdx >= len(e.rounds) {
		return pipeline.TranslateResult{}, fmt.Errorf("engine: round %d out of range", roundIdx)
	}

	cfg := &translateConfig{}
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

	// Prepare document
	e.PrepareDocument(doc, nil)
	if len(cfg.segmentFilter) > 0 {
		applySegmentSelection(doc, cfg.segmentFilter)
	}

	e.logger.Info("translate round start",
		"round", roundIdx,
		"name", round.Name,
		"segments", len(doc.Segments),
		"source_lang", doc.SourceLang,
		"target_lang", doc.TargetLang)

	inlineBootstrap := e.cfg.Glossary.Enabled && e.cfg.Glossary.Bootstrap.Enabled
	repairOpts := e.cfg.Repair

	executor := &pipeline.RoundExecutor{
		Round:                  round,
		Renderer:               round.Renderer,
		Glossary:               e.glossary,
		TM:                     e.tm,
		Logger:                 e.logger,
		Reporter:               e.reporter,
		RubyRestorer:           e.rubyRestorer,
		RubyRetryBackends:      e.rubyRetryBackends,
		InlineBootstrap:        inlineBootstrap,
		MaxTermsPer1000Chars:   e.cfg.Glossary.Bootstrap.MaxTermsPer1000Chars,
		MinBootstrapSourceLen:  e.cfg.Glossary.Bootstrap.MinSourceLen,
		InlineConflictStrategy: e.cfg.Glossary.Bootstrap.InlineConflictStrategy,
		Repair:                 repairOpts,
		Context:                pipeline.DefaultContextConfig(),
		BatchHandler:           cfg.batchHandler,
	}

	if err := executor.Run(ctx, doc); err != nil {
		return pipeline.TranslateResult{}, err
	}

	result := buildTranslateResult(doc)
	result.InputTokens = atomic.LoadInt64(&doc.InputTokens)
	result.OutputTokens = atomic.LoadInt64(&doc.OutputTokens)

	e.logger.Info("translate round done",
		"round", roundIdx,
		"name", round.Name,
		"segments", len(doc.Segments),
		"unresolved", result.UnresolvedCount,
		"duration", time.Since(start).Round(time.Millisecond))

	return result, nil
}

// buildTranslateResult 从实际段落状态构建翻译结果。
func buildTranslateResult(doc *pipeline.Document) pipeline.TranslateResult {
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
