package engine

import (
	"fmt"
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

// Round 描述一轮翻译的执行配置。
type Round struct {
	Backend          backend.Backend
	Name             string
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
}

// RuntimeResources 封装可选的运行时资源。
type RuntimeResources struct {
	Glossary glossary.Glossary
	TM       tm.TranslationMemory
}

func resolveName(name string, idx int) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf("round-%d", idx+1)
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

// buildStagesRounds 将 engine.Round 转换为 pipeline.Round。
func buildStagesRounds(in []Round, cfg *Config) []pipeline.Round {
	if len(in) == 0 {
		return nil
	}
	globalRetry := cfg.TranslateDefaults.Retry
	out := make([]pipeline.Round, 0, len(in))
	for i, r := range in {
		retry := r.Retry
		if retry.MaxAttempts == 0 {
			retry = globalRetry
		}

		var roundRepair *repair.Options
		if r.Repair != nil {
			rc := *r.Repair
			rc.Normalize()
			opts := rc.ToOptions()
			roundRepair = &opts
		}

		mode := r.Mode
		if mode == "" {
			mode = pipeline.RoundModeTranslate
		}

		// 构建 per-round Protector
		var prot protect.Protector
		if mode == pipeline.RoundModeTranslate && (r.RubyEnabled || len(r.ProtectRules) > 0) {
			ps := []protect.Protector{}
			if r.RubyEnabled {
				ps = append(ps, &ruby.Extractor{})
			}
			if len(r.ProtectRules) > 0 {
				ps = append(ps, protect.FromRules(r.ProtectRules))
			}
			prot = protect.Compose(ps...)
		}

		rubyMode := ""
		if r.RubyEnabled {
			rubyMode = prompt.RubyModeJSON
			if r.ResponseMode == "text" {
				rubyMode = prompt.RubyModeSection
			}
		}

		var roundCtx *pipeline.ContextConfig
		if r.Context != nil {
			roundCtx = r.Context
		}

		var roundPostprocess *pipeline.PostprocessConfig
		if r.Postprocess != nil {
			roundPostprocess = &pipeline.PostprocessConfig{
				TrimSpaces: r.Postprocess.TrimSpaces,
			}
		}

		out = append(out, pipeline.Round{
			Name:              resolveName(r.Name, i),
			Backend:           r.Backend,
			BatchSize:         resolveDefault(r.BatchSize, cfg.TranslateDefaults.BatchSize, 1),
			MaxWordsPerBatch:  r.MaxWordsPerBatch,
			Concurrency:       resolveDefault(r.Concurrency, cfg.TranslateDefaults.Concurrency, 1),
			FallbackShrink:    resolveShrink(r.FallbackShrink, cfg.TranslateDefaults.FallbackShrink),
			Retry:             retry,
			Renderer:          r.Renderer,
			Repair:            roundRepair,
			ResponseMode:      r.ResponseMode,
			Mode:              mode,
			Protector:         prot,
			RubyEnabled:       r.RubyEnabled,
			RubyPreserveKinds: r.RubyPreserveKinds,
			RubyMode:          rubyMode,
			Context:           roundCtx,
			Postprocess:       roundPostprocess,
		})
	}
	return out
}
