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
func (r *JobRunner) buildEngineFromSnapshot(
	ctx context.Context,
	snapshot *service.JobExecutionSnapshot,
	resources engine.RuntimeResources,
	reporter progress.Reporter,
) (*engine.Engine, error) {
	var rounds []engine.Round
	for _, rs := range snapshot.Rounds {
		// 仅处理翻译轮次，跳过抽取轮次（抽取轮次由独立流程处理）
		if rs.Mode != "translate" || rs.Translate == nil {
			continue
		}
		t := rs.Translate

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
		roundRenderer, err := prompt.NewRenderer(t.Prompt.Content)
		if err != nil {
			return nil, fmt.Errorf("round %q build renderer: %w", rs.Name, err)
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
		rounds = append(rounds, engine.Round{
			Backend:          b,
			Name:             rs.Name,
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

	return engine.NewWithOptions(engine.Options{
		Rounds:            rounds,
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

	// 从第一个翻译轮次读取策略配置
	for _, rs := range snapshot.Rounds {
		if rs.Mode != "translate" || rs.Translate == nil {
			continue
		}
		s := rs.Translate.Strategy
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
				Enabled:                s.Glossary.Bootstrap.Enabled,
				MaxTermsPer1000Chars:   s.Glossary.Bootstrap.MaxTermsPer1000Chars,
				MinSourceLen:           s.Glossary.Bootstrap.MinSourceLen,
				InlineConflictStrategy: s.Glossary.Bootstrap.InlineConflictStrategy,
			},
		}
		cfg.QA = qa.Config{
			Enabled:        s.QA.Enabled,
			AutoReject:     s.QA.AutoReject,
			LengthMethod:   qa.LengthMethod(s.QA.LengthMethod),
			LengthRatioMin: s.QA.LengthRatioMin,
			LengthRatioMax: s.QA.LengthRatioMax,
		}
		break
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
