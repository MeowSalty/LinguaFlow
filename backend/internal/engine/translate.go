package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
)

// Translate 是 Engine 的统一入口。
// 调用方负责：Parse（CLI）或 BuildDocumentFromSegments（Worker）构造 *pipeline.Document。
// Engine 负责：Bootstrap(可选) → 批级并发 { Protect → Translate → Unprotect → RubyRestore → BatchHandler }。
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

	// 3. Build batch-level processing components
	pc := e.cfg.Pipeline
	protector := e.buildProtector()
	translatePipe, translateLimiter := e.BuildTranslateStage()
	defer translateLimiter.Close()

	var restorer *protect.RubyRestorer
	if pc.Protect.Ruby.Enabled {
		restorer = protect.NewRubyRestorer(pc.Protect.Ruby.OutputFormat)
	}

	// 4. Collect pending segments and build batches
	batchSize := e.resolveBatchSize()
	concurrency := e.resolveConcurrency()

	var pending []int
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		if seg.Skip || isSegmentEmpty(seg) {
			seg.Target = seg.Source
			continue
		}
		pending = append(pending, i)
	}

	if len(pending) == 0 {
		return pipeline.TranslateResultFromDocument(doc), nil
	}

	batches := buildBatches(pending, batchSize)
	e.logger.Info("batch processing",
		"pending", len(pending),
		"batches", len(batches),
		"batch_size", batchSize,
		"concurrency", concurrency)

	// 5. Run batches concurrently
	var mu sync.Mutex
	var batchResults []pipeline.BatchResult
	var totalFailed int

	err := pipeline.RunConcurrent(ctx, len(batches), concurrency, func(ctx context.Context, bidx int) error {
		idxs := batches[bidx]

		// 深拷贝 Vars 避免并发写入同一个 map
		varsCopy := make(map[string]any, len(doc.Vars))
		for k, v := range doc.Vars {
			varsCopy[k] = v
		}

		// 深拷贝段落，独立拷贝 Protected/Meta map 避免竞态
		batchSegs := make([]pipeline.Segment, len(idxs))
		for i, idx := range idxs {
			orig := doc.Segments[idx]
			seg := orig // 值拷贝基本字段
			if orig.Protected != nil {
				seg.Protected = make(map[string]string, len(orig.Protected))
				for k, v := range orig.Protected {
					seg.Protected[k] = v
				}
			}
			if orig.Meta != nil {
				seg.Meta = make(map[string]any, len(orig.Meta))
				for k, v := range orig.Meta {
					seg.Meta[k] = v
				}
			}
			batchSegs[i] = seg
		}

		// 预注入批边界上下文（从原始 doc 提取相邻段落）
		firstIdx, lastIdx := idxs[0], idxs[len(idxs)-1]
		prevCtx, nextCtx := pipeline.BuildContextRange(doc, firstIdx, lastIdx)

		batchDoc := &pipeline.Document{
			Segments:   batchSegs,
			SourceLang: doc.SourceLang,
			TargetLang: doc.TargetLang,
			Format:     doc.Format,
			Vars:       varsCopy,
		}
		// 将上下文注入 Vars 供 prompt 渲染使用
		if prevCtx != "" {
			batchDoc.Vars["_batch_prev_context"] = prevCtx
		}
		if nextCtx != "" {
			batchDoc.Vars["_batch_next_context"] = nextCtx
		}

		// 5a. Protect
		if pc.Protect.Enabled {
			for i := range batchDoc.Segments {
				seg := &batchDoc.Segments[i]
				if seg.OriginalSource == "" {
					seg.OriginalSource = seg.Source
				}
				if err := protector.Protect(seg); err != nil {
					return fmt.Errorf("protect batch %d: %w", bidx, err)
				}
			}
		}

		// 5b. Translate (using existing pipeline)
		if err := translatePipe.Run(ctx, batchDoc); err != nil {
			return fmt.Errorf("translate batch %d: %w", bidx, err)
		}

		// 5c. Unprotect
		if pc.Protect.Enabled {
			for i := range batchDoc.Segments {
				if err := protector.Unprotect(&batchDoc.Segments[i]); err != nil {
					return fmt.Errorf("unprotect batch %d: %w", bidx, err)
				}
			}
		}

		// 5d. RubyRestore
		if restorer != nil {
			for i := range batchDoc.Segments {
				seg := &batchDoc.Segments[i]
				rubyOutput := extractRubyOutput(seg)
				if len(rubyOutput) > 0 {
					originals := extractRubyAnnotations(seg)
					_ = restorer.Restore(seg, rubyOutput, originals)
				}
			}
		}

		// 5e. 统计本批失败段数并拷贝结果回原始 doc
		localFailed := 0
		for i, idx := range idxs {
			doc.Segments[idx].Target = batchDoc.Segments[i].Target
			doc.Segments[idx].OriginalSource = batchDoc.Segments[i].OriginalSource
			if batchDoc.Segments[i].Target == "" {
				localFailed++
			}
			// 合并 Meta 变更（如 ruby_output）
			if batchDoc.Segments[i].Meta != nil {
				if doc.Segments[idx].Meta == nil {
					doc.Segments[idx].Meta = make(map[string]any)
				}
				for k, v := range batchDoc.Segments[i].Meta {
					doc.Segments[idx].Meta[k] = v
				}
			}
		}

		mu.Lock()
		totalFailed += localFailed
		mu.Unlock()

		// 5f. Build BatchResult and call handler
		translated := make([]pipeline.TranslatedSegment, len(idxs))
		for i, idx := range idxs {
			seg := doc.Segments[idx]
			source := seg.OriginalSource
			if source == "" {
				source = seg.Source
			}
			translated[i] = pipeline.TranslatedSegment{
				Index:      idx,
				ID:         seg.ID,
				SourceText: source,
				TargetText: seg.Target,
				Failed:     seg.Target == "",
				Meta:       seg.Meta,
			}
		}
		batchResult := pipeline.BatchResult{
			Segments:   translated,
			BatchIndex: bidx,
		}

		mu.Lock()
		batchResults = append(batchResults, batchResult)
		mu.Unlock()

		// 5g. Call BatchHandler
		if cfg.batchHandler != nil {
			if err := cfg.batchHandler(ctx, batchResult); err != nil {
				return fmt.Errorf("batch handler batch %d: %w", bidx, err)
			}
		}

		return nil
	})
	if err != nil {
		return pipeline.TranslateResult{}, err
	}

	// 6. Save glossary if needed
	e.maybeSaveGlossary(ctx)

	// 从实际段落状态计算结果（不依赖 Vars 中的不确定值）
	result := buildTranslateResult(doc, totalFailed)

	e.logger.Info("translate done",
		"segments", len(doc.Segments),
		"unresolved", result.UnresolvedCount,
		"duration", time.Since(start).Round(time.Millisecond))

	return result, nil
}

// buildTranslateResult 从实际段落状态构建翻译结果。
// 不依赖 Vars 中的 _translate_unresolved_count / _translate_failed_indices，
// 避免并发批写入导致的不确定性。
func buildTranslateResult(doc *pipeline.Document, totalFailed int) pipeline.TranslateResult {
	result := pipeline.TranslateResult{
		SegmentCount:    len(doc.Segments),
		UnresolvedCount: totalFailed,
	}
	result.Segments = make([]pipeline.SegmentResult, len(doc.Segments))
	for i, seg := range doc.Segments {
		source := seg.OriginalSource
		if source == "" {
			source = seg.Source
		}
		result.Segments[i] = pipeline.SegmentResult{
			Index:      i,
			SourceText: source,
			TargetText: seg.Target,
			Failed:     seg.Target == "" && !seg.Skip,
		}
	}
	return result
}

// buildBootstrapStage constructs the Bootstrap stage.
func (e *Engine) buildBootstrapStage() *pipeline.Bootstrap {
	pc := e.cfg.Pipeline
	limiter := backend.NewRateLimiter(pc.Translate.RateLimitPerSec)
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
		Limiter:          limiter,
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

// resolveBatchSize returns the batch size from the first round.
func (e *Engine) resolveBatchSize() int {
	if len(e.rounds) > 0 && e.rounds[0].BatchSize > 0 {
		return e.rounds[0].BatchSize
	}
	return 1
}

// resolveConcurrency returns the concurrency from the first round.
func (e *Engine) resolveConcurrency() int {
	if len(e.rounds) > 0 && e.rounds[0].Concurrency > 0 {
		return e.rounds[0].Concurrency
	}
	return 1
}

// isSegmentEmpty checks if a segment has no translatable content.
func isSegmentEmpty(seg *pipeline.Segment) bool {
	if seg.Skip {
		return true
	}
	t := seg.Source
	if t == "" {
		return true
	}
	for _, r := range t {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// buildBatches splits pending indices into batches of the given size.
func buildBatches(pending []int, batchSize int) [][]int {
	if len(pending) == 0 {
		return nil
	}
	batchSize = max(batchSize, 1)
	var batches [][]int
	for i := 0; i < len(pending); i += batchSize {
		end := min(i+batchSize, len(pending))
		batches = append(batches, pending[i:end])
	}
	return batches
}

// extractRubyOutput extracts ruby_output from segment Meta.
func extractRubyOutput(seg *pipeline.Segment) []protect.RubyOutputEntry {
	if seg.Meta == nil {
		return nil
	}
	raw, ok := seg.Meta["ruby_output"]
	if !ok {
		return nil
	}
	entries, ok := raw.([]protect.RubyOutputEntry)
	if !ok {
		return nil
	}
	return entries
}

// extractRubyAnnotations extracts ruby_annotations from segment Meta.
func extractRubyAnnotations(seg *pipeline.Segment) []protect.RubyAnnotation {
	if seg.Meta == nil {
		return nil
	}
	raw, ok := seg.Meta["ruby_annotations"]
	if !ok {
		return nil
	}
	annots, ok := raw.([]protect.RubyAnnotation)
	if !ok {
		return nil
	}
	return annots
}
