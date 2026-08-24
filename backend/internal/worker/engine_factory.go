package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/correct"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

// EngineFactory builds an engine.Engine from a JobExecutionSnapshot using the
// given limiter pool, runtime resources, and reporter. It is shared between
// JobRunner and PreviewRunner so that both produce identical execution
// configurations.
type EngineFactory struct {
	logger      *slog.Logger
	limiterPool *backend.LimiterPool
}

func NewEngineFactory(logger *slog.Logger, limiterPool *backend.LimiterPool) *EngineFactory {
	if logger == nil {
		logger = slog.Default()
	}
	return &EngineFactory{logger: logger, limiterPool: limiterPool}
}

// BuildEngine constructs a fully configured engine.Engine from a snapshot.
// Each round's backend is wrapped with a MeteredBackend (innermost) and a
// RateLimitedBackend (outer). The caller owns the returned Engine and must
// Close it.
func (f *EngineFactory) BuildEngine(
	ctx context.Context,
	snapshot *service.JobExecutionSnapshot,
	resources engine.RuntimeResources,
	reporter progress.Reporter,
) (*engine.Engine, error) {
	return f.BuildEngineWithConfig(ctx, snapshot, BuildEngineConfig(snapshot), resources, reporter)
}

// BuildEngineWithConfig 同 BuildEngine，但使用调用方预派生的 engine.Config。
// 供修订预览等场景使用：执行 snapshot 被裁剪为单个 revise 轮后仍保留顶层
// Strategy（计划级物化），调用方可基于完整快照显式覆盖个别配置（如 Repair）。
func (f *EngineFactory) BuildEngineWithConfig(
	ctx context.Context,
	snapshot *service.JobExecutionSnapshot,
	cfg *engine.Config,
	resources engine.RuntimeResources,
	reporter progress.Reporter,
) (*engine.Engine, error) {
	var rounds []engine.Round
	for i, rs := range snapshot.Rounds {
		// correct 是纯本地轮，无 backend：跳过 backend 构建（Backend 留 nil）。
		var b backend.Backend
		var err error
		if rs.Mode != "correct" {
			bCfg := backend.Config{
				Name:    rs.Backend.Name,
				Type:    rs.Backend.Type,
				Enabled: true,
				Options: rs.Backend.Options,
			}
			b, err = backend.Build(bCfg)
			if err != nil {
				return nil, fmt.Errorf("round[%d] build backend: %w", i, err)
			}

			b = backend.NewMeteredBackend(b)

			if f.limiterPool != nil && rs.Backend.RateLimitPerMinute > 0 {
				limiter := f.limiterPool.Get(rs.Backend.ID, rs.Backend.RateLimitPerMinute)
				b = backend.NewRateLimitedBackend(b, limiter)
			}
		}

		var round engine.Round
		switch rs.Mode {
		case "translate":
			if rs.Translate == nil {
				return nil, fmt.Errorf("round[%d]: mode=translate but translate config is nil", i)
			}
			round, err = buildTranslateRound(rs, snapshot.Strategy, b)
		case "extract":
			if rs.Extract == nil {
				return nil, fmt.Errorf("round[%d]: mode=extract but extract config is nil", i)
			}
			round, err = buildExtractRound(rs, b)
		case "adjudicate":
			if rs.Adjudicate == nil {
				return nil, fmt.Errorf("round[%d]: mode=adjudicate but adjudicate config is nil", i)
			}
			round, err = buildAdjudicateRound(rs, b)
		case "semantic_qa":
			if rs.SemanticQA == nil {
				return nil, fmt.Errorf("round[%d]: mode=semantic_qa but semantic_qa config is nil", i)
			}
			round, err = buildSemanticQARound(rs, b)
		case "revise":
			if rs.Revise == nil {
				return nil, fmt.Errorf("round[%d]: mode=revise but revise config is nil", i)
			}
			round, err = buildReviseRound(rs, snapshot.Strategy, b)
		case "correct":
			if rs.Correct == nil {
				return nil, fmt.Errorf("round[%d]: mode=correct but correct config is nil", i)
			}
			round, err = buildCorrectRound(rs)
		default:
			return nil, fmt.Errorf("round[%d]: unsupported mode %q", i, rs.Mode)
		}
		if err != nil {
			return nil, err
		}
		round.RoundIndex = i
		rounds = append(rounds, round)
	}

	var rubyRetryBackends []backend.Backend
	rubyRetryAttempts := 0
	if snapshot.RubyRetry != nil && snapshot.RubyRetry.Enabled {
		rrCfg := backend.Config{
			Name:    snapshot.RubyRetry.Backend.Name,
			Type:    snapshot.RubyRetry.Backend.Type,
			Enabled: true,
			Options: snapshot.RubyRetry.Backend.Options,
		}
		rrBackend, err := backend.Build(rrCfg)
		if err != nil {
			return nil, fmt.Errorf("ruby retry backend: %w", err)
		}
		rrBackend = backend.NewMeteredBackend(rrBackend)
		if f.limiterPool != nil && snapshot.RubyRetry.Backend.RateLimitPerMinute > 0 {
			limiter := f.limiterPool.Get(snapshot.RubyRetry.Backend.ID, snapshot.RubyRetry.Backend.RateLimitPerMinute)
			rrBackend = backend.NewRateLimitedBackend(rrBackend, limiter)
		}
		rubyRetryBackends = []backend.Backend{rrBackend}
		rubyRetryAttempts = service.NormalizeRubyRetryAttempts(snapshot.RubyRetry.MaxAttempts)
	}

	return engine.NewWithOptions(engine.Options{
		Rounds:            rounds,
		RubyRetryBackends: rubyRetryBackends,
		RubyRetryAttempts: rubyRetryAttempts,
		Config:            cfg,
		Logger:            f.logger,
		Resources:         resources,
		Reporter:          reporter,
	})
}

// CollectMeterMetrics extracts MeterMetrics from every MeteredBackend in the
// engine. After the engine has been closed, this is the only way to read the
// final metering totals.
func CollectMeterMetrics(e *engine.Engine) []backend.MeterMetrics {
	seen := make(map[backend.Backend]struct{})
	var metrics []backend.MeterMetrics
	for _, r := range e.Rounds() {
		m := meterMetricsFromBackend(r.Handler, seen)
		if m != nil {
			metrics = append(metrics, *m)
		}
	}
	return metrics
}

func meterMetricsFromBackend(handler pipeline.RoundHandler, seen map[backend.Backend]struct{}) *backend.MeterMetrics {
	b := extractBackend(handler)
	if b == nil {
		return nil
	}
	if _, ok := seen[b]; ok {
		return nil
	}
	seen[b] = struct{}{}
	mb, ok := unwrapMetered(b)
	if !ok {
		return nil
	}
	m := mb.Metrics()
	return &m
}

func extractBackend(handler pipeline.RoundHandler) backend.Backend {
	switch h := handler.(type) {
	case *pipeline.TranslateHandler:
		return h.Backend
	case *pipeline.ExtractHandler:
		if len(h.Backends) > 0 {
			return h.Backends[0]
		}
	case *pipeline.AdjudicateHandler:
		return h.Backend
	case *pipeline.SemanticQAHandler:
		return h.Backend
	case *pipeline.ReviseHandler:
		return h.Backend
	}
	return nil
}

func unwrapMetered(b backend.Backend) (*backend.MeteredBackend, bool) {
	if mb, ok := b.(*backend.MeteredBackend); ok {
		return mb, true
	}
	if rl, ok := b.(*backend.RateLimitedBackend); ok {
		return unwrapMetered(rl.Backend())
	}
	return nil, false
}

// BuildEngineConfig 是 JobRunner 与 PreviewRunner 共享的引擎配置构建器。
// QA、repair、ruby、glossary 自举等计划级行为直接读快照顶层的 Strategy
// （由 service 层从计划引用的策略物化一次），不再扫描 translate 轮。
func BuildEngineConfig(snapshot *service.JobExecutionSnapshot) *engine.Config {
	s := snapshot.Strategy
	cfg := &engine.Config{
		SourceLang: snapshot.SourceLang,
		TargetLang: snapshot.TargetLang,
		TMEnabled:  snapshot.TMEnabled,
		Glossary: engine.GlossaryConfig{
			Enabled: snapshot.GlossaryEnabled,
		},
	}
	cfg.Repair = service.RepairOptionsFromStrategy(s)
	cfg.Ruby = engine.RubyConfig{
		Enabled:       s.Ruby.Enabled,
		PreserveKinds: s.Ruby.PreserveKinds,
	}
	cfg.Glossary.Bootstrap = config.BootstrapConfig{
		Enabled:                s.Glossary.Bootstrap.Enabled,
		MaxTermsPer1000Chars:   s.Glossary.Bootstrap.MaxTermsPer1000Chars,
		MinSourceLen:           s.Glossary.Bootstrap.MinSourceLen,
		InlineConflictStrategy: s.Glossary.Bootstrap.InlineConflictStrategy,
	}
	cfg.QA = qa.Config{
		Enabled:        s.QA.Enabled,
		AutoReject:     s.QA.AutoReject,
		Checks:         s.QA.Checks,
		LengthMethod:   qa.LengthMethod(s.QA.LengthMethod),
		LengthRatioMin: s.QA.LengthRatioMin,
		LengthRatioMax: s.QA.LengthRatioMax,
	}
	cfg.QA.SourceLang = snapshot.SourceLang
	cfg.QA.TargetLang = snapshot.TargetLang
	return cfg
}

// buildTranslateRound 构建翻译轮：protect/postprocess/context/ruby 全部取自
// 计划级策略快照（snapshot.Strategy，由调用方传入），轮次自身不再携带策略。
func buildTranslateRound(rs service.JobRoundSnapshot, strategy service.StrategySnapshot, b backend.Backend) (engine.Round, error) {
	t := rs.Translate
	roundRenderer, err := prompt.NewRenderer(t.Prompt.Content)
	if err != nil {
		return engine.Round{}, fmt.Errorf("build renderer: %w", err)
	}
	var protectRules []string
	if strategy.Protect.Enabled {
		protectRules = strategy.Protect.Rules
	}
	var roundPostprocess *pipeline.PostprocessConfig
	if strategy.Postprocess.Enabled {
		roundPostprocess = &pipeline.PostprocessConfig{
			TrimSpaces: strategy.Postprocess.TrimSpaces,
		}
	}
	return engine.Round{
		Backend:          b,
		BatchSize:        t.BatchSize,
		MaxWordsPerBatch: t.MaxWordsPerBatch,
		Concurrency:      t.Concurrency,
		FallbackShrink:   service.NormalizeShrink(t.FallbackShrink),
		Retry: backend.RetryPolicy{
			MaxAttempts: t.Retry.MaxAttempts,
			Backoff:     time.Duration(t.Retry.BackoffMs) * time.Millisecond,
			Jitter:      t.Retry.Jitter,
		},
		Renderer:          roundRenderer,
		ResponseMode:      responseModeFromBackendOptions(rs.Backend.Options),
		Mode:              pipeline.RoundModeTranslate,
		ProtectRules:      protectRules,
		RubyEnabled:       strategy.Ruby.Enabled,
		RubyPreserveKinds: strategy.Ruby.PreserveKinds,
		Context: &pipeline.ContextConfig{
			Enabled:  strategy.Context.Enabled,
			Before:   strategy.Context.Before,
			After:    strategy.Context.After,
			MaxChars: strategy.Context.MaxChars,
		},
		Postprocess: roundPostprocess,
	}, nil
}

func buildExtractRound(rs service.JobRoundSnapshot, b backend.Backend) (engine.Round, error) {
	e := rs.Extract
	renderer, err := prompt.NewBootstrapRenderer(e.TemplateContent)
	if err != nil {
		return engine.Round{}, fmt.Errorf("build bootstrap renderer: %w", err)
	}
	return engine.Round{
		Backend:     b,
		BatchSize:   e.BatchSize,
		Concurrency: e.Concurrency,
		// NOTE: extract 不接缩批，不填 FallbackShrink（engine.RoundConfig.FallbackShrink 留零值，
		// buildExtractPipelineRound 不传 Shrink，RunRound 入口兜底为 1.0 供 SSE 文案）。
		Retry: backend.RetryPolicy{
			MaxAttempts: e.Retry.MaxAttempts,
			Backoff:     time.Duration(e.Retry.BackoffMs) * time.Millisecond,
			Jitter:      e.Retry.Jitter,
		},
		Mode:                        pipeline.RoundModeExtract,
		ResponseMode:                responseModeFromBackendOptions(rs.Backend.Options),
		ExtractRenderer:             renderer,
		ExtractMaxTermsPer1000Chars: e.MaxTermsPer1000Chars,
		ExtractMinSourceLen:         e.MinSourceLen,
		ExtractMaxWordsPerBatch:     e.MaxWordsPerBatch,
	}, nil
}

func buildAdjudicateRound(rs service.JobRoundSnapshot, b backend.Backend) (engine.Round, error) {
	a := rs.Adjudicate
	renderer, err := prompt.NewAdjudicationRenderer(templates.EmbeddedAdjudicationTemplate())
	if err != nil {
		return engine.Round{}, fmt.Errorf("build adjudication renderer: %w", err)
	}
	return engine.Round{
		Backend:          b,
		BatchSize:        a.BatchSize,
		MaxWordsPerBatch: a.MaxWordsPerBatch,
		Concurrency:      a.Concurrency,
		// NOTE: adjudicate 不接缩批，不填 FallbackShrink（详见 extract 分支注释）。
		Retry: backend.RetryPolicy{
			MaxAttempts: a.Retry.MaxAttempts,
			Backoff:     time.Duration(a.Retry.BackoffMs) * time.Millisecond,
			Jitter:      a.Retry.Jitter,
		},
		Mode:               pipeline.RoundModeAdjudicate,
		ResponseMode:       responseModeFromBackendOptions(rs.Backend.Options),
		AdjudicateRenderer: renderer,
		AdjudicateCodes:    a.AdjudicateCodes,
	}, nil
}

func buildSemanticQARound(rs service.JobRoundSnapshot, b backend.Backend) (engine.Round, error) {
	s := rs.SemanticQA
	renderer, err := prompt.NewSemanticQARenderer(templates.EmbeddedSemanticQATemplate())
	if err != nil {
		return engine.Round{}, fmt.Errorf("build semantic_qa renderer: %w", err)
	}
	return engine.Round{
		Backend:          b,
		BatchSize:        s.BatchSize,
		MaxWordsPerBatch: s.MaxWordsPerBatch,
		Concurrency:      s.Concurrency,
		// NOTE: semantic_qa 不接缩批，不填 FallbackShrink（详见 extract 分支注释）。
		Retry: backend.RetryPolicy{
			MaxAttempts: s.Retry.MaxAttempts,
			Backoff:     time.Duration(s.Retry.BackoffMs) * time.Millisecond,
			Jitter:      s.Retry.Jitter,
		},
		Mode:               pipeline.RoundModeSemanticQA,
		ResponseMode:       responseModeFromBackendOptions(rs.Backend.Options),
		SemanticQARenderer: renderer,
		SegmentScope:       s.SegmentScope,
		IssueCodes:         s.IssueCodes,
	}, nil
}

// buildReviseRound 构建修订轮：protect/ruby 与 translate 轮同源，直接取计划级
// 策略快照（protect Enabled→Rules，否则 nil）；零值策略 = 无保护规则、不启用注音，
// ReviseHandler 降级原文直发。
func buildReviseRound(rs service.JobRoundSnapshot, strategy service.StrategySnapshot, b backend.Backend) (engine.Round, error) {
	r := rs.Revise
	renderer, err := prompt.NewReviseRenderer(templates.EmbeddedReviseTemplate())
	if err != nil {
		return engine.Round{}, fmt.Errorf("build revise renderer: %w", err)
	}
	var protectRules []string
	if strategy.Protect.Enabled {
		protectRules = strategy.Protect.Rules
	}
	return engine.Round{
		Backend:          b,
		BatchSize:        r.BatchSize,
		MaxWordsPerBatch: r.MaxWordsPerBatch,
		Concurrency:      r.Concurrency,
		// NOTE: revise 不接缩批，不填 FallbackShrink（详见 extract 分支注释）。
		Retry: backend.RetryPolicy{
			MaxAttempts: r.Retry.MaxAttempts,
			Backoff:     time.Duration(r.Retry.BackoffMs) * time.Millisecond,
			Jitter:      r.Retry.Jitter,
		},
		Mode:              pipeline.RoundModeRevise,
		ResponseMode:      responseModeFromBackendOptions(rs.Backend.Options),
		ReviseRenderer:    renderer,
		IssueCodes:        r.IssueCodes,
		ProtectRules:      protectRules,
		RubyEnabled:       strategy.Ruby.Enabled,
		RubyPreserveKinds: strategy.Ruby.PreserveKinds,
	}, nil
}

func buildCorrectRound(rs service.JobRoundSnapshot) (engine.Round, error) {
	c := rs.Correct
	rules := make([]correct.RuleConfig, 0, len(c.Rules))
	for _, r := range c.Rules {
		// 仅构建启用的规则；correct.New 仍按白名单再过滤一次（兜底）。
		if !r.Enabled {
			continue
		}
		rules = append(rules, correct.RuleConfig{Name: r.Name, Enabled: r.Enabled})
	}
	return engine.Round{
		// NOTE: correct 是纯本地轮，无 Backend（留 nil）。Backend==nil 由 engine.NewWithOptions
		// 对 mode==correct 放行，Close()/meterMetrics 对 nil 兜底。
		Concurrency: c.Concurrency,
		// correct 无重试语义（纯函数，仅 ctx cancel 可中断）；Retry 留零值 = 单池、无重试。
		Mode:         pipeline.RoundModeCorrect,
		CorrectRules: rules,
	}, nil
}

func responseModeFromBackendOptions(opts map[string]any) string {
	if v, ok := opts["response_format"].(string); ok {
		return v
	}
	return ""
}
