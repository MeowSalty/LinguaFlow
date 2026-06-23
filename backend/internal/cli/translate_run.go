package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

type translateOptions struct {
	inputs        []string
	output        string
	from          string
	to            string
	glossaryPath  string
	bootstrapMode string
	profile       string // 覆盖执行轮次使用的翻译策略
	prompt        string // 覆盖执行轮次使用的提示词模板
}

func runTranslate(cmd *cobra.Command, rt *appCtx, opts translateOptions) error {
	if len(opts.inputs) == 0 {
		return fmt.Errorf("--input/-i 必填")
	}
	if opts.output == "" {
		return fmt.Errorf("--output/-o 必填")
	}

	jobs, report, err := buildTranslateJobs(opts.inputs, opts.output)
	if err != nil {
		return err
	}

	// 加载新格式配置
	cliCfg, err := config.LoadCLIConfig(rt.configPath)
	if err != nil {
		return err
	}

	// 应用 CLI flag 覆盖
	if err := applyTranslateFlags(cliCfg, opts); err != nil {
		return err
	}

	reporter, err := newReporter(rt)
	if err != nil {
		return err
	}
	defer func() { _ = reporter.Close() }()

	// 从 CLIConfig 构造 engine.Options
	engOpts, err := buildEngineFromCLIConfig(cliCfg)
	if err != nil {
		return err
	}
	engOpts.Logger = rt.logger
	engOpts.Reporter = reporter

	eng, err := engine.NewWithOptions(*engOpts)
	if err != nil {
		return err
	}
	defer func() { _ = eng.Close() }()

	var failed []string
	for _, ignored := range report.Ignored {
		rt.logger.Info("ignored unsupported file", "path", ignored.Path, "reason", ignored.Reason)
	}
	for _, job := range jobs {
		rt.logger.Info("translation queued")
		job.SourceLang = opts.from
		job.TargetLang = opts.to
		if err := eng.Translate(cmd.Context(), job); err != nil {
			failed = append(failed, fmt.Sprintf("%v", err))
			rt.logger.Error("translation failed", "err", err)
			continue
		}
	}

	rt.logger.Info("batch translate summary",
		"succeeded", len(jobs)-len(failed),
		"failed", len(failed),
		"ignored", len(report.Ignored))
	if len(failed) > 0 {
		return fmt.Errorf("批量翻译完成，但有 %d 个文件失败:\n%s", len(failed), strings.Join(failed, "\n"))
	}
	return nil
}

// buildEngineFromCLIConfig 从 CLIConfig 构造 engine.Options。
//
// 全局 Config（split/protect/postprocess 等文档级配置）取第一轮的 profile 作为默认。
// 每个 round 独立解析后端引用、提示词模板和翻译策略。
func buildEngineFromCLIConfig(cliCfg *config.CLIConfig) (*engine.Options, error) {
	if len(cliCfg.Execution.Rounds) == 0 {
		return nil, fmt.Errorf("execution.rounds 不能为空")
	}

	// ── 1. 取第一轮的 profile 和 prompt 作为全局默认 ──
	firstRound := cliCfg.Execution.Rounds[0]
	firstProfile := resolveProfile(cliCfg, firstRound.Profile)

	// 全局 Prompt 配置：翻译模板必填，不再回退到内置默认值
	firstPromptContent := resolvePromptContent(cliCfg, firstRound.Prompt)
	if firstPromptContent == "" {
		return nil, fmt.Errorf("prompt_templates %q has no content (translation prompt is required)", firstRound.Prompt)
	}

	cfg := &config.Config{
		Version:    cliCfg.Version,
		SourceLang: cliCfg.SourceLang,
		TargetLang: cliCfg.TargetLang,
		Pipeline: config.PipelineConfig{
			Split:       firstProfile.Split,
			Protect:     firstProfile.Protect,
			Postprocess: firstProfile.Postprocess,
			Translate: config.TranslateConfig{
				BatchSize:       firstRound.BatchSize,
				Concurrency:     firstRound.Concurrency,
				FallbackShrink:  firstRound.FallbackShrink,
				RateLimitPerSec: firstRound.RateLimitPerSec,
				Retry:           firstRound.Retry,
				Repair:          firstProfile.Repair,
			},
		},
		Prompt: config.PromptConfig{
			SystemTemplateContent: firstPromptContent,
		},
		Glossary:          cliCfg.Glossary,
		TranslationMemory: cliCfg.TranslationMemory,
		Plugins:           cliCfg.Plugins,
		Output:            cliCfg.Output,
		Log:               cliCfg.Log,
	}

	// ── 1b. 解析 bootstrap 模板引用 ──
	if cliCfg.Glossary.Bootstrap.Mode != config.BootstrapModeOff {
		pt, ok := cliCfg.PromptTemplates[cliCfg.Glossary.Bootstrap.Template]
		if !ok {
			return nil, fmt.Errorf("prompt_templates %q not found (referenced by glossary.bootstrap.template)", cliCfg.Glossary.Bootstrap.Template)
		}
		bootstrapContent := pt.BootstrapContent
		if bootstrapContent == "" && pt.BootstrapFile != "" {
			data, err := os.ReadFile(pt.BootstrapFile)
			if err != nil {
				return nil, fmt.Errorf("read bootstrap file %q: %w", pt.BootstrapFile, err)
			}
			bootstrapContent = string(data)
		}
		if bootstrapContent == "" {
			return nil, fmt.Errorf("prompt_templates %q has no bootstrap_content (required when glossary.bootstrap.mode is %q)",
				cliCfg.Glossary.Bootstrap.Template, cliCfg.Glossary.Bootstrap.Mode)
		}
		cfg.Glossary.Bootstrap.TemplateContent = bootstrapContent
	}

	// ── 2. 构造每轮配置 ──
	var rounds []engine.Round
	for _, r := range cliCfg.Execution.Rounds {
		// 解析后端引用
		bCfg, ok := cliCfg.Backends[r.Backend]
		if !ok {
			return nil, fmt.Errorf("backend %q not found in backends", r.Backend)
		}
		backends, err := engine.BuildBackends([]config.BackendConfig{{
			Name:            r.Backend,
			Type:            bCfg.Type,
			Enabled:         bCfg.Enabled,
			RateLimitPerSec: bCfg.RateLimitPerSec,
			Options:         bCfg.Options,
		}})
		if err != nil {
			return nil, fmt.Errorf("build backend %q: %w", r.Backend, err)
		}

		// 解析提示词引用，构造轮次级 Renderer
		var renderer *prompt.Renderer
		if promptContent := resolvePromptContent(cliCfg, r.Prompt); promptContent != "" {
			renderer, err = prompt.NewRenderer(config.PromptConfig{
				SystemTemplateContent: promptContent,
			})
			if err != nil {
				return nil, fmt.Errorf("build renderer for prompt %q: %w", r.Prompt, err)
			}
		}

		// 解析策略引用，构造轮次级 Repair
		var roundRepair *config.RepairConfig
		if profileCfg, ok := cliCfg.TranslationProfiles[r.Profile]; ok {
			rc := profileCfg.Repair
			roundRepair = &rc
		}

		rounds = append(rounds, engine.Round{
			Name:            r.Name,
			Backends:        backends,
			BatchSize:       r.BatchSize,
			Concurrency:     r.Concurrency,
			FallbackShrink:  r.FallbackShrink,
			RateLimitPerSec: r.RateLimitPerSec,
			Retry: backend.RetryPolicy{
				MaxAttempts: r.Retry.MaxAttempts,
				Backoff:     time.Duration(r.Retry.BackoffMs) * time.Millisecond,
				Jitter:      r.Retry.Jitter,
			},
			Renderer: renderer,
			Repair:   roundRepair,
		})
	}

	return &engine.Options{
		Config: cfg,
		Rounds: rounds,
	}, nil
}

// resolveProfile 从 CLIConfig 的 translation_profiles map 中查找策略。
// 未找到时返回零值。
func resolveProfile(cliCfg *config.CLIConfig, name string) config.CLIConfigTranslationProfile {
	if p, ok := cliCfg.TranslationProfiles[name]; ok {
		return p
	}
	return config.CLIConfigTranslationProfile{}
}

// resolvePromptContent 从 CLIConfig 的 prompt_templates map 中查找提示词内容。
// 未找到时返回空串（使用内置默认值）。
func resolvePromptContent(cliCfg *config.CLIConfig, name string) string {
	if pt, ok := cliCfg.PromptTemplates[name]; ok {
		return pt.Content
	}
	return ""
}
