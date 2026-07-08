package worker

import (
	"context"
	"fmt"
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
)

// buildEngineFromSnapshot 从任务快照构建引擎实例。
// 后端实例由快照中的 Type + Options 直接构建，不依赖名称查找。
func (r *TranslationRunner) buildEngineFromSnapshot(
	ctx context.Context,
	snapshot *service.JobExecutionSnapshot,
	resources engine.RuntimeResources,
	reporter progress.Reporter,
) (*engine.Engine, error) {
	var rounds []engine.Round
	for _, rs := range snapshot.Rounds {
		// 从快照直接构建后端实例（无需名称匹配）
		bCfg := backend.Config{
			Name:    rs.Backend.Name, // 仅用于日志，不用于匹配
			Type:    rs.Backend.Type,
			Enabled: true,
			Options: rs.Backend.Options,
		}
		b, err := backend.Build(bCfg)
		if err != nil {
			return nil, fmt.Errorf("round %q build backend: %w", rs.Name, err)
		}

		// 使用共享 limiter pool 包装后端
		if r.limiterPool != nil && rs.Backend.RateLimitPerMinute > 0 {
			limiter := r.limiterPool.Get(rs.Backend.ID, rs.Backend.RateLimitPerMinute)
			b = backend.NewRateLimitedBackend(b, limiter)
		}

		// 为每轮构建独立的 Renderer（使用该轮自己的 prompt 模板）
		roundRenderer, err := prompt.NewRenderer(rs.Prompt.Content)
		if err != nil {
			return nil, fmt.Errorf("round %q build renderer: %w", rs.Name, err)
		}

		rounds = append(rounds, engine.Round{
			Backend:          b,
			Name:             rs.Name,
			BatchSize:        rs.BatchSize,
			MaxWordsPerBatch: rs.MaxWordsPerBatch,
			Concurrency:      rs.Concurrency,
			FallbackShrink:   rs.FallbackShrink,
			Retry: backend.RetryPolicy{
				MaxAttempts: rs.Retry.MaxAttempts,
				Backoff:     time.Duration(rs.Retry.BackoffMs) * time.Millisecond,
				Jitter:      rs.Retry.Jitter,
			},
			Renderer:          roundRenderer,
			ResponseMode:      responseModeFromBackendOptions(rs.Backend.Options),
			Mode:              pipeline.RoundModeTranslate,
			ProtectRules:      rs.Strategy.Protect.Rules,
			RubyEnabled:       rs.Strategy.Ruby.Enabled,
			RubyPreserveKinds: rs.Strategy.Ruby.PreserveKinds,
			Context: &pipeline.ContextConfig{
				Enabled:  rs.Strategy.Context.Enabled,
				Before:   rs.Strategy.Context.Before,
				After:    rs.Strategy.Context.After,
				MaxChars: rs.Strategy.Context.MaxChars,
			},
			Postprocess: &pipeline.PostprocessConfig{
				TrimSpaces: rs.Strategy.Postprocess.TrimSpaces,
			},
		})
	}

	// 构建策略配置（不含后端信息）
	cfg := buildEngineConfig(snapshot)

	// 构建注音对齐重试后端
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
		if r.limiterPool != nil && snapshot.RubyRetry.Backend.RateLimitPerMinute > 0 {
			limiter := r.limiterPool.Get(snapshot.RubyRetry.Backend.ID, snapshot.RubyRetry.Backend.RateLimitPerMinute)
			rrBackend = backend.NewRateLimitedBackend(rrBackend, limiter)
		}
		rubyRetryBackends = []backend.Backend{rrBackend}
	}

	// 构建独立自举后端
	var bootstrapBackends []backend.Backend
	if snapshot.Bootstrap != nil && snapshot.Bootstrap.Enabled {
		bsCfg := backend.Config{
			Name:    snapshot.Bootstrap.Backend.Name,
			Type:    snapshot.Bootstrap.Backend.Type,
			Enabled: true,
			Options: snapshot.Bootstrap.Backend.Options,
		}
		bsBackend, err := backend.Build(bsCfg)
		if err != nil {
			return nil, fmt.Errorf("bootstrap backend: %w", err)
		}
		if r.limiterPool != nil && snapshot.Bootstrap.Backend.RateLimitPerMinute > 0 {
			limiter := r.limiterPool.Get(snapshot.Bootstrap.Backend.ID, snapshot.Bootstrap.Backend.RateLimitPerMinute)
			bsBackend = backend.NewRateLimitedBackend(bsBackend, limiter)
		}
		bootstrapBackends = []backend.Backend{bsBackend}
	}

	return engine.NewWithOptions(engine.Options{
		Rounds:            rounds,
		BootstrapBackends: bootstrapBackends,
		RubyRetryBackends: rubyRetryBackends,
		Config:            cfg,
		Logger:            r.logger,
		Resources:         resources,
		Reporter:          reporter,
	})
}

// buildEngineConfig 从快照构建引擎运行时配置。
func buildEngineConfig(snapshot *service.JobExecutionSnapshot) *engine.Config {
	cfg := &engine.Config{
		SourceLang: snapshot.SourceLang,
		TargetLang: snapshot.TargetLang,
		TMEnabled:  snapshot.TMEnabled,
	}

	if len(snapshot.Rounds) > 0 {
		s := snapshot.Rounds[0].Strategy
		rc := repair.Config{
			Enabled:              s.Repair.Enabled,
			JSONStructural:       s.Repair.JSONStructural,
			SchemaAliases:        s.Repair.SchemaAliases,
			Partial:              s.Repair.Partial,
			PartialThreshold:     s.Repair.PartialThreshold,
			PlaceholderNormalize: s.Repair.PlaceholderNormalize,
			PromptUpgrade:        s.Repair.PromptUpgrade,
		}
		cfg.Repair = rc.ToOptions()
		cfg.Ruby = engine.RubyConfig{
			Enabled:       s.Ruby.Enabled,
			PreserveKinds: s.Ruby.PreserveKinds,
		}
		cfg.Glossary = engine.GlossaryConfig{
			Enabled: snapshot.GlossaryEnabled,
			Bootstrap: config.BootstrapConfig{
				MaxTermsPer1000Chars:   s.Glossary.Bootstrap.MaxTermsPer1000Chars,
				MinSourceLen:           s.Glossary.Bootstrap.MinSourceLen,
				InlineConflictStrategy: s.Glossary.Bootstrap.InlineConflictStrategy,
			},
		}
		cfg.QA = qa.Config{
			Enabled:        s.QA.Enabled,
			AutoReject:     s.QA.AutoReject,
			LengthRatioMin: s.QA.LengthRatioMin,
			LengthRatioMax: s.QA.LengthRatioMax,
		}
	}

	if snapshot.Bootstrap != nil {
		cfg.Glossary.Standalone = config.StandaloneBootstrapConfig{
			Enabled:          snapshot.Bootstrap.Enabled,
			TemplateContent:  snapshot.Bootstrap.TemplateContent,
			BatchSize:        snapshot.Bootstrap.BatchSize,
			Concurrency:      snapshot.Bootstrap.Concurrency,
			MaxTermsPerBatch: snapshot.Bootstrap.MaxTermsPerBatch,
			MinSourceLen:     snapshot.Bootstrap.MinSourceLen,
		}
	}

	return cfg
}

// responseModeFromBackendOptions 从后端 Options map 中读取 response_format 值。
// 用于设置 engine.Round.ResponseMode，使 pipeline 能区分 json/text 模式。
func responseModeFromBackendOptions(opts map[string]any) string {
	if v, ok := opts["response_format"].(string); ok {
		return v
	}
	return ""
}
