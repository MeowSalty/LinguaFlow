package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
)

type translateOptions struct {
	inputs        []string
	output        string
	from          string
	to            string
	glossaryPath  string
	bootstrapMode string
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
	cfg, err := config.Load(rt.configPath)
	if err != nil {
		return err
	}
	if err := applyTranslateFlags(cfg, opts.glossaryPath, opts.bootstrapMode); err != nil {
		return err
	}

	reporter, err := newReporter(rt)
	if err != nil {
		return err
	}
	defer func() { _ = reporter.Close() }()

	eng, err := engine.New(cfg, rt.logger, reporter)
	if err != nil {
		return err
	}
	defer func() { _ = eng.Close() }()

	var failed []string
	for _, ignored := range report.Ignored {
		rt.logger.Info("ignored unsupported file", "path", ignored.Path, "reason", ignored.Reason)
	}
	for _, job := range jobs {
		rt.logger.Info("translation queued", "input", job.InputPath, "output", job.OutputPath)
		job.SourceLang = opts.from
		job.TargetLang = opts.to
		if err := eng.Translate(cmd.Context(), job); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", job.InputPath, err))
			rt.logger.Error("translation failed", "input", job.InputPath, "output", job.OutputPath, "err", err)
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
