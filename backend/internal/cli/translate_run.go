package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

type translateOptions struct {
	inputs        []string
	output        string
	from          string
	to            string
	glossaryPath  string
	bootstrapMode string
	profile       string
	prompt        string
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

	cliCfg, err := config.LoadCLIConfig(rt.configPath)
	if err != nil {
		return err
	}

	if err := applyTranslateFlags(cliCfg, opts); err != nil {
		return err
	}

	reporter, err := newReporter(rt)
	if err != nil {
		return err
	}
	defer func() { _ = reporter.Close() }()

	engOpts, err := buildEngineFromCLIConfig(cliCfg)
	if err != nil {
		return err
	}
	if engOpts.Config.QA.Enabled {
		rt.logger.Warn("QA is configured but not yet supported in CLI mode; QA settings will be ignored")
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
	for _, fj := range jobs {
		rt.logger.Info("translation queued", "input", fj.InputPath, "output", fj.OutputPath)
		if err := translateSingleFile(cmd.Context(), eng, fj, opts.from, opts.to); err != nil {
			failed = append(failed, fmt.Sprintf("%v", err))
			rt.logger.Error("translation failed", "input", fj.InputPath, "err", err)
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
func buildEngineFromCLIConfig(cliCfg *config.CLIConfig) (*engine.Options, error) {
	if len(cliCfg.Execution.Rounds) == 0 {
		return nil, fmt.Errorf("execution.rounds 不能为空")
	}

	// 找到第一个 translate 轮次作为主翻译配置
	var firstTranslateRound *config.CLIConfigTranslateRound
	for _, r := range cliCfg.Execution.Rounds {
		if r.Mode == "translate" && r.Translate != nil {
			firstTranslateRound = r.Translate
			break
		}
	}
	if firstTranslateRound == nil {
		return nil, fmt.Errorf("execution.rounds 中必须至少有一个 translate 轮次")
	}

	firstProfile := resolveProfile(cliCfg, firstTranslateRound.Profile)

	firstPromptContent := resolvePromptContent(cliCfg, firstTranslateRound.Prompt)
	if firstPromptContent == "" {
		return nil, fmt.Errorf("prompt_templates %q has no content (translation prompt is required)", firstTranslateRound.Prompt)
	}

	cfg := &engine.Config{
		SourceLang: cliCfg.SourceLang,
		TargetLang: cliCfg.TargetLang,
		TranslateDefaults: engine.TranslateDefaults{
			BatchSize:        firstTranslateRound.BatchSize,
			MaxWordsPerBatch: firstTranslateRound.MaxWordsPerBatch,
			Concurrency:      firstTranslateRound.Concurrency,
			FallbackShrink:   firstTranslateRound.FallbackShrink,
			Retry:            toBackendRetryPolicy(firstTranslateRound.Retry),
		},
		Repair: repair.Config{
			Enabled:              firstProfile.Repair.Enabled,
			JSONStructural:       firstProfile.Repair.JSONStructural,
			SchemaAliases:        firstProfile.Repair.SchemaAliases,
			Partial:              firstProfile.Repair.Partial,
			PartialThreshold:     firstProfile.Repair.PartialThreshold,
			PlaceholderNormalize: firstProfile.Repair.PlaceholderNormalize,
			PromptUpgrade:        firstProfile.Repair.PromptUpgrade,
		}.ToOptions(),
		Ruby: engine.RubyConfig{
			Enabled:       firstProfile.Ruby.Enabled,
			PreserveKinds: firstProfile.Ruby.PreserveKinds,
		},
		Glossary: engine.GlossaryConfig{
			Enabled:   cliCfg.Glossary.Enabled,
			Path:      cliCfg.Glossary.Path,
			Save:      cliCfg.Glossary.Save,
			Bootstrap: firstProfile.Bootstrap,
		},
		TMEnabled: cliCfg.TranslationMemory.Enabled,
		QA: qa.Config{
			Enabled:        firstProfile.QA.Enabled,
			AutoReject:     firstProfile.QA.AutoReject,
			LengthMethod:   qa.LengthMethod(firstProfile.QA.LengthMethod),
			LengthRatioMin: firstProfile.QA.LengthRatioMin,
			LengthRatioMax: firstProfile.QA.LengthRatioMax,
		},
	}

	var rounds []engine.Round
	for i, r := range cliCfg.Execution.Rounds {
		switch r.Mode {
		case "translate":
			if r.Translate == nil {
				return nil, fmt.Errorf("execution.rounds[%d]: mode=translate requires translate config", i)
			}
		case "extract":
			// CLI 模式下独立自举由 engine 内部处理
			continue
		default:
			return nil, fmt.Errorf("execution.rounds[%d]: unsupported mode %q", i, r.Mode)
		}

		t := r.Translate
		bCfg, ok := cliCfg.Backends[r.Backend]
		if !ok {
			return nil, fmt.Errorf("backend %q not found in backends", r.Backend)
		}
		b, err := backend.Build(backend.Config{
			Name:               r.Backend,
			Type:               bCfg.Type,
			Enabled:            bCfg.Enabled,
			RateLimitPerMinute: bCfg.RateLimitPerMinute,
			Options:            bCfg.Options,
		})
		if err != nil {
			return nil, fmt.Errorf("build backend %q: %w", r.Backend, err)
		}

		if bCfg.RateLimitPerMinute > 0 {
			limiter := backend.NewRateLimiterPerMinute(bCfg.RateLimitPerMinute)
			b = backend.NewRateLimitedBackend(b, limiter)
		}

		var roundRenderer *prompt.Renderer
		if promptContent := resolvePromptContent(cliCfg, t.Prompt); promptContent != "" {
			roundRenderer, err = prompt.NewRenderer(promptContent)
			if err != nil {
				return nil, fmt.Errorf("build renderer for prompt %q: %w", t.Prompt, err)
			}
		}

		var roundRepair *repair.Config
		var roundContext *pipeline.ContextConfig
		var roundPostprocess *pipeline.PostprocessConfig
		var roundRuby engine.RubyConfig
		var roundProtectRules []string
		if profileCfg, ok := cliCfg.TranslationProfiles[t.Profile]; ok {
			rc := repair.Config{
				Enabled:              profileCfg.Repair.Enabled,
				JSONStructural:       profileCfg.Repair.JSONStructural,
				SchemaAliases:        profileCfg.Repair.SchemaAliases,
				Partial:              profileCfg.Repair.Partial,
				PartialThreshold:     profileCfg.Repair.PartialThreshold,
				PlaceholderNormalize: profileCfg.Repair.PlaceholderNormalize,
				PromptUpgrade:        profileCfg.Repair.PromptUpgrade,
			}
			roundRepair = &rc
			ctx := pipeline.ContextConfig{
				Enabled:  profileCfg.Context.Enabled,
				Before:   profileCfg.Context.Before,
				After:    profileCfg.Context.After,
				MaxChars: profileCfg.Context.MaxChars,
			}
			roundContext = &ctx
			if profileCfg.Postprocess.Enabled {
				pp := pipeline.PostprocessConfig{
					TrimSpaces: profileCfg.Postprocess.TrimSpaces,
				}
				roundPostprocess = &pp
			}
			roundRuby = engine.RubyConfig{
				Enabled:       profileCfg.Ruby.Enabled,
				PreserveKinds: profileCfg.Ruby.PreserveKinds,
			}
			if profileCfg.Protect.Enabled {
				roundProtectRules = profileCfg.Protect.Rules
			}
		}

		rounds = append(rounds, engine.Round{
			Name:              r.Name,
			Backend:           b,
			BatchSize:         t.BatchSize,
			MaxWordsPerBatch:  t.MaxWordsPerBatch,
			Concurrency:       t.Concurrency,
			FallbackShrink:    t.FallbackShrink,
			Retry:             toBackendRetryPolicy(t.Retry),
			Renderer:          roundRenderer,
			Repair:            roundRepair,
			ResponseMode:      responseModeFromOptions(bCfg.Options),
			Mode:              pipeline.RoundModeTranslate,
			ProtectRules:      roundProtectRules,
			RubyEnabled:       roundRuby.Enabled,
			RubyPreserveKinds: roundRuby.PreserveKinds,
			Context:           roundContext,
			Postprocess:       roundPostprocess,
		})
	}

	var rubyRetryBackends []backend.Backend
	retryName := firstProfile.Ruby.RetryBackend
	if retryName != "" {
		bCfg, ok := cliCfg.Backends[retryName]
		if !ok {
			return nil, fmt.Errorf("ruby retry backend %q not found in backends", retryName)
		}
		b, bErr := backend.Build(backend.Config{
			Name:               retryName,
			Type:               bCfg.Type,
			Enabled:            bCfg.Enabled,
			RateLimitPerMinute: bCfg.RateLimitPerMinute,
			Options:            bCfg.Options,
		})
		if bErr != nil {
			return nil, fmt.Errorf("build ruby retry backend %q: %w", retryName, bErr)
		}
		if bCfg.RateLimitPerMinute > 0 {
			limiter := backend.NewRateLimiterPerMinute(bCfg.RateLimitPerMinute)
			b = backend.NewRateLimitedBackend(b, limiter)
		}
		rubyRetryBackends = []backend.Backend{b}
	}

	return &engine.Options{
		Config:            cfg,
		Rounds:            rounds,
		RubyRetryBackends: rubyRetryBackends,
	}, nil
}

func resolveProfile(cliCfg *config.CLIConfig, name string) config.CLIConfigTranslationProfile {
	if p, ok := cliCfg.TranslationProfiles[name]; ok {
		return p
	}
	return config.CLIConfigTranslationProfile{}
}

func resolvePromptContent(cliCfg *config.CLIConfig, name string) string {
	if pt, ok := cliCfg.PromptTemplates[name]; ok {
		return pt.Content
	}
	return ""
}

// translateSingleFile 使用 TranslateRound 轮次循环翻译单个文件。
func translateSingleFile(ctx context.Context, eng *engine.Engine, fj FileJob, sourceLang, targetLang string) error {
	p, err := parser.DetectByExt(fj.InputPath)
	if err != nil {
		return err
	}

	reader, err := os.Open(fj.InputPath)
	if err != nil {
		return fmt.Errorf("cli: open source: %w", err)
	}
	doc, parseErr := p.Parse(ctx, reader)
	reader.Close()
	if parseErr != nil {
		return fmt.Errorf("cli: parse: %w", parseErr)
	}

	if sourceLang != "" {
		doc.SourceLang = sourceLang
	}
	if targetLang != "" {
		doc.TargetLang = targetLang
	}

	// 轮次循环
	for roundIdx := range eng.Rounds() {
		segmentIndexes := collectPendingOrFailed(doc, roundIdx)
		if len(segmentIndexes) == 0 {
			break
		}
		if roundIdx > 0 {
			restoreFailedSegments(doc, segmentIndexes)
		}

		_, err := eng.TranslateRound(ctx, roundIdx, doc, engine.WithSegmentFilter(segmentIndexes))
		if err != nil {
			return fmt.Errorf("cli: translate round %d: %w", roundIdx, err)
		}
	}

	original, err := os.Open(fj.InputPath)
	if err != nil {
		return fmt.Errorf("cli: reopen source: %w", err)
	}
	defer func() { _ = original.Close() }()

	writer, err := createAtomicWriter(fj.OutputPath)
	if err != nil {
		return err
	}
	defer func() { _ = writer.Close() }()

	if err := p.Render(ctx, doc, original, writer); err != nil {
		return fmt.Errorf("cli: render: %w", err)
	}

	eng.SaveGlossary(ctx)
	return nil
}

// collectPendingOrFailed 收集待翻译或前一轮失败的段落索引。
func collectPendingOrFailed(doc *pipeline.Document, roundIdx int) []int {
	if roundIdx == 0 {
		// 首轮：收集所有 pending 段落
		var indexes []int
		for i, seg := range doc.Segments {
			if seg.Skip || !seg.Translate {
				continue
			}
			if seg.Target == "" {
				indexes = append(indexes, i)
			}
		}
		return indexes
	}
	// 后续轮次：收集失败段落（Target 为空）
	failedSet := pipeline.ParseFailedIndices(doc.Vars)
	var indexes []int
	for idx := range failedSet {
		indexes = append(indexes, idx)
	}
	return indexes
}

// restoreFailedSegments 还原失败段落的 Source 为 OriginalSource。
// CLI 每轮共享 Document，translate 模式 Protect 修改 seg.Source，
// 下一轮需要还原以重新执行 Protect。
func restoreFailedSegments(doc *pipeline.Document, indexes []int) {
	for _, idx := range indexes {
		if idx < 0 || idx >= len(doc.Segments) {
			continue
		}
		seg := &doc.Segments[idx]
		if seg.OriginalSource != "" {
			seg.Source = seg.OriginalSource
		}
		seg.Protected = nil
		seg.Target = ""
	}
}

func responseModeFromOptions(opts map[string]any) string {
	if v, ok := opts["response_format"].(string); ok {
		return v
	}
	return ""
}

// toBackendRetryPolicy 将 config.RetryConfig 转换为 backend.RetryPolicy。
func toBackendRetryPolicy(cfg config.RetryConfig) backend.RetryPolicy {
	return backend.RetryPolicy{
		MaxAttempts: cfg.MaxAttempts,
		Backoff:     time.Duration(cfg.BackoffMs) * time.Millisecond,
		Jitter:      cfg.Jitter,
	}
}
