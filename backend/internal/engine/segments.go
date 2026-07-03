package engine

import (
	"log/slog"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
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

// BuildTranslateStage 构建翻译管道（Protect + Translate 阶段）。
// Protect 作为 Pipeline stage 执行；Unprotect/RubyRestore/TM 作为 postSegment hooks。
// rubyOutputFormat 应通过 resolveRubyOutputFormat() 预先解析。
func (e *Engine) BuildTranslateStage(protector protect.Protector, restorer *protect.RubyRestorer, rubyOutputFormat string) *pipeline.Pipeline {
	pc := e.cfg.Pipeline
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
		Retry:                  retry,
		Logger:                 e.logger,
		Reporter:               e.reporter,
		InlineBootstrap:        inlineBootstrap,
		MaxTermsPer1000Chars:   e.cfg.Glossary.Bootstrap.MaxTermsPer1000Chars,
		MinBootstrapSourceLen:  e.cfg.Glossary.Bootstrap.MinSourceLen,
		InlineConflictStrategy: e.cfg.Glossary.Bootstrap.InlineConflictStrategy,
		Repair:                 repairOpts,
		RubyOutputFormat:       rubyOutputFormat,
		PreserveKinds:          pc.Protect.Ruby.PreserveKinds,
		RubyRetryBackends:      e.rubyRetryBackends,
		Context:                pc.Context,
	}

	// 构建 postSegment hooks
	var hooks []pipeline.PostSegmentHook
	if pc.Protect.Enabled {
		hooks = append(hooks, pipeline.UnprotectHook(protector, e.logger))
	}
	if pc.Protect.Ruby.Enabled && restorer != nil {
		hooks = append(hooks, pipeline.RubyRestoreHook(
			restorer,
			pc.Protect.Ruby.PreserveKinds,
			e.rubyRetryBackends,
			retry,
			e.reporter,
			e.logger,
		))
	}
	if e.tm != nil {
		hooks = append(hooks, pipeline.TMAddHook(e.tm, e.logger))
	}

	// 构建 stages：Protect → Translate
	var stages []pipeline.Stage
	if pc.Protect.Enabled {
		stages = append(stages, pipeline.NewProtect(protector))
	}
	stages = append(stages, translateStage)

	return pipeline.NewWithHooks(e.logger, hooks, stages...)
}

// PrepareDocument 设置语言、Vars、段落选择等公共配置。
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

// hasTextModeRound 检查是否有任何 round 使用 text 响应模式。
func hasTextModeRound(rounds []pipeline.Round) bool {
	for _, r := range rounds {
		if r.ResponseMode == "text" {
			return true
		}
	}
	return false
}

// resolveRubyOutputFormat 根据配置和 text 模式自动选择 ruby 输出格式。
// text 模式下强制使用 inline_markers（因 JSON ruby_output 无法在纯文本协议中表达）。
func (e *Engine) resolveRubyOutputFormat() string {
	format := e.cfg.Pipeline.Protect.Ruby.OutputFormat
	if hasTextModeRound(e.rounds) && format == config.RubyOutputDefault {
		e.logger.Info("text mode detected, overriding ruby output_format to inline_markers",
			slog.String("from", config.RubyOutputDefault),
			slog.String("to", config.RubyOutputInlineMarkers))
		format = config.RubyOutputInlineMarkers
	}
	return format
}
