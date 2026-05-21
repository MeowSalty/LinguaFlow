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
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// Engine 封装一次进程内的翻译能力。它持有 Selector / Renderer 等可复用组件。
type Engine struct {
	cfg      *config.Config
	logger   *slog.Logger
	selector backend.Selector
	renderer *prompt.Renderer
}

// New 按配置构造 Engine。失败时返回 (nil, error)。
func New(cfg *config.Config, logger *slog.Logger) (*Engine, error) {
	if logger == nil {
		logger = slog.Default()
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
	return &Engine{cfg: cfg, logger: logger, selector: sel, renderer: rend}, nil
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
		"path", job.InputPath, "format", doc.Format, "segments", len(doc.Segments))

	pipe := e.buildPipeline()
	e.logger.Info("pipeline start", "stages", stageNames(pipe.Stages()))
	if err := pipe.Run(ctx, doc); err != nil {
		return err
	}

	w := output.New(e.cfg.Output, p, job.OutputPath)
	if err := w.Write(ctx, doc); err != nil {
		return err
	}

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

	var s []pipeline.Stage
	if pc.Split.Enabled {
		s = append(s, stages.NewSplit(pc.Split.MaxChars))
	}
	if pc.Protect.Enabled {
		s = append(s, stages.NewProtect(protector))
	}
	s = append(s, &stages.Translate{
		Selector: e.selector,
		Renderer: e.renderer,
		Glossary: glossary.Nop{},
		TM:       tm.Nop{},
		Limiter:  limiter,
		Retry: backend.RetryPolicy{
			MaxAttempts: pc.Translate.Retry.MaxAttempts,
			Backoff:     pc.Translate.Retry.Backoff,
		},
		Concurrency: pc.Translate.Concurrency,
		BatchSize:   pc.Translate.BatchSize,
		Logger:      e.logger,
	})
	if pc.Protect.Enabled {
		s = append(s, stages.NewUnprotect(protector))
	}
	return pipeline.New(e.logger, s...)
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
