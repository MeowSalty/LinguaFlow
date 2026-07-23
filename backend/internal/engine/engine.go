package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// Engine 封装一次进程内的翻译能力。它持有 rounds / Renderer 等可复用组件。
type Engine struct {
	cfg               *Config
	logger            *slog.Logger
	reporter          progress.Reporter
	rounds            []pipeline.Round
	bootstrapBackends []backend.Backend
	rubyRetryBackends []backend.Backend
	glossary          glossary.Glossary
	tm                tm.TranslationMemory
	rubyRestorer      *ruby.Restorer
	saveGlossary      bool
	glossaryPath      string
}

// NewWithOptions 按 Options 构造 Engine。rounds 必须非空，每轮 backends 必须非空。
func NewWithOptions(opts Options) (*Engine, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Reporter == nil {
		opts.Reporter = progress.Nop{}
	}
	if len(opts.Rounds) == 0 {
		return nil, fmt.Errorf("engine: no rounds provided")
	}
	// 校验每轮都有后端
	for i, r := range opts.Rounds {
		if r.Backend == nil {
			return nil, fmt.Errorf("engine: round %d has no backend", i)
		}
	}
	glos := opts.Resources.Glossary
	if glos == nil {
		var err error
		glos, err = glossary.New(opts.Config.Glossary.Enabled, opts.Config.Glossary.Path)
		if err != nil {
			return nil, fmt.Errorf("engine: build glossary: %w", err)
		}
	}
	translationMemory := opts.Resources.TM
	if translationMemory == nil {
		translationMemory = tm.Nop{}
	}

	roundConfigs := buildRoundConfigs(opts.Rounds, opts.Config)
	bootstrapBackends := opts.BootstrapBackends
	if len(bootstrapBackends) == 0 {
		bootstrapBackends = []backend.Backend{opts.Rounds[0].Backend}
	}
	rubyRetryBackends := opts.RubyRetryBackends

	inlineBootstrap := opts.Config.Glossary.Enabled && opts.Config.Glossary.Bootstrap.Enabled
	maxTermsPer1000 := opts.Config.Glossary.Bootstrap.MaxTermsPer1000Chars
	minSourceLen := opts.Config.Glossary.Bootstrap.MinSourceLen
	inlineConflictStr := opts.Config.Glossary.Bootstrap.InlineConflictStrategy

	var rubyRestorer *ruby.Restorer
	if opts.Config.Ruby.Enabled {
		rubyRestorer = ruby.NewRestorer()
	}

	rounds, err := buildPipelineRounds(
		roundConfigs,
		glos,
		translationMemory,
		rubyRestorer,
		rubyRetryBackends,
		opts.Config.Repair,
		inlineBootstrap,
		maxTermsPer1000,
		minSourceLen,
		inlineConflictStr,
		opts.Logger,
		opts.Reporter,
	)
	if err != nil {
		return nil, fmt.Errorf("engine: build rounds: %w", err)
	}

	e := &Engine{
		cfg:               opts.Config,
		logger:            opts.Logger,
		reporter:          opts.Reporter,
		rounds:            rounds,
		bootstrapBackends: bootstrapBackends,
		rubyRetryBackends: rubyRetryBackends,
		glossary:          glos,
		tm:                translationMemory,
		rubyRestorer:      rubyRestorer,
		saveGlossary:      opts.Config.Glossary.Save,
		glossaryPath:      opts.Config.Glossary.Path,
	}
	return e, nil
}

// Close 释放后端连接。
func (e *Engine) Close() error {
	seen := make(map[backend.Backend]struct{})
	var firstErr error
	for _, r := range e.rounds {
		var b backend.Backend
		if th, ok := r.Handler.(*pipeline.TranslateHandler); ok {
			b = th.Backend
		} else if eh, ok := r.Handler.(*pipeline.ExtractHandler); ok {
			if len(eh.Backends) > 0 {
				b = eh.Backends[0]
			}
		} else if ah, ok := r.Handler.(*pipeline.AdjudicateHandler); ok {
			b = ah.Backend
		}
		if b == nil {
			continue
		}
		if _, ok := seen[b]; ok {
			continue
		}
		seen[b] = struct{}{}
		if err := b.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, b := range e.bootstrapBackends {
		if _, ok := seen[b]; ok {
			continue
		}
		seen[b] = struct{}{}
		if err := b.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// maybeSaveGlossary 在 bootstrap.save=true 且 glossary 实现 Saver 时回写到磁盘。
func (e *Engine) maybeSaveGlossary(ctx context.Context) {
	if !e.saveGlossary {
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
	e.logger.Info("glossary saved", "path", e.glossaryPath)
}

// Rounds 返回引擎的轮次配置。
func (e *Engine) Rounds() []pipeline.Round { return e.rounds }

// SaveGlossary 保存术语表到磁盘。
func (e *Engine) SaveGlossary(ctx context.Context) { e.maybeSaveGlossary(ctx) }

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func selectedSegmentIndexSet(indexes []int) map[int]struct{} {
	if len(indexes) == 0 {
		return nil
	}
	selected := make(map[int]struct{}, len(indexes))
	for _, idx := range indexes {
		if idx >= 0 {
			selected[idx] = struct{}{}
		}
	}
	return selected
}

func applySegmentSelection(doc *pipeline.Document, selected map[int]struct{}) {
	if doc == nil || len(selected) == 0 {
		return
	}
	for i := range doc.Segments {
		if _, ok := selected[i]; !ok {
			doc.Segments[i].Translate = false
		}
	}
}
