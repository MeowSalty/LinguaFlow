package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
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
// 计划级策略来自 execution.profile（按名查 translation_profiles，缺省回退内置默认
// 策略），translate 与 revise 轮统一从该唯一策略接入 protect/ruby。
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

	profileCfg := config.ResolveExecutionProfile(cliCfg)

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
			Enabled:              profileCfg.Repair.Enabled,
			JSONStructural:       profileCfg.Repair.JSONStructural,
			SchemaAliases:        profileCfg.Repair.SchemaAliases,
			PlaceholderNormalize: profileCfg.Repair.PlaceholderNormalize,
			PromptUpgrade:        profileCfg.Repair.PromptUpgrade,
		}.ToOptions(),
		Ruby: engine.RubyConfig{
			Enabled:       profileCfg.Ruby.Enabled,
			PreserveKinds: profileCfg.Ruby.PreserveKinds,
		},
		Glossary: engine.GlossaryConfig{
			Enabled:   cliCfg.Glossary.Enabled,
			Path:      cliCfg.Glossary.Path,
			Save:      cliCfg.Glossary.Save,
			Bootstrap: profileCfg.Bootstrap,
		},
		TMEnabled: cliCfg.TranslationMemory.Enabled,
		QA: qa.Config{
			Enabled:        profileCfg.QA.Enabled,
			AutoReject:     profileCfg.QA.AutoReject,
			Checks:         profileCfg.QA.Checks,
			LengthMethod:   qa.LengthMethod(profileCfg.QA.LengthMethod),
			LengthRatioMin: profileCfg.QA.LengthRatioMin,
			LengthRatioMax: profileCfg.QA.LengthRatioMax,
			SourceLang:     cliCfg.SourceLang,
			TargetLang:     cliCfg.TargetLang,
		},
	}

	// 计划级 protect 规则：Enabled→Rules，否则 nil（与 worker 引擎工厂同语义）。
	var protectRules []string
	if profileCfg.Protect.Enabled {
		protectRules = profileCfg.Protect.Rules
	}
	roundRuby := engine.RubyConfig{
		Enabled:       profileCfg.Ruby.Enabled,
		PreserveKinds: profileCfg.Ruby.PreserveKinds,
	}
	roundRepair := repair.Config{
		Enabled:              profileCfg.Repair.Enabled,
		JSONStructural:       profileCfg.Repair.JSONStructural,
		SchemaAliases:        profileCfg.Repair.SchemaAliases,
		PlaceholderNormalize: profileCfg.Repair.PlaceholderNormalize,
		PromptUpgrade:        profileCfg.Repair.PromptUpgrade,
	}
	roundContext := pipeline.ContextConfig{
		Enabled:  profileCfg.Context.Enabled,
		Before:   profileCfg.Context.Before,
		After:    profileCfg.Context.After,
		MaxChars: profileCfg.Context.MaxChars,
	}
	var roundPostprocess *pipeline.PostprocessConfig
	if profileCfg.Postprocess.Enabled {
		pp := pipeline.PostprocessConfig{
			TrimSpaces: profileCfg.Postprocess.TrimSpaces,
		}
		roundPostprocess = &pp
	}

	var rounds []engine.Round
	for i, r := range cliCfg.Execution.Rounds {
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

		switch r.Mode {
		case "translate":
			if r.Translate == nil {
				return nil, fmt.Errorf("execution.rounds[%d]: mode=translate requires translate config", i)
			}
			t := r.Translate

			var roundRenderer *prompt.Renderer
			if promptContent := resolvePromptContent(cliCfg, t.Prompt); promptContent != "" {
				roundRenderer, err = prompt.NewRenderer(promptContent)
				if err != nil {
					return nil, fmt.Errorf("build renderer for prompt %q: %w", t.Prompt, err)
				}
			}

			rc := roundRepair
			ctx := roundContext
			rounds = append(rounds, engine.Round{
				Backend:           b,
				BatchSize:         t.BatchSize,
				MaxWordsPerBatch:  t.MaxWordsPerBatch,
				Concurrency:       t.Concurrency,
				FallbackShrink:    t.FallbackShrink,
				Retry:             toBackendRetryPolicy(t.Retry),
				Renderer:          roundRenderer,
				Repair:            &rc,
				ResponseMode:      responseModeFromOptions(bCfg.Options),
				Mode:              pipeline.RoundModeTranslate,
				ProtectRules:      protectRules,
				RubyEnabled:       roundRuby.Enabled,
				RubyPreserveKinds: roundRuby.PreserveKinds,
				Context:           &ctx,
				Postprocess:       roundPostprocess,
			})

		case "revise":
			if r.Revise == nil {
				return nil, fmt.Errorf("execution.rounds[%d]: mode=revise requires revise config", i)
			}
			v := r.Revise

			renderer, err := prompt.NewReviseRenderer(templates.EmbeddedReviseTemplate())
			if err != nil {
				return nil, fmt.Errorf("build revise renderer: %w", err)
			}
			issueCodes, err := resolveReviseIssueCodes(v)
			if err != nil {
				return nil, fmt.Errorf("execution.rounds[%d]: %w", i, err)
			}

			// protect/ruby 与 translate 轮同源：统一取计划级唯一策略。
			rounds = append(rounds, engine.Round{
				Backend:           b,
				BatchSize:         v.BatchSize,
				MaxWordsPerBatch:  v.MaxWordsPerBatch,
				Concurrency:       v.Concurrency,
				Retry:             toBackendRetryPolicy(v.Retry),
				ResponseMode:      responseModeFromOptions(bCfg.Options),
				Mode:              pipeline.RoundModeRevise,
				ReviseRenderer:    renderer,
				IssueCodes:        issueCodes,
				ProtectRules:      protectRules,
				RubyEnabled:       roundRuby.Enabled,
				RubyPreserveKinds: roundRuby.PreserveKinds,
			})

		case "extract":
			if r.Extract == nil {
				return nil, fmt.Errorf("execution.rounds[%d]: mode=extract requires extract config", i)
			}
			e := r.Extract

			var extractRenderer *prompt.BootstrapRenderer
			if pt, ok := cliCfg.PromptTemplates[e.Template]; ok && pt.Content != "" {
				extractRenderer, err = prompt.NewBootstrapRenderer(pt.Content)
				if err != nil {
					return nil, fmt.Errorf("build bootstrap renderer for template %q: %w", e.Template, err)
				}
			}

			rounds = append(rounds, engine.Round{
				Backend:      b,
				BatchSize:    e.BatchSize,
				Concurrency:  e.Concurrency,
				Retry:        toBackendRetryPolicy(e.Retry),
				Mode:         pipeline.RoundModeExtract,
				ResponseMode: responseModeFromOptions(bCfg.Options),

				ExtractRenderer:             extractRenderer,
				ExtractMaxTermsPer1000Chars: e.MaxTermsPer1000Chars,
				ExtractMinSourceLen:         e.MinSourceLen,
				ExtractMaxWordsPerBatch:     e.MaxWordsPerBatch,
			})

		default:
			return nil, fmt.Errorf("execution.rounds[%d]: unsupported mode %q", i, r.Mode)
		}
	}

	var rubyRetryBackends []backend.Backend
	retryName := profileCfg.Ruby.RetryBackend
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

// resolveReviseIssueCodes 物化修订轮的语义 issue 目标集合（与 service 层快照语义
// 一致）：with_issues（默认/空）物化为完整语义白名单；with_issue_codes 校验并透传
// 用户指定 codes。
func resolveReviseIssueCodes(r *config.CLIConfigReviseRound) ([]string, error) {
	switch r.SegmentScope {
	case "", "with_issues":
		return qa.SemanticQACodes(), nil
	case "with_issue_codes":
		if len(r.IssueCodes) == 0 {
			return nil, fmt.Errorf("revise.issue_codes must contain at least one code when segment_scope is \"with_issue_codes\"")
		}
		for _, code := range r.IssueCodes {
			if !qa.IsSemanticQACode(code) {
				return nil, fmt.Errorf("revise.issue_codes contains invalid code %q", code)
			}
		}
		return append([]string(nil), r.IssueCodes...), nil
	default:
		return nil, fmt.Errorf("revise.segment_scope must be \"with_issues\" or \"with_issue_codes\", got %q", r.SegmentScope)
	}
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
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(fj.InputPath)), ".")
	doc, parseErr := p.Parse(ctx, reader, format)
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

	// 跨轮增量载体（in-memory）：per-mode 已解决段索引集合。
	// 与 job_runner/preview/quick_translate 保持一致，使 CLI 行为与正式作业对齐。
	// translate 不参与（由 doc.Vars _translate_failed_indices 驱动增量）。
	resolvedByMode := engine.NewResolvedByMode()

	// 轮次循环
	for roundIdx := range eng.Rounds() {
		mode := eng.Rounds()[roundIdx].Handler.ModeName()

		if mode == pipeline.RoundModeTranslate {
			segmentIndexes := collectPendingOrFailed(doc, roundIdx)
			if len(segmentIndexes) == 0 {
				continue
			}
			if roundIdx > 0 {
				restoreFailedSegments(doc, segmentIndexes)
			}

			_, err := eng.ExecuteRound(ctx, roundIdx, doc, engine.WithSegmentFilter(segmentIndexes))
			if err != nil {
				return fmt.Errorf("cli: translate round %d: %w", roundIdx, err)
			}
			continue
		}

		// 非翻译轮（extract 等）：注入跨轮增量载体，排除上一同模式轮已解决的段。
		resolvedSet := resolvedByMode[mode]
		// 非翻译轮处理全部 doc 段（handler 的 BuildBatches 自行按 status/scope 过滤）。
		allIndexes := make([]int, len(doc.Segments))
		for i := range doc.Segments {
			allIndexes[i] = i
		}
		execOpts := []engine.ExecuteOption{
			engine.WithSegmentFilter(allIndexes),
			engine.WithResolvedIndices(resolvedSet),
		}
		result, err := eng.ExecuteRound(ctx, roundIdx, doc, execOpts...)
		if err != nil {
			return fmt.Errorf("cli: %s round %d: %w", mode, roundIdx, err)
		}
		// 累加本轮成功段到对应模式的 resolved 集合（跨轮增量）。
		engine.AccumulateResolved(resolvedByMode, mode, result.Resolved)
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
