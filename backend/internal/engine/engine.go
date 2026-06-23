package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline/stages"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// Engine 封装一次进程内的翻译能力。它持有 rounds / Renderer 等可复用组件。
type Engine struct {
	cfg               *config.Config
	logger            *slog.Logger
	reporter          progress.Reporter
	rounds            []stages.Round    // 替代 selector
	bootstrapBackends []backend.Backend // 自举后端
	renderer          *prompt.Renderer
	bootstrapRenderer *prompt.BootstrapRenderer
	glossary          glossary.Glossary
	tm                tm.TranslationMemory
}

type SegmentResult struct {
	Index      int
	SourceText string
	TargetText string
	Failed     bool // true 表示该段在所有轮次中均未成功翻译
}

type TranslateResult struct {
	SegmentCount    int
	Segments        []SegmentResult
	UnresolvedCount int // 所有轮次结束后仍未解决（被原文填充）的段数量
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
	e := &Engine{
		cfg:               opts.Config,
		logger:            opts.Logger,
		reporter:          opts.Reporter,
		rounds:            rounds,
		bootstrapBackends: bootstrapBackends,
		renderer:          rend,
		glossary:          glos,
		tm:                translationMemory,
	}
	if opts.Config.Glossary.Enabled && opts.Config.Glossary.Bootstrap.Mode == config.BootstrapModePre {
		if opts.Config.Glossary.Bootstrap.TemplateContent == "" {
			return nil, fmt.Errorf("engine: bootstrap template content is required when mode is %q", config.BootstrapModePre)
		}
		br, err := prompt.NewBootstrapRenderer(opts.Config.Glossary.Bootstrap.TemplateContent)
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

// Translate 执行一次翻译任务。
func (e *Engine) Translate(ctx context.Context, job TranslateJob) error {
	_, err := e.TranslateWithResult(ctx, job)
	return err
}

func (e *Engine) TranslateWithResult(ctx context.Context, job TranslateJob) (TranslateResult, error) {
	start := time.Now()
	var result TranslateResult

	// 1. 通过 FormatHint 检测格式
	hint := job.Source.FormatHint()
	p, err := parser.DetectByExt(hint)
	if err != nil {
		return result, err
	}

	// 2. 通过 DocumentSource.Open 获取 reader
	reader, err := job.Source.Open(ctx)
	if err != nil {
		return result, fmt.Errorf("engine: open source: %w", err)
	}
	defer func() { _ = reader.Close() }()

	doc, parseErr := p.Parse(ctx, reader)
	if parseErr != nil {
		return result, fmt.Errorf("engine: parse: %w", parseErr)
	}
	result.SegmentCount = len(doc.Segments)
	selectedSegments := selectedSegmentIndexSet(job.SegmentIndexes)
	if len(selectedSegments) > 0 {
		applySegmentSelection(doc, selectedSegments)
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
		"format", doc.Format,
		"segments", len(doc.Segments),
		"source_lang", doc.SourceLang,
		"target_lang", doc.TargetLang)

	pipe, limiter := e.buildPipeline(pipelineOptions{})
	defer limiter.Close()
	e.logger.Info("pipeline start", "stages", stageNames(pipe.Stages()))
	if err := pipe.Run(ctx, doc); err != nil {
		return result, err
	}
	// 从文档变量中读取未解决段数量（由 translate stage 注入）。
	if v, ok := doc.Vars["_translate_unresolved_count"]; ok {
		if n, ok := v.(int); ok {
			result.UnresolvedCount = n
		}
	}
	// 从文档变量中读取失败段索引集合（由 translate stage 注入）。
	failedSet := make(map[int]struct{})
	if v, ok := doc.Vars["_translate_failed_indices"]; ok {
		if s, ok := v.(string); ok && s != "" {
			for _, idxStr := range strings.Split(s, ",") {
				if idx, err := strconv.Atoi(strings.TrimSpace(idxStr)); err == nil {
					failedSet[idx] = struct{}{}
				}
			}
		}
	}
	if len(selectedSegments) > 0 {
		restoreUnselectedTargets(doc, selectedSegments, job.ExistingTargets)
	}
	result.Segments = make([]SegmentResult, 0, len(doc.Segments))
	for i, seg := range doc.Segments {
		source := seg.OriginalSource
		if source == "" {
			source = seg.Source
		}
		_, isFailed := failedSet[i]
		result.Segments = append(result.Segments, SegmentResult{
			Index:      i,
			SourceText: source,
			TargetText: seg.Target,
			Failed:     isFailed,
		})
	}

	// 3. 重新打开原始文件用于 Render（Parse 已消耗了 reader）
	original, err := job.Source.Open(ctx)
	if err != nil {
		return result, fmt.Errorf("engine: reopen source: %w", err)
	}
	defer func() { _ = original.Close() }()

	// 4. 通过 DocumentSink.Create 获取 writer
	writer, err := job.Sink.Create(ctx)
	if err != nil {
		return result, fmt.Errorf("engine: create sink: %w", err)
	}
	defer func() { _ = writer.Close() }()

	if err := p.Render(ctx, doc, original, writer); err != nil {
		return result, fmt.Errorf("engine: render: %w", err)
	}

	// 自举完成后按配置回写术语表。失败仅 warn——译文已写出。
	e.maybeSaveGlossary(ctx)

	e.logger.Info("output written",
		"segments", len(doc.Segments),
		"duration", time.Since(start).Round(time.Millisecond))
	return result, nil
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
