package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// Engine 封装一次进程内的翻译能力。它持有 rounds / Renderer 等可复用组件。
type Engine struct {
	cfg                 *config.Config
	logger              *slog.Logger
	reporter            progress.Reporter
	rounds              []pipeline.Round                  // 替代 selector
	bootstrapBackends   []backend.Backend                 // 自举后端
	rubyRetryBackends   []backend.Backend                 // 注音对齐重试后端
	standaloneBootstrap *config.StandaloneBootstrapConfig // 独立自举配置
	renderer            *prompt.Renderer
	bootstrapRenderer   *prompt.BootstrapRenderer
	glossary            glossary.Glossary
	tm                  tm.TranslationMemory
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
		if len(r.Backends) == 0 {
			return nil, fmt.Errorf("engine: round %d has no backends", i)
		}
	}
	rend, err := prompt.NewRenderer(opts.Config.Prompt)
	if err != nil {
		return nil, err
	}
	glos := opts.Resources.Glossary
	if glos == nil {
		glos, err = glossary.New(opts.Config.Glossary)
		if err != nil {
			return nil, fmt.Errorf("engine: build glossary: %w", err)
		}
	}
	translationMemory := opts.Resources.TM
	if translationMemory == nil {
		translationMemory = tm.Nop{}
	}
	rounds := buildStagesRounds(opts.Rounds, opts.Config)
	bootstrapBackends := opts.BootstrapBackends
	if len(bootstrapBackends) == 0 {
		bootstrapBackends = opts.Rounds[0].Backends
	}
	rubyRetryBackends := opts.RubyRetryBackends
	if len(rubyRetryBackends) == 0 {
		rubyRetryBackends = opts.Rounds[0].Backends
	}
	e := &Engine{
		cfg:                 opts.Config,
		logger:              opts.Logger,
		reporter:            opts.Reporter,
		rounds:              rounds,
		bootstrapBackends:   bootstrapBackends,
		rubyRetryBackends:   rubyRetryBackends,
		standaloneBootstrap: &opts.Config.Glossary.Standalone,
		renderer:            rend,
		glossary:            glos,
		tm:                  translationMemory,
	}
	if opts.Config.Glossary.Standalone.Enabled {
		if opts.Config.Glossary.Standalone.TemplateContent == "" {
			return nil, fmt.Errorf("engine: standalone bootstrap template content is required when enabled")
		}
		br, err := prompt.NewBootstrapRenderer(opts.Config.Glossary.Standalone.TemplateContent)
		if err != nil {
			return nil, fmt.Errorf("engine: build bootstrap renderer: %w", err)
		}
		e.bootstrapRenderer = br
	}
	return e, nil
}

// Close 释放后端连接。
func (e *Engine) Close() error {
	seen := make(map[backend.Backend]struct{})
	var firstErr error
	for _, r := range e.rounds {
		for _, b := range r.Backends {
			if _, ok := seen[b]; ok {
				continue
			}
			seen[b] = struct{}{}
			if err := b.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
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
// FileGlossary 还会通过 Dirty() 跳过无变化情况，避免无意义的文件写。
func (e *Engine) maybeSaveGlossary(ctx context.Context) {
	if !e.cfg.Glossary.Enabled || !e.cfg.Glossary.Save {
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
			doc.Segments[i].Skip = true
		}
	}
}

func restoreUnselectedTargets(doc *pipeline.Document, selected map[int]struct{}, existing map[int]string) {
	if doc == nil || len(selected) == 0 {
		return
	}
	for i := range doc.Segments {
		if _, ok := selected[i]; ok {
			continue
		}
		if target, ok := existing[i]; ok && target != "" {
			doc.Segments[i].Target = target
		}
	}
}

// toRepairOptions 把 config 层的 RepairConfig 翻成 repair 包消费的 Options。
// config.RepairConfig.Normalize() 已在 Validate 阶段处理 Enabled=false 的短路与
// PartialThreshold 边界，这里只做字段映射。
func toRepairOptions(c config.RepairConfig) repair.Options {
	return repair.Options{
		JSONStructural:       c.JSONStructural,
		SchemaAliases:        c.SchemaAliases,
		Partial:              c.Partial,
		PartialThreshold:     c.PartialThreshold,
		PlaceholderNormalize: c.PlaceholderNormalize,
		PromptUpgrade:        c.PromptUpgrade,
	}
}
