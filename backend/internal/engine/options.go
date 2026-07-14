package engine

import (
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// Options 是 Engine 的构造参数。
type Options struct {
	Rounds            []Round
	BootstrapBackends []backend.Backend
	RubyRetryBackends []backend.Backend
	Config            *Config
	Logger            *slog.Logger
	Reporter          progress.Reporter
	Resources         RuntimeResources
}

// Round 描述一轮翻译的执行配置（Engine 级别）。
type Round struct {
	Backend          backend.Backend
	BatchSize        int
	MaxWordsPerBatch int
	Concurrency      int
	FallbackShrink   float64
	Retry            backend.RetryPolicy
	Renderer         *prompt.Renderer
	Repair           *repair.Config
	ResponseMode     string

	Mode              string
	ProtectRules      []string
	RubyEnabled       bool
	RubyPreserveKinds []string
	Context           *pipeline.ContextConfig
	Postprocess       *pipeline.PostprocessConfig

	// 抽取轮次专用字段
	ExtractRenderer             *prompt.BootstrapRenderer
	ExtractMaxTermsPer1000Chars float64
	ExtractMinSourceLen         int
	ExtractMaxWordsPerBatch     int
	ExtractRepair               repair.Options
}

// RuntimeResources 封装可选的运行时资源。
type RuntimeResources struct {
	Glossary glossary.Glossary
	TM       tm.TranslationMemory
}

func resolveDefault(val, global, fallback int) int {
	if val > 0 {
		return val
	}
	if global > 0 {
		return global
	}
	return fallback
}

func resolveShrink(val, global float64) float64 {
	if val > 0 {
		return val
	}
	return global
}

// buildRoundConfigs 将 engine.Round 转换为 RoundConfig（中间配置）。
func buildRoundConfigs(in []Round, cfg *Config) []RoundConfig {
	if len(in) == 0 {
		return nil
	}
	globalRetry := cfg.TranslateDefaults.Retry
	out := make([]RoundConfig, 0, len(in))
	for _, r := range in {
		retry := r.Retry
		if retry.MaxAttempts == 0 {
			retry = globalRetry
		}

		mode := r.Mode
		if mode == "" {
			mode = pipeline.RoundModeTranslate
		}

		var roundCtx *pipeline.ContextConfig
		if r.Context != nil {
			roundCtx = r.Context
		}

		rc := RoundConfig{
			Backend:          r.Backend,
			BatchSize:        resolveDefault(r.BatchSize, cfg.TranslateDefaults.BatchSize, 1),
			MaxWordsPerBatch: r.MaxWordsPerBatch,
			Concurrency:      resolveDefault(r.Concurrency, cfg.TranslateDefaults.Concurrency, 1),
			FallbackShrink:   resolveShrink(r.FallbackShrink, cfg.TranslateDefaults.FallbackShrink),
			Retry:            retry,
			Context:          roundCtx,
		}

		switch mode {
		case pipeline.RoundModeTranslate:
			var roundRepair *repair.Config
			if r.Repair != nil {
				rr := *r.Repair
				rr.Normalize()
				roundRepair = &rr
			}

			var roundPostprocess *pipeline.PostprocessConfig
			if r.Postprocess != nil {
				roundPostprocess = &pipeline.PostprocessConfig{
					TrimSpaces: r.Postprocess.TrimSpaces,
				}
			}

			rc.Translate = &TranslateRoundConfig{
				Renderer:          r.Renderer,
				Repair:            roundRepair,
				ResponseMode:      r.ResponseMode,
				ProtectRules:      r.ProtectRules,
				RubyEnabled:       r.RubyEnabled,
				RubyPreserveKinds: r.RubyPreserveKinds,
				Postprocess:       roundPostprocess,
			}

		case pipeline.RoundModeExtract:
			rc.Extract = &ExtractRoundConfig{
				Renderer:             r.ExtractRenderer,
				MaxTermsPer1000Chars: r.ExtractMaxTermsPer1000Chars,
				MinSourceLen:         r.ExtractMinSourceLen,
				MaxWordsPerBatch:     r.ExtractMaxWordsPerBatch,
				Repair:               r.ExtractRepair,
			}

		default:
			// 未知模式默认为翻译
			rc.Translate = &TranslateRoundConfig{
				Renderer: r.Renderer,
			}
		}

		out = append(out, rc)
	}
	return out
}

// buildPipelineRounds 将 RoundConfig 转换为 pipeline.Round（含 Handler）。
// 注入引擎级资源：glossary、TM、ruby restorer 等。
func buildPipelineRounds(
	configs []RoundConfig,
	glossaryRes glossary.Glossary,
	tmRes tm.TranslationMemory,
	rubyRestorer *ruby.Restorer,
	rubyRetryBackends []backend.Backend,
	defaultRepair repair.Options,
	inlineBootstrap bool,
	maxTermsPer1000 float64,
	minSourceLen int,
	inlineConflictStr string,
	logger *slog.Logger,
	reporter progress.Reporter,
) ([]pipeline.Round, error) {
	out := make([]pipeline.Round, 0, len(configs))
	for _, rc := range configs {
		round, err := buildSinglePipelineRound(
			rc, glossaryRes, tmRes, rubyRestorer, rubyRetryBackends,
			defaultRepair, inlineBootstrap, maxTermsPer1000, minSourceLen,
			inlineConflictStr, logger, reporter,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, round)
	}
	return out, nil
}

func buildSinglePipelineRound(
	rc RoundConfig,
	glossaryRes glossary.Glossary,
	tmRes tm.TranslationMemory,
	rubyRestorer *ruby.Restorer,
	rubyRetryBackends []backend.Backend,
	defaultRepair repair.Options,
	inlineBootstrap bool,
	maxTermsPer1000 float64,
	minSourceLen int,
	inlineConflictStr string,
	logger *slog.Logger,
	reporter progress.Reporter,
) (pipeline.Round, error) {
	if rc.Extract != nil {
		return buildExtractPipelineRound(rc, glossaryRes, logger, reporter)
	}
	return buildTranslatePipelineRound(
		rc, glossaryRes, tmRes, rubyRestorer, rubyRetryBackends,
		defaultRepair, inlineBootstrap, maxTermsPer1000, minSourceLen,
		inlineConflictStr, logger, reporter,
	)
}

func buildTranslatePipelineRound(
	rc RoundConfig,
	glossaryRes glossary.Glossary,
	tmRes tm.TranslationMemory,
	rubyRestorer *ruby.Restorer,
	rubyRetryBackends []backend.Backend,
	defaultRepair repair.Options,
	inlineBootstrap bool,
	maxTermsPer1000 float64,
	minSourceLen int,
	inlineConflictStr string,
	logger *slog.Logger,
	reporter progress.Reporter,
) (pipeline.Round, error) {
	t := rc.Translate
	if t == nil {
		t = &TranslateRoundConfig{}
	}

	repairOpts := defaultRepair
	if t.Repair != nil {
		repairOpts = t.Repair.ToOptions()
	}

	// 构建 per-round Protector
	var prot protect.Protector
	if t.RubyEnabled || len(t.ProtectRules) > 0 {
		ps := []protect.Protector{}
		if t.RubyEnabled {
			ps = append(ps, &ruby.Extractor{})
		}
		if len(t.ProtectRules) > 0 {
			ps = append(ps, protect.FromRules(t.ProtectRules))
		}
		prot = protect.Compose(ps...)
	}

	rubyMode := ""
	if t.RubyEnabled {
		rubyMode = prompt.RubyModeJSON
		if t.ResponseMode == "text" {
			rubyMode = prompt.RubyModeSection
		}
	}

	ctxConfig := pipeline.DefaultContextConfig()
	if rc.Context != nil {
		ctxConfig = *rc.Context
	}

	handler := &pipeline.TranslateHandler{
		Backend:                rc.Backend,
		BatchSize:              rc.BatchSize,
		MaxWordsPerBatch:       rc.MaxWordsPerBatch,
		FallbackShrink:         rc.FallbackShrink,
		Retry:                  rc.Retry,
		ResponseMode:           t.ResponseMode,
		Renderer:               t.Renderer,
		Glossary:               glossaryRes,
		TM:                     tmRes,
		Repair:                 repairOpts,
		Context:                ctxConfig,
		Protector:              prot,
		RubyEnabled:            t.RubyEnabled,
		RubyPreserveKinds:      t.RubyPreserveKinds,
		RubyMode:               rubyMode,
		Postprocess:            t.Postprocess,
		RubyRestorer:           rubyRestorer,
		RubyRetryBackends:      rubyRetryBackends,
		InlineBootstrap:        inlineBootstrap,
		MaxTermsPer1000Chars:   maxTermsPer1000,
		MinBootstrapSourceLen:  minSourceLen,
		InlineConflictStrategy: inlineConflictStr,
		Reporter:               reporter,
		Logger:                 logger,
	}

	return pipeline.Round{
		Concurrency: rc.Concurrency,
		Retry:       rc.Retry,
		Context:     rc.Context,
		Handler:     handler,
	}, nil
}

func buildExtractPipelineRound(
	rc RoundConfig,
	glossaryRes glossary.Glossary,
	logger *slog.Logger,
	reporter progress.Reporter,
) (pipeline.Round, error) {
	e := rc.Extract
	if e == nil {
		e = &ExtractRoundConfig{}
	}

	handler := &pipeline.ExtractHandler{
		Backends:             []backend.Backend{rc.Backend},
		Renderer:             e.Renderer,
		Glossary:             glossaryRes,
		Retry:                rc.Retry,
		BatchSize:            rc.BatchSize,
		MaxWordsPerBatch:     e.MaxWordsPerBatch,
		MaxTermsPer1000Chars: e.MaxTermsPer1000Chars,
		MinSourceLen:         e.MinSourceLen,
		Repair:               e.Repair,
		Logger:               logger,
		Reporter:             reporter,
	}

	return pipeline.Round{
		Concurrency: rc.Concurrency,
		Retry:       rc.Retry,
		Context:     rc.Context,
		Handler:     handler,
	}, nil
}
