package engine

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// Options 是 Engine 的构造参数。
// 调用方负责将后端配置解析为实例并按轮次组装。
type Options struct {
	// Rounds 是执行计划。每个元素对应一轮翻译，必须非空。
	// 单轮模式：一个元素，Backends 包含所有后端。
	// 多轮模式：多个元素，每轮使用对应的后端列表。
	Rounds []Round

	// BootstrapBackends 是术语自举阶段使用的后端列表。
	// 为空时回退到 Rounds[0].Backend。
	BootstrapBackends []backend.Backend

	// RubyRetryBackends 是注音对齐重试使用的后端列表。
	// 为空时回退到 Rounds[0].Backend。
	RubyRetryBackends []backend.Backend

	// Config 是策略配置（分割、保护、提示词、术语表等）。
	// 不包含 backends、backend_mode、backend_order 字段。
	Config *config.Config

	// Logger 日志器。nil 时使用 slog.Default()。
	Logger *slog.Logger

	// Reporter 进度上报器。nil 时使用 progress.Nop{}。
	Reporter progress.Reporter

	// Resources 可选的运行时资源（术语表、翻译记忆）。
	Resources RuntimeResources
}

// Round 描述一轮翻译的执行配置。
type Round struct {
	// Backend 本轮使用的后端。
	// 必须非 nil。
	Backend backend.Backend

	// Name 轮次名称，用于日志。空值自动生成 "round-N"。
	Name string

	// BatchSize 本轮的批大小。<=0 时回退到全局默认。
	BatchSize int

	// Concurrency 本轮的并发数。<=0 时回退到全局默认。
	Concurrency int

	// FallbackShrink 本轮的批失败收缩系数。(0,1) 启用递归缩小；0 表示直接降到单段。
	FallbackShrink float64

	// Retry 本轮的重试策略。零值回退到全局默认。
	Retry backend.RetryPolicy

	// Renderer 本轮使用的提示词渲染器。nil 时回退到 Options.Config 的 Prompt 配置。
	Renderer *prompt.Renderer

	// Repair 本轮的修复策略配置。nil 时回退到 Options.Config 的 Repair 配置。
	// 使用指针以区分"未设置"（nil）和"显式设为零值"。
	Repair *config.RepairConfig
}

// RuntimeResources 封装可选的运行时资源。
// 调用方可以注入自定义实现，nil 字段使用默认值。
type RuntimeResources struct {
	Glossary glossary.Glossary
	TM       tm.TranslationMemory
}

// resolveName 返回 name（非空时），否则返回 "round-N"。
func resolveName(name string, idx int) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf("round-%d", idx+1)
}

// resolveDefault 返回 val（>0 时），否则返回 global（>0 时），否则返回 fallback。
func resolveDefault(val, global, fallback int) int {
	if val > 0 {
		return val
	}
	if global > 0 {
		return global
	}
	return fallback
}

// resolveShrink 返回 val（>0 时），否则返回 global。
func resolveShrink(val, global float64) float64 {
	if val > 0 {
		return val
	}
	return global
}

// buildStagesRounds 将 engine.Round 转换为 pipeline.Round。
// 用全局默认值填充零值字段。
func buildStagesRounds(in []Round, cfg *config.Config) []pipeline.Round {
	if len(in) == 0 {
		return nil
	}
	globalRetry := backend.RetryPolicy{
		MaxAttempts: cfg.Pipeline.Translate.Retry.MaxAttempts,
		Backoff:     time.Duration(cfg.Pipeline.Translate.Retry.BackoffMs) * time.Millisecond,
		Jitter:      cfg.Pipeline.Translate.Retry.Jitter,
	}
	out := make([]pipeline.Round, 0, len(in))
	for i, r := range in {
		// Retry 零值回退到全局
		retry := r.Retry
		if retry.MaxAttempts == 0 {
			retry = globalRetry
		}

		// 解析轮次级 Repair
		var roundRepair *repair.Options
		if r.Repair != nil {
			rc := *r.Repair
			rc.Normalize()
			opts := toRepairOptions(rc)
			roundRepair = &opts
		}

		out = append(out, pipeline.Round{
			Name:           resolveName(r.Name, i),
			Backend:        r.Backend,
			BatchSize:      resolveDefault(r.BatchSize, cfg.Pipeline.Translate.BatchSize, 1),
			Concurrency:    resolveDefault(r.Concurrency, cfg.Pipeline.Translate.Concurrency, 1),
			FallbackShrink: resolveShrink(r.FallbackShrink, cfg.Pipeline.Translate.FallbackShrink),
			Retry:          retry,
			Renderer:       r.Renderer,
			Repair:         roundRepair,
		})
	}
	return out
}
