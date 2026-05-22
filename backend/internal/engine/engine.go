package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/output"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline/stages"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// Engine 封装一次进程内的翻译能力。它持有 Selector / Renderer 等可复用组件。
type Engine struct {
	cfg               *config.Config
	logger            *slog.Logger
	reporter          progress.Reporter
	selector          backend.Selector
	renderer          *prompt.Renderer
	bootstrapRenderer *prompt.BootstrapRenderer
	glossary          glossary.Glossary
}

// New 按配置构造 Engine。reporter 可为 nil（fallback 为 progress.Nop）。
// 失败时返回 (nil, error)。
func New(cfg *config.Config, logger *slog.Logger, reporter progress.Reporter) (*Engine, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if reporter == nil {
		reporter = progress.Nop{}
	}
	sel, err := backend.NewSelector(cfg.Backends)
	if err != nil {
		return nil, err
	}
	rend, err := prompt.NewRenderer(cfg.Prompt)
	if err != nil {
		_ = sel.Close()
		return nil, err
	}
	glos, err := glossary.New(cfg.Glossary)
	if err != nil {
		_ = sel.Close()
		return nil, fmt.Errorf("engine: build glossary: %w", err)
	}
	e := &Engine{
		cfg:      cfg,
		logger:   logger,
		reporter: reporter,
		selector: sel,
		renderer: rend,
		glossary: glos,
	}
	// 仅在 bootstrap 真要启用时编译模板，避免无关错误干扰常规路径。
	if cfg.Glossary.Enabled && cfg.Glossary.Bootstrap.Enabled {
		br, err := prompt.NewBootstrapRenderer()
		if err != nil {
			_ = sel.Close()
			return nil, fmt.Errorf("engine: build bootstrap renderer: %w", err)
		}
		e.bootstrapRenderer = br
	}
	return e, nil
}

// Close 释放后端连接。
func (e *Engine) Close() error { return e.selector.Close() }

// Translate 执行一次翻译任务。
func (e *Engine) Translate(ctx context.Context, job TranslateJob) error {
	start := time.Now()

	p, err := parser.DetectByExt(job.InputPath)
	if err != nil {
		return err
	}

	f, err := os.Open(job.InputPath)
	if err != nil {
		return fmt.Errorf("engine: open input: %w", err)
	}
	doc, parseErr := p.Parse(ctx, f)
	_ = f.Close()
	if parseErr != nil {
		return fmt.Errorf("engine: parse: %w", parseErr)
	}

	// 语言：CLI flag 优先，再用 config 默认
	doc.SourceLang = firstNonEmpty(job.SourceLang, e.cfg.SourceLang)
	doc.TargetLang = firstNonEmpty(job.TargetLang, e.cfg.TargetLang)
	if doc.Vars == nil {
		doc.Vars = map[string]any{}
	}
	for k, v := range e.cfg.Prompt.Vars {
		if _, exists := doc.Vars[k]; !exists {
			doc.Vars[k] = v
		}
	}

	e.logger.Info("parsed document",
		"path", job.InputPath,
		"format", doc.Format,
		"segments", len(doc.Segments),
		"source_lang", doc.SourceLang,
		"target_lang", doc.TargetLang)

	pipe := e.buildPipeline()
	e.logger.Info("pipeline start", "stages", stageNames(pipe.Stages()))
	if err := pipe.Run(ctx, doc); err != nil {
		return err
	}

	w := output.New(e.cfg.Output, p, job.OutputPath)
	if err := w.Write(ctx, doc); err != nil {
		return err
	}

	// 自举完成后按配置回写术语表。失败仅 warn——译文已写出。
	e.maybeSaveGlossary(ctx)

	e.logger.Info("output written",
		"path", job.OutputPath,
		"segments", len(doc.Segments),
		"duration", time.Since(start).Round(time.Millisecond))
	return nil
}

func (e *Engine) buildPipeline() *pipeline.Pipeline {
	pc := e.cfg.Pipeline

	protector := protect.FromRules(pc.Protect.Rules)
	limiter := backend.NewRateLimiter(pc.Translate.RateLimitPerSec)
	retry := backend.RetryPolicy{
		MaxAttempts: pc.Translate.Retry.MaxAttempts,
		Backoff:     pc.Translate.Retry.Backoff,
	}

	var s []pipeline.Stage
	if pc.Split.Enabled {
		s = append(s, stages.NewSplit(pc.Split.MaxChars))
	}
	if pc.Protect.Enabled {
		s = append(s, stages.NewProtect(protector))
	}
	if e.cfg.Glossary.Enabled && e.cfg.Glossary.Bootstrap.Enabled && e.bootstrapRenderer != nil {
		s = append(s, &stages.Bootstrap{
			Selector:         e.selector,
			Renderer:         e.bootstrapRenderer,
			Glossary:         e.glossary,
			Limiter:          limiter,
			Retry:            retry,
			Concurrency:      pc.Translate.Concurrency,
			BatchSize:        pc.Translate.BatchSize,
			MaxTermsPerBatch: e.cfg.Glossary.Bootstrap.MaxTermsPerBatch,
			MinSourceLen:     e.cfg.Glossary.Bootstrap.MinSourceLen,
			Logger:           e.logger,
			Reporter:         e.reporter,
		})
	}
	s = append(s, &stages.Translate{
		Selector:       e.selector,
		Renderer:       e.renderer,
		Glossary:       e.glossary,
		TM:             tm.Nop{},
		Limiter:        limiter,
		Retry:          retry,
		Concurrency:    pc.Translate.Concurrency,
		BatchSize:      pc.Translate.BatchSize,
		FallbackShrink: pc.Translate.FallbackShrink,
		Logger:         e.logger,
		Reporter:       e.reporter,
	})
	if pc.Protect.Enabled {
		s = append(s, stages.NewUnprotect(protector))
	}
	return pipeline.New(e.logger, s...)
}

// maybeSaveGlossary 在 bootstrap.save=true 且 glossary 实现 Saver 时回写到磁盘。
// FileGlossary 还会通过 Dirty() 跳过无变化情况，避免无意义的文件写。
func (e *Engine) maybeSaveGlossary(ctx context.Context) {
	if !e.cfg.Glossary.Enabled || !e.cfg.Glossary.Bootstrap.Save {
		return
	}
	type dirtyChecker interface{ Dirty() bool }
	if dc, ok := e.glossary.(dirtyChecker); ok && !dc.Dirty() {
		e.logger.Debug("glossary unchanged, skip save")
		return
	}
	saver, ok := e.glossary.(glossary.Saver)
	if !ok {
		return
	}
	if err := saver.Save(ctx); err != nil {
		e.logger.Warn("glossary save failed", "err", err)
		return
	}
	e.logger.Info("glossary saved", "path", e.cfg.Glossary.Path)
}

func stageNames(ss []pipeline.Stage) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name()
	}
	return out
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
