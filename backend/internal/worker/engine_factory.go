package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
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
	var rounds []engine.Round
	for i, rs := range snapshot.Rounds {
		bCfg := backend.Config{
			Name:    rs.Backend.Name,
			Type:    rs.Backend.Type,
			Enabled: true,
			Options: rs.Backend.Options,
		}
		b, err := backend.Build(bCfg)
		if err != nil {
			return nil, fmt.Errorf("round[%d] build backend: %w", i, err)
		}

		b = backend.NewMeteredBackend(b)

		if f.limiterPool != nil && rs.Backend.RateLimitPerMinute > 0 {
			limiter := f.limiterPool.Get(rs.Backend.ID, rs.Backend.RateLimitPerMinute)
			b = backend.NewRateLimitedBackend(b, limiter)
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
		default:
			return nil, fmt.Errorf("round[%d]: unsupported mode %q", i, rs.Mode)
		}
		if err != nil {
			return nil, err
		}
		round.RoundIndex = i
		rounds = append(rounds, round)
	}

	cfg := BuildEngineConfig(snapshot)

	var rubyRetryBackends []backend.Backend
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
	}

	return engine.NewWithOptions(engine.Options{
		Rounds:            rounds,
		RubyRetryBackends: rubyRetryBackends,
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
		rc := repair.Config{
			Enabled:              s.Repair.Enabled,
			JSONStructural:       s.Repair.JSONStructural,
			SchemaAliases:        s.Repair.SchemaAliases,
			PlaceholderNormalize: s.Repair.PlaceholderNormalize,
			PromptUpgrade:        s.Repair.PromptUpgrade,
		}
		cfg.Repair = rc.ToOptions()
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
		FallbackShrink:   t.FallbackShrink,
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

func responseModeFromBackendOptions(opts map[string]any) string {
	if v, ok := opts["response_format"].(string); ok {
		return v
	}
	return ""
}
