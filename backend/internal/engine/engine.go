package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

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
	rubyRetryBackends []backend.Backend
	memoryBackends    []backend.Backend
	glossary          glossary.Glossary
	tm                tm.TranslationMemory
	saveGlossary      bool
	glossaryPath      string
	runtime           *pipeline.ExecutionRuntime
	ownRuntime        bool
	closeOnce         sync.Once
	closeMu           sync.Mutex
	runtimeClosed     bool
	closeErr          error
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
	// 校验每轮都有后端（correct 轮纯本地，无需后端）
	for i, r := range opts.Rounds {
		mode := r.Mode
		if mode == "" {
			mode = pipeline.RoundModeTranslate
		}
		if r.Backend == nil && mode != pipeline.RoundModeCorrect && !(mode == pipeline.RoundModeExtract && !opts.Config.Glossary.Enabled) {
			return nil, fmt.Errorf("engine: round %d has no backend", i)
		}
	}
	glos := opts.Resources.Glossary
	if !opts.Config.Glossary.Enabled {
		glos = glossary.Nop{}
	}
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
	rubyRetryBackends := opts.RubyRetryBackends
	var ownedMemoryRuntime *pipeline.ExecutionRuntime
	var memoryBackends []backend.Backend
	if opts.Runtime == nil {
		var err error
		memoryBackends = make([]backend.Backend, 0, len(roundConfigs)+len(rubyRetryBackends))
		for _, round := range roundConfigs {
			memoryBackends = append(memoryBackends, round.Backend)
		}
		memoryBackends = append(memoryBackends, rubyRetryBackends...)
		ownedMemoryRuntime, rubyRetryBackends, err = bindMemoryExecution(roundConfigs, rubyRetryBackends)
		if err != nil {
			return nil, err
		}
		opts.Runtime, opts.OwnRuntime = ownedMemoryRuntime, true
		if opts.RubyProtocolVersion == 0 {
			opts.RubyProtocolVersion = ruby.ProtocolV2
		}
	}

	rounds, err := buildPipelineRounds(
		roundConfigs,
		glos,
		translationMemory,
		rubyRetryBackends,
		opts.Config.Repair,
		opts.Logger,
		opts.Reporter,
		opts.RubyRetryAttempts,
	)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("engine: build rounds: %w", err), ownedMemoryRuntime.Close())
	}
	for i := range rounds {
		round := &rounds[i]
		round.Runtime = opts.Runtime
		switch h := round.Handler.(type) {
		case *pipeline.TranslateHandler:
			h.RubyProtocolVersion = opts.RubyProtocolVersion
			h.RubyBatch = opts.RubyBatch
			h.RubyTemplates = opts.RubyTemplates
			h.RetryReminderTemplate = opts.RetryReminderTemplate
		case *pipeline.ReviseHandler:
			h.RubyProtocolVersion = opts.RubyProtocolVersion
			h.RubyBatch = opts.RubyBatch
			h.RubyTemplates = opts.RubyTemplates
		}
	}

	e := &Engine{
		cfg:               opts.Config,
		logger:            opts.Logger,
		reporter:          opts.Reporter,
		rounds:            rounds,
		rubyRetryBackends: rubyRetryBackends,
		memoryBackends:    memoryBackends,
		glossary:          glos,
		tm:                translationMemory,
		saveGlossary:      opts.Config.Glossary.Save,
		glossaryPath:      opts.Config.Glossary.Path,
		runtime:           opts.Runtime,
		ownRuntime:        opts.OwnRuntime,
	}
	return e, nil
}

func bindMemoryExecution(rounds []RoundConfig, rubyBackends []backend.Backend) (*pipeline.ExecutionRuntime, []backend.Backend, error) {
	main := make(map[int]int, len(rounds))
	alignment := 0
	for i := range rounds {
		rounds[i].RoundIndex = i
		main[i] = max(1, rounds[i].Concurrency)
		if len(rubyBackends) != 0 {
			alignment = max(alignment, main[i])
		}
	}
	runtime, err := pipeline.NewMemoryExecutionRuntime(main, alignment, nil)
	if err != nil {
		return nil, nil, err
	}
	for i := range rounds {
		rounds[i].Backend = pipeline.BindRequestBackend(rounds[i].Backend, i+1, i, runtime)
	}
	bound := make([]backend.Backend, len(rubyBackends))
	for i, b := range rubyBackends {
		bound[i] = pipeline.BindRequestBackend(b, len(rounds)+i+1, 0, runtime)
	}
	return runtime, bound, nil
}

// Close releases backend connections once and retries retirement of an owned runtime.
// The caller must join execution before closing the Engine.
func (e *Engine) Close() error {
	e.closeMu.Lock()
	defer e.closeMu.Unlock()
	var runtimeErr error
	if e.ownRuntime && e.runtime != nil && !e.runtimeClosed {
		runtimeErr = e.runtime.Close()
		e.runtimeClosed = runtimeErr == nil
	}
	e.closeOnce.Do(func() {
		e.closeErr = e.closeBackends()
	})
	return errors.Join(runtimeErr, e.closeErr)
}

func (e *Engine) closeBackends() error {
	seen := make(map[backend.Backend]struct{})
	var firstErr error
	closeBackend := func(b backend.Backend) {
		if b == nil {
			return
		}
		if _, ok := seen[b]; ok {
			return
		}
		seen[b] = struct{}{}
		if err := b.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if e.memoryBackends != nil {
		for _, b := range e.memoryBackends {
			closeBackend(b)
		}
		return firstErr
	}
	for _, r := range e.rounds {
		var b backend.Backend
		if th, ok := r.Handler.(*pipeline.TranslateHandler); ok {
			b = th.Backend
		} else if eh, ok := r.Handler.(*pipeline.ExtractHandler); ok {
			b = eh.Backend
		} else if ah, ok := r.Handler.(*pipeline.AdjudicateHandler); ok {
			b = ah.Backend
		} else if sh, ok := r.Handler.(*pipeline.SemanticQAHandler); ok {
			b = sh.Backend
		} else if rh, ok := r.Handler.(*pipeline.ReviseHandler); ok {
			b = rh.Backend
		} else if _, ok := r.Handler.(*pipeline.CorrectHandler); ok {
			// correct 是纯本地轮，无 backend。
		}
		closeBackend(b)
	}
	for _, b := range e.rubyRetryBackends {
		closeBackend(b)
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
