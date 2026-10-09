package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/logging"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
	"github.com/MeowSalty/LinguaFlow/backend/internal/worker"
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
	revisionInput string
	changed       map[string]bool
}

func runTranslate(cmd *cobra.Command, rt *appCtx, opts translateOptions) error {
	if len(opts.inputs) == 0 {
		return errors.New("--input/-i is required")
	}
	if opts.output == "" {
		return errors.New("--output/-o is required")
	}
	in := config.CLIInputs{Environment: config.Environment()}
	if cmd.Flags().Changed("config") {
		in.ConfigPath = &rt.configPath
	}
	if opts.changed["from"] {
		in.SourceLang = &opts.from
	}
	if opts.changed["to"] {
		in.TargetLang = &opts.to
	}
	if cmd.Flags().Changed("log-level") {
		in.LogLevel = &rt.logLevel
	} else if cmd.Flags().Changed("verbose") && rt.verbose {
		level := "debug"
		in.LogLevel = &level
	}
	if cmd.Flags().Changed("log-format") {
		in.LogFormat = &rt.logFormat
	}
	cfg, err := config.ResolveCLIConfig(in)
	if err != nil {
		return err
	}
	if err := applyTranslateFlags(cfg, opts); err != nil {
		return err
	}
	rt.logger = logging.New(os.Stderr, cfg.Log.Level, cfg.Log.Format)
	slog.SetDefault(rt.logger)
	jobs, report, err := buildTranslateJobs(opts.inputs, opts.output)
	if err != nil {
		return err
	}
	revision, err := resolveCLIRevisionInput(cfg, opts, len(jobs))
	if err != nil {
		return err
	}
	reporter, err := newReporter(rt)
	if err != nil {
		return err
	}
	defer func() { _ = reporter.Close() }()
	eng, resolved, err := buildEngineFromCLIConfig(cmd.Context(), cfg, rt.logger, reporter)
	if err != nil {
		return err
	}
	defer resolved.Close()
	defer func() { _ = eng.Close() }()
	var failed []string
	for _, ignored := range report.Ignored {
		rt.logger.Info("ignored unsupported file", "path", ignored.Path, "reason", ignored.Reason)
	}
	for _, job := range jobs {
		rt.logger.Info("translation queued", "input", job.InputPath, "output", job.OutputPath)
		if err := translateSingleFile(cmd.Context(), eng, job, resolved.Spec.SourceLang, resolved.Spec.TargetLang, revision); err != nil {
			failed = append(failed, err.Error())
			rt.logger.Error("translation failed", "input", job.InputPath, "err", err)
		}
	}
	rt.logger.Info("batch translate summary", "succeeded", len(jobs)-len(failed), "failed", len(failed), "ignored", len(report.Ignored))
	if len(failed) > 0 {
		return fmt.Errorf("translation finished with %d failed files:\n%s", len(failed), strings.Join(failed, "\n"))
	}
	return nil
}

type cliExecution struct {
	Spec     *execution.ResolvedExecutionSpec
	Secrets  *credential.Memory
	Limiters *backend.LimiterPool
}

func (r *cliExecution) Close() { r.Limiters.Shutdown(); _ = r.Secrets.Close() }

func resolveCLIExecution(cfg *config.CLIConfig) (result *cliExecution, err error) {
	if err := config.ValidateCLIConfig(cfg); err != nil {
		return nil, err
	}
	profile, err := config.ResolveExecutionProfile(cfg)
	if err != nil {
		return nil, err
	}
	result = &cliExecution{Secrets: credential.NewMemory(), Limiters: backend.NewLimiterPool()}
	defer func() {
		if err != nil {
			result.Close()
		}
	}()
	backends := map[string]execution.BackendSnapshot{}
	policies := map[int]int{}
	resolveBackend := func(name string) (execution.BackendSnapshot, error) {
		if b, ok := backends[name]; ok {
			return b, nil
		}
		input, ok := cfg.Backends[name]
		if !ok {
			return execution.BackendSnapshot{}, errors.New("execution references an unknown backend")
		}
		if !input.Enabled {
			return execution.BackendSnapshot{}, fmt.Errorf("backend %s is disabled", name)
		}
		if input.Secret == "" {
			return execution.BackendSnapshot{}, fmt.Errorf("backend %s requires secret", name)
		}
		endpoint, _ := input.Options["base_url"].(string)
		binding, err := result.Secrets.Register(input.Type, endpoint, input.Secret)
		if err != nil {
			return execution.BackendSnapshot{}, fmt.Errorf("backend %s has invalid credentials or endpoint", name)
		}
		options := make(map[string]any, len(input.Options))
		for key, value := range input.Options {
			options[key] = value
		}
		b := execution.BackendSnapshot{ID: binding.ID, Scope: "cli", Name: name, Type: input.Type, Options: options, Credential: binding, RateLimitPerMinute: input.RateLimitPerMinute}
		backends[name] = b
		policies[b.ID] = input.RateLimitPerMinute
		return b, nil
	}
	snapshot := execution.JobExecutionSnapshot{
		SchemaVersion: execution.SnapshotSchemaVersion, DefaultsVersion: execution.SnapshotDefaultsVersion,
		RubyProtocolVersion: execution.RubyProtocolVersion, RubyValidatorVersion: execution.RubyValidatorVersion,
		ExecutionPlanName: "CLI translation",
		SourceLang:        cfg.SourceLang, TargetLang: cfg.TargetLang, GlossaryEnabled: cfg.Glossary.Enabled,
		Strategy: execution.StrategySnapshot{
			ProfileName: cfg.Execution.Profile, Protect: profile.Protect, Postprocess: profile.Postprocess, Repair: profile.Repair,
			Context: profile.Context, Ruby: profile.Ruby, QA: profile.QA,
		},
		RubyTemplates: execution.RubyTemplates{JSON: prompt.RubyAlignmentJSONTemplate, Text: prompt.RubyAlignmentTextTemplate},
	}
	for i, r := range cfg.Execution.Rounds {
		b, backendErr := resolveBackend(r.Backend)
		if backendErr != nil {
			return result, backendErr
		}
		round := execution.JobRoundSnapshot{Mode: r.Mode, Backend: b}
		switch r.Mode {
		case "translate":
			t := r.Translate
			content := templates.EmbeddedPromptTemplate()
			if t.Prompt != "" {
				p, ok := cfg.PromptTemplates[t.Prompt]
				if !ok || p.Content == "" {
					return result, fmt.Errorf("execution.rounds[%d] references an unavailable translation prompt", i)
				}
				content = p.Content
			}
			round.Translate = &execution.JobTranslateRoundSnapshot{Prompt: execution.PromptSnapshot{TemplateName: t.Prompt, Content: content}, BatchSize: t.BatchSize, MaxWordsPerBatch: t.MaxWordsPerBatch, Concurrency: t.Concurrency, FallbackShrink: t.FallbackShrink, SegmentFilter: &execution.SegmentFilterSnapshot{StatusFilter: "pending_only"}, Retry: execution.RetryConfig(t.Retry), InlineTermExtraction: t.InlineTermExtraction}
		case "extract":
			e := r.Extract
			content := templates.EmbeddedBootstrapTemplate()
			if e.Template != "" {
				p, ok := cfg.BootstrapPromptTemplates[e.Template]
				if !ok || p.Content == "" {
					return result, fmt.Errorf("execution.rounds[%d] references an unavailable extraction prompt", i)
				}
				content = p.Content
			}
			round.Extract = &execution.JobExtractRoundSnapshot{TemplateContent: content, BatchSize: e.BatchSize, MaxWordsPerBatch: e.MaxWordsPerBatch, Concurrency: e.Concurrency, MaxTermsPer1000Chars: e.MaxTermsPer1000Chars, MinSourceLen: e.MinSourceLen, Retry: execution.RetryConfig(e.Retry)}
		case "revise":
			v := r.Revise
			codes, codeErr := resolveReviseIssueCodes(v)
			if codeErr != nil {
				return result, codeErr
			}
			round.Revise = &execution.JobReviseRoundSnapshot{TemplateContent: templates.EmbeddedReviseTemplate(), BatchSize: v.BatchSize, MaxWordsPerBatch: v.MaxWordsPerBatch, Concurrency: v.Concurrency, SegmentScope: v.SegmentScope, IssueCodes: codes, Retry: execution.RetryConfig(v.Retry)}
		default:
			return result, errors.New("unsupported CLI execution mode")
		}
		snapshot.Rounds = append(snapshot.Rounds, round)
	}
	if ruby := cfg.Execution.RubyRetry; ruby != nil && ruby.Enabled {
		concurrency, concurrencyErr := execution.ResolveRubyRetryConcurrency(ruby.Concurrency)
		if concurrencyErr != nil {
			return result, concurrencyErr
		}
		b, backendErr := resolveBackend(ruby.Backend)
		if backendErr != nil {
			return result, backendErr
		}
		snapshot.RubyRetry = &execution.ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: b, MaxAttempts: ruby.MaxAttempts, Concurrency: concurrency}
	}
	result.Spec, err = execution.Resolve(snapshot)
	if err != nil {
		return result, err
	}
	result.Limiters.Initialize(policies)
	return result, nil
}

func buildEngineFromCLIConfig(ctx context.Context, cfg *config.CLIConfig, logger *slog.Logger, reporter progress.Reporter) (*engine.Engine, *cliExecution, error) {
	resolved, err := resolveCLIExecution(cfg)
	if err != nil {
		return nil, nil, err
	}
	engineConfig := worker.BuildEngineConfig(resolved.Spec)
	engineConfig.Glossary.Path = cfg.Glossary.Path
	engineConfig.Glossary.Save = cfg.Glossary.Save
	factory := worker.NewEngineFactoryWithCredentials(logger, resolved.Limiters, resolved.Secrets, resolved.Secrets)
	eng, err := factory.BuildEngineWithConfig(ctx, resolved.Spec, engineConfig, engine.RuntimeResources{}, reporter)
	if err != nil {
		resolved.Close()
		return nil, nil, err
	}
	return eng, resolved, nil
}

func resolveReviseIssueCodes(r *config.CLIConfigReviseRound) ([]string, error) {
	switch r.SegmentScope {
	case "", "with_issues":
		return qa.SemanticQACodes(), nil
	case "with_issue_codes":
		if len(r.IssueCodes) == 0 {
			return nil, errors.New("revise.issue_codes must not be empty")
		}
		for _, code := range r.IssueCodes {
			if !qa.IsSemanticQACode(code) {
				return nil, errors.New("revise.issue_codes contains an unsupported code")
			}
		}
		return append([]string(nil), r.IssueCodes...), nil
	default:
		return nil, errors.New("revise.segment_scope must be with_issues or with_issue_codes")
	}
}

// translateSingleFile 使用 TranslateRound 轮次循环翻译单个文件。
func translateSingleFile(ctx context.Context, eng *engine.Engine, fj FileJob, sourceLang, targetLang string, revision *config.CLIRevisionInput) error {
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
	// Parsers describe content; this CLI invocation selects every usable segment.
	for i := range doc.Segments {
		doc.Segments[i].Translate = !doc.Segments[i].Skip
	}
	if err := applyCLIRevisionInput(doc, eng, revision); err != nil {
		return err
	}

	// 跨轮增量载体（in-memory）：per-mode 已解决段索引集合。
	// 与 job_runner/preview/quick_translate 保持一致，使 CLI 行为与正式作业对齐。
	// translate 不参与（由 doc.Vars _translate_failed_indices 驱动增量）。
	resolvedByMode := engine.NewResolvedByMode()

	// Extraction may precede translation; count translation passes independently.
	translationPass := 0
	for roundIdx := range eng.Rounds() {
		mode := eng.Rounds()[roundIdx].Handler.ModeName()

		if mode == pipeline.RoundModeTranslate {
			segmentIndexes := collectPendingOrFailed(doc, translationPass)
			if len(segmentIndexes) == 0 {
				continue
			}
			if translationPass > 0 {
				restoreFailedSegments(doc, segmentIndexes)
			}

			translationPass++
			_, err := eng.ExecuteRound(ctx, roundIdx, doc, engine.WithSegmentFilter(segmentIndexes))
			if err != nil {
				return fmt.Errorf("cli: translate round %d: %w", roundIdx, err)
			}
			for _, index := range segmentIndexes {
				if doc.Segments[index].Target != "" {
					doc.Segments[index].Status = "translated"
				}
			}
			continue
		}
		if mode == pipeline.RoundModeRevise {
			if err := executeCLIRevisionRound(ctx, eng, roundIdx, doc); err != nil {
				return err
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
	if err := validateCLIRevisionComplete(doc, eng); err != nil {
		return err
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
	defer func() { _ = writer.Abort() }()

	if err := p.Render(ctx, doc, original, writer); err != nil {
		return fmt.Errorf("cli: render: %w", err)
	}

	eng.SaveGlossary(ctx)
	return writer.Close()
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
