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
// 供修订预览等场景使用：执行 snapshot 被裁剪为单个 revise 轮后，BuildEngineConfig
// 无法再从 translate 轮派生 Repair 等策略，调用方需从完整 snapshot 派生后传入。
func (f *EngineFactory) BuildEngineWithConfig(
	ctx context.Context,
	snapshot *service.JobExecutionSnapshot,
	cfg *engine.Config,
	resources engine.RuntimeResources,
	reporter progress.Reporter,
) (*engine.Engine, error) {
	// revise 轮的 protect/ruby 配置借用计划内第一条 translate 轮的 Strategy
	// （与 BuildEngineConfig 借用 Repair/Ruby/QA 同语义）；无 translate 轮返回零值，
	// ReviseHandler 降级为原文直发。
	borrowProtectRules, borrowRubyEnabled, borrowRubyPreserveKinds := borrowTranslateProtectRuby(snapshot)

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
			round, err = buildTranslateRound(rs, b)
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
			round, err = buildReviseRound(rs, b, borrowProtectRules, borrowRubyEnabled, borrowRubyPreserveKinds)
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

// BuildEngineConfig is the shared engine.Config builder used by both JobRunner
// and PreviewRunner. It reads QA, repair, ruby, and glossary config from the
// first translate round's strategy snapshot.
func BuildEngineConfig(snapshot *service.JobExecutionSnapshot) *engine.Config {
	cfg := &engine.Config{
		SourceLang: snapshot.SourceLang,
		TargetLang: snapshot.TargetLang,
		TMEnabled:  snapshot.TMEnabled,
		Glossary: engine.GlossaryConfig{
			Enabled: snapshot.GlossaryEnabled,
		},
	}
	for _, rs := range snapshot.Rounds {
		if rs.Mode != "translate" || rs.Translate == nil {
			continue
		}
		s := rs.Translate.Strategy
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
		break
	}
	return cfg
}

func buildTranslateRound(rs service.JobRoundSnapshot, b backend.Backend) (engine.Round, error) {
	t := rs.Translate
	roundRenderer, err := prompt.NewRenderer(t.Prompt.Content)
	if err != nil {
		return engine.Round{}, fmt.Errorf("build renderer: %w", err)
	}
	var protectRules []string
	if t.Strategy.Protect.Enabled {
		protectRules = t.Strategy.Protect.Rules
	}
	var roundPostprocess *pipeline.PostprocessConfig
	if t.Strategy.Postprocess.Enabled {
		roundPostprocess = &pipeline.PostprocessConfig{
			TrimSpaces: t.Strategy.Postprocess.TrimSpaces,
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
		RubyEnabled:       t.Strategy.Ruby.Enabled,
		RubyPreserveKinds: t.Strategy.Ruby.PreserveKinds,
		Context: &pipeline.ContextConfig{
			Enabled:  t.Strategy.Context.Enabled,
			Before:   t.Strategy.Context.Before,
			After:    t.Strategy.Context.After,
			MaxChars: t.Strategy.Context.MaxChars,
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

// borrowTranslateProtectRuby 委托 service.BorrowTranslateProtectRuby：真实作业
// 引擎与修订预览的合成快照共用同一借用语义，实现收敛在 service 层避免重复。
func borrowTranslateProtectRuby(snapshot *service.JobExecutionSnapshot) (protectRules []string, rubyEnabled bool, rubyPreserveKinds []string) {
	return service.BorrowTranslateProtectRuby(snapshot)
}

func buildReviseRound(rs service.JobRoundSnapshot, b backend.Backend, borrowProtectRules []string, borrowRubyEnabled bool, borrowRubyPreserveKinds []string) (engine.Round, error) {
	r := rs.Revise
	renderer, err := prompt.NewReviseRenderer(templates.EmbeddedReviseTemplate())
	if err != nil {
		return engine.Round{}, fmt.Errorf("build revise renderer: %w", err)
	}
	// 快照轮显式携带借用结果（修订预览的合成单轮快照，裁剪前已物化）时优先之；
	// 否则用工厂从完整快照借用的值（真实作业路径）。两者均零值 = 无 translate
	// 轮，降级原文直发。
	protectRules, rubyEnabled, rubyPreserveKinds := borrowProtectRules, borrowRubyEnabled, borrowRubyPreserveKinds
	if r.ProtectRules != nil || r.RubyEnabled || r.RubyPreserveKinds != nil {
		protectRules, rubyEnabled, rubyPreserveKinds = r.ProtectRules, r.RubyEnabled, r.RubyPreserveKinds
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
		Mode:           pipeline.RoundModeRevise,
		ResponseMode:   responseModeFromBackendOptions(rs.Backend.Options),
		ReviseRenderer: renderer,
		IssueCodes:     r.IssueCodes,
		// protect/ruby 借用计划内第一条 translate 轮的 Strategy（见
		// borrowTranslateProtectRuby）；零值 = 无 translate 轮，降级原文直发。
		ProtectRules:      protectRules,
		RubyEnabled:       rubyEnabled,
		RubyPreserveKinds: rubyPreserveKinds,
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
