package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
)

// buildProtector 根据配置构建 protector 组合。
func (e *Engine) buildProtector() protect.Protector {
	pc := e.cfg.Pipeline
	var ps []protect.Protector
	if pc.Protect.Ruby.Enabled {
		ps = append(ps, &protect.RubyProtector{})
	}
	ps = append(ps, protect.FromRules(pc.Protect.Rules))
	return protect.Compose(ps...)
}

// BuildPreStages 构建翻译前处理管道：Protect + Bootstrap。
// Web 场景 skipSplit=true（segments 已分割完毕）。
// 返回管道和 limiter；调用方必须 defer limiter.Close()。
func (e *Engine) BuildPreStages(skipSplit bool) (*pipeline.Pipeline, backend.RateLimiter) {
	pc := e.cfg.Pipeline
	protector := e.buildProtector()
	limiter := backend.NewRateLimiter(pc.Translate.RateLimitPerSec)
	retry := backend.RetryPolicy{
		MaxAttempts: pc.Translate.Retry.MaxAttempts,
		Backoff:     time.Duration(pc.Translate.Retry.BackoffMs) * time.Millisecond,
		Jitter:      pc.Translate.Retry.Jitter,
	}
	repairOpts := toRepairOptions(pc.Translate.Repair)

	var s []pipeline.Stage
	if pc.Protect.Enabled {
		s = append(s, pipeline.NewProtect(protector))
	}
	if e.standaloneBootstrap != nil && e.standaloneBootstrap.Enabled && e.bootstrapRenderer != nil {
		s = append(s, &pipeline.Bootstrap{
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
		})
	}
	return pipeline.New(e.logger, s...), limiter
}

// BuildTranslateStage 构建纯翻译管道（仅 Translate 阶段）。
// 返回管道和 limiter；调用方必须 defer limiter.Close()。
func (e *Engine) BuildTranslateStage() (*pipeline.Pipeline, backend.RateLimiter) {
	pc := e.cfg.Pipeline
	limiter := backend.NewRateLimiter(pc.Translate.RateLimitPerSec)
	retry := backend.RetryPolicy{
		MaxAttempts: pc.Translate.Retry.MaxAttempts,
		Backoff:     time.Duration(pc.Translate.Retry.BackoffMs) * time.Millisecond,
		Jitter:      pc.Translate.Retry.Jitter,
	}
	inlineBootstrap := e.cfg.Glossary.Enabled && e.cfg.Glossary.Bootstrap.Enabled
	repairOpts := toRepairOptions(pc.Translate.Repair)

	translateStage := &pipeline.Translate{
		Rounds:                 e.rounds,
		Renderer:               e.renderer,
		Glossary:               e.glossary,
		TM:                     e.tm,
		Limiter:                limiter,
		Retry:                  retry,
		Logger:                 e.logger,
		Reporter:               e.reporter,
		InlineBootstrap:        inlineBootstrap,
		MaxTermsPer1000Chars:   e.cfg.Glossary.Bootstrap.MaxTermsPer1000Chars,
		MinBootstrapSourceLen:  e.cfg.Glossary.Bootstrap.MinSourceLen,
		InlineConflictStrategy: e.cfg.Glossary.Bootstrap.InlineConflictStrategy,
		Repair:                 repairOpts,
		RubyOutputFormat:       pc.Protect.Ruby.OutputFormat,
		Context:                pc.Context,
	}
	return pipeline.New(e.logger, translateStage), limiter
}

// BuildPostStages 构建翻译后处理管道：Unprotect + RubyRestore。
func (e *Engine) BuildPostStages() *pipeline.Pipeline {
	pc := e.cfg.Pipeline
	protector := e.buildProtector()
	retry := backend.RetryPolicy{
		MaxAttempts: pc.Translate.Retry.MaxAttempts,
		Backoff:     time.Duration(pc.Translate.Retry.BackoffMs) * time.Millisecond,
		Jitter:      pc.Translate.Retry.Jitter,
	}

	var s []pipeline.Stage
	if pc.Protect.Enabled {
		s = append(s, pipeline.NewUnprotect(protector))
	}
	if pc.Protect.Ruby.Enabled {
		restorer := protect.NewRubyRestorer(pc.Protect.Ruby.OutputFormat)
		s = append(s, pipeline.NewRubyRestore(restorer, e.logger, e.rubyRetryBackends, retry, pc.Protect.Ruby.OutputFormat, pc.Protect.Ruby.PreserveKinds))
	}
	return pipeline.New(e.logger, s...)
}

// PrepareDocument 设置语言、Vars、段落选择等公共配置。
// 在调用 BuildXxxStages 之前调用。
func (e *Engine) PrepareDocument(doc *pipeline.Document, segmentIndexes []int) {
	if doc == nil {
		return
	}
	selectedSegments := selectedSegmentIndexSet(segmentIndexes)
	if len(selectedSegments) > 0 {
		applySegmentSelection(doc, selectedSegments)
	}
	doc.SourceLang = firstNonEmpty(doc.SourceLang, e.cfg.SourceLang)
	doc.TargetLang = firstNonEmpty(doc.TargetLang, e.cfg.TargetLang)
	if doc.Vars == nil {
		doc.Vars = map[string]any{}
	}
	for k, v := range e.cfg.Prompt.Vars {
		if _, exists := doc.Vars[k]; !exists {
			doc.Vars[k] = v
		}
	}
}

// TranslateSegments 对已解析的 Document 执行纯翻译，不涉及解析和渲染。
//
// 使用场景：
//   - Web：从 DB 加载 segments → 构建 Document → 调用此方法 → 写回 DB
//   - 测试：直接构造 Document 进行翻译测试
func (e *Engine) TranslateSegments(ctx context.Context, input pipeline.TranslateSegmentsInput) (pipeline.TranslateResult, error) {
	start := time.Now()
	var result pipeline.TranslateResult

	doc := input.Document
	if doc == nil {
		return result, fmt.Errorf("engine: document is nil")
	}

	if len(doc.Segments) == 0 {
		return result, nil
	}

	e.PrepareDocument(doc, input.SegmentIndexes)

	e.logger.Info("translate segments start",
		"segments", len(doc.Segments),
		"source_lang", doc.SourceLang,
		"target_lang", doc.TargetLang)

	prePipe, preLimiter := e.BuildPreStages(true)
	defer preLimiter.Close()
	e.logger.Info("pipeline start", "stages", stageNames(prePipe.Stages()))
	if err := prePipe.Run(ctx, doc); err != nil {
		return result, err
	}

	translatePipe, translateLimiter := e.BuildTranslateStage()
	defer translateLimiter.Close()
	if err := translatePipe.Run(ctx, doc); err != nil {
		return result, err
	}

	postPipe := e.BuildPostStages()
	if err := postPipe.Run(ctx, doc); err != nil {
		return result, err
	}

	result = pipeline.TranslateResultFromDocument(doc)

	if len(input.SegmentIndexes) > 0 {
		restoreUnselectedTargets(doc, selectedSegmentIndexSet(input.SegmentIndexes), input.ExistingTargets)
	}

	e.maybeSaveGlossary(ctx)

	e.logger.Info("translate segments done",
		"segments", len(doc.Segments),
		"unresolved", result.UnresolvedCount,
		"duration", time.Since(start).Round(time.Millisecond))

	return result, nil
}
