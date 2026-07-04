package engine

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// Translate 是 Engine 的统一入口。
// 调用方负责：Parse（CLI）或 BuildDocumentFromSegments（Worker）构造 *pipeline.Document。
// Engine 负责：Bootstrap(可选) → Protect 全文 → Pipeline(分批 + 翻译) → Unprotect 全文 → RubyRestore。
func (e *Engine) Translate(ctx context.Context, doc *pipeline.Document, opts ...TranslateOption) (pipeline.TranslateResult, error) {
	start := time.Now()

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

	// 1. Prepare document (language, vars, segment filter)
	e.PrepareDocument(doc, nil)
	if len(cfg.segmentFilter) > 0 {
		applySegmentSelection(doc, cfg.segmentFilter)
	}

	e.logger.Info("translate start",
		"segments", len(doc.Segments),
		"source_lang", doc.SourceLang,
		"target_lang", doc.TargetLang)

	// 2. Optional Bootstrap (global, before batch processing)
	if e.standaloneBootstrap != nil && e.standaloneBootstrap.Enabled && e.bootstrapRenderer != nil {
		bootstrapStage := e.buildBootstrapStage()
		if err := bootstrapStage.Run(ctx, doc); err != nil {
			e.logger.Warn("bootstrap failed, continuing without incremental terms", "err", err)
		}
	}

	// 3. Build processing components
	pc := e.cfg.Pipeline
	protector := e.buildProtector()

	var restorer *ruby.Restorer
	if pc.Ruby.Enabled {
		restorer = ruby.NewRestorer()
	}

	translatePipe := e.BuildTranslateStage(protector, restorer)

	// 4. 设置 batchHandler 并调用 Pipeline
	if cfg.batchHandler != nil {
		translatePipe.SetBatchHandler(cfg.batchHandler)
		defer translatePipe.SetBatchHandler(nil)
	}
	if err := translatePipe.Run(ctx, doc); err != nil {
		return pipeline.TranslateResult{}, err
	}

	// 7. Save glossary if needed
	e.maybeSaveGlossary(ctx)

	// 10. 构建结果
	result := buildTranslateResult(doc)
	result.InputTokens = atomic.LoadInt64(&doc.InputTokens)
	result.OutputTokens = atomic.LoadInt64(&doc.OutputTokens)

	e.logger.Info("translate done",
		"segments", len(doc.Segments),
		"unresolved", result.UnresolvedCount,
		"duration", time.Since(start).Round(time.Millisecond))

	return result, nil
}

// buildTranslateResult 从实际段落状态构建翻译结果。
func buildTranslateResult(doc *pipeline.Document) pipeline.TranslateResult {
	// 从 doc.Vars 解析失败索引
	failedSet := pipeline.ParseFailedIndices(doc.Vars)

	result := pipeline.TranslateResult{
		SegmentCount:    len(doc.Segments),
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

// buildBootstrapStage constructs the Bootstrap stage.
func (e *Engine) buildBootstrapStage() *pipeline.Bootstrap {
	pc := e.cfg.Pipeline
	retry := backend.RetryPolicy{
		MaxAttempts: pc.Translate.Retry.MaxAttempts,
		Backoff:     time.Duration(pc.Translate.Retry.BackoffMs) * time.Millisecond,
		Jitter:      pc.Translate.Retry.Jitter,
	}
	repairOpts := toRepairOptions(pc.Translate.Repair)
	return &pipeline.Bootstrap{
		Backends:         e.bootstrapBackends,
		Renderer:         e.bootstrapRenderer,
		Glossary:         e.glossary,
		Retry:            retry,
		Concurrency:      e.standaloneBootstrap.Concurrency,
		BatchSize:        e.standaloneBootstrap.BatchSize,
		MaxTermsPerBatch: e.standaloneBootstrap.MaxTermsPerBatch,
		MinSourceLen:     e.standaloneBootstrap.MinSourceLen,
		Logger:           e.logger,
		Reporter:         e.reporter,
		Repair:           repairOpts,
	}
}
