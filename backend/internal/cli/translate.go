package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

func newTranslateCmd(rt *appCtx) *cobra.Command {
	var (
		inputs        []string
		output        string
		from          string
		to            string
		glossaryPath  string
		bootstrapMode string
	)
	cmd := &cobra.Command{
		Use:   "translate",
		Short: "翻译一个或多个文件/目录",
		Example: `  linguaflow translate -i README.md -o README_zh.md --to zh
  linguaflow translate -i docs.md -o out.md --from en --to ja -c linguaflow.yaml
	linguaflow translate -i docs.md notes.txt -o ./out --to zh
	linguaflow translate -i ./docs ./subtitles -o ./translated --to zh
  linguaflow translate -i docs.md -o out.md --to zh --glossary-path ./terms.csv --bootstrap=inline`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(inputs) == 0 {
				return fmt.Errorf("--input/-i 必填")
			}
			if output == "" {
				return fmt.Errorf("--output/-o 必填")
			}

			jobs, report, err := buildTranslateJobs(inputs, output)
			if err != nil {
				return err
			}
			cfg, err := config.Load(rt.configPath)
			if err != nil {
				return err
			}
			if err := applyTranslateFlags(cfg, glossaryPath, bootstrapMode); err != nil {
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
				job.SourceLang = from
				job.TargetLang = to
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
		},
	}
	cmd.Flags().StringSliceVarP(&inputs, "input", "i", nil, "输入文件或目录路径；可传多个")
	cmd.Flags().StringVarP(&output, "output", "o", "", "单文件输入时为输出文件；多文件或目录输入时必须为输出目录")
	cmd.Flags().StringVar(&from, "from", "", "源语言（留空则用配置）")
	cmd.Flags().StringVar(&to, "to", "", "目标语言（留空则用配置）")
	cmd.Flags().StringVar(&glossaryPath, "glossary-path", "", "术语表 CSV 路径；指定后强制启用 glossary")
	cmd.Flags().StringVar(&bootstrapMode, "bootstrap", "", "术语自举模式 off|pre|inline；留空沿用配置（非 off 隐含启用 glossary）")
	return cmd
}

type batchPlan struct {
	Ignored []ignoredInput
}

type ignoredInput struct {
	Path   string
	Reason string
}

func buildTranslateJobs(inputs []string, output string) ([]engine.TranslateJob, batchPlan, error) {
	entries, report, err := collectInputEntries(inputs)
	if err != nil {
		return nil, batchPlan{}, err
	}
	if len(entries) == 0 {
		return nil, report, fmt.Errorf("没有可翻译的输入文件")
	}

	singleDirectFile := len(inputs) == 1 && len(entries) == 1 && entries[0].root == ""
	if singleDirectFile {
		if stat, err := os.Stat(output); err == nil && stat.IsDir() {
			return nil, report, fmt.Errorf("单文件输入时 --output/-o 必须是输出文件路径，当前为目录: %s", output)
		}
		return []engine.TranslateJob{{InputPath: entries[0].path, OutputPath: output}}, report, nil
	}

	if err := os.MkdirAll(output, 0o755); err != nil {
		return nil, report, fmt.Errorf("创建输出目录失败 %s: %w", output, err)
	}
	stat, err := os.Stat(output)
	if err != nil {
		return nil, report, fmt.Errorf("读取输出路径失败 %s: %w", output, err)
	}
	if !stat.IsDir() {
		return nil, report, fmt.Errorf("多文件或目录输入时 --output/-o 必须是输出目录: %s", output)
	}

	jobs := make([]engine.TranslateJob, 0, len(entries))
	for _, entry := range entries {
		rel := entry.outputRelativePath()
		jobs = append(jobs, engine.TranslateJob{
			InputPath:  entry.path,
			OutputPath: filepath.Join(output, rel),
		})
	}
	return jobs, report, nil
}

type inputEntry struct {
	path string
	root string
	rel  string
}

func (e inputEntry) outputRelativePath() string {
	if e.root == "" {
		return filepath.Base(e.path)
	}
	return e.rel
}

func collectInputEntries(inputs []string) ([]inputEntry, batchPlan, error) {
	var (
		entries []inputEntry
		report  batchPlan
	)
	seen := map[string]struct{}{}
	for _, raw := range inputs {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		cleaned := filepath.Clean(raw)
		info, err := os.Stat(cleaned)
		if err != nil {
			return nil, report, fmt.Errorf("读取输入路径失败 %s: %w", cleaned, err)
		}
		if info.IsDir() {
			dirEntries, ignored, err := collectDirectoryEntries(cleaned)
			if err != nil {
				return nil, report, err
			}
			report.Ignored = append(report.Ignored, ignored...)
			for _, entry := range dirEntries {
				if _, ok := seen[entry.path]; ok {
					continue
				}
				seen[entry.path] = struct{}{}
				entries = append(entries, entry)
			}
			continue
		}
		if err := ensureSupportedFile(cleaned); err != nil {
			if errors.Is(err, parser.ErrNoParser) {
				report.Ignored = append(report.Ignored, ignoredInput{Path: cleaned, Reason: err.Error()})
				continue
			}
			return nil, report, err
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		entries = append(entries, inputEntry{path: cleaned})
	}
	return entries, report, nil
}

func collectDirectoryEntries(root string) ([]inputEntry, []ignoredInput, error) {
	var entries []inputEntry
	var ignored []ignoredInput
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if err := ensureSupportedFile(path); err != nil {
			if errors.Is(err, parser.ErrNoParser) {
				ignored = append(ignored, ignoredInput{Path: path, Reason: err.Error()})
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("计算相对路径失败 %s: %w", path, err)
		}
		entries = append(entries, inputEntry{path: path, root: root, rel: rel})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("扫描目录失败 %s: %w", root, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	sort.Slice(ignored, func(i, j int) bool { return ignored[i].Path < ignored[j].Path })
	return entries, ignored, nil
}

func ensureSupportedFile(path string) error {
	_, err := parser.DetectByExt(path)
	return err
}

// applyTranslateFlags 把 CLI 覆盖应用到 cfg。
//
// glossary-path 非空：cfg.Glossary.Path 改写、Enabled 强制 true。
// bootstrap 非空：校验取值，覆盖 cfg.Glossary.Bootstrap.Mode；
// 非 "off" 时一并把 Glossary.Enabled 设为 true（与 config.Validate 一致）。
func applyTranslateFlags(cfg *config.Config, glossaryPath, bootstrapMode string) error {
	if glossaryPath != "" {
		cfg.Glossary.Path = glossaryPath
		cfg.Glossary.Enabled = true
	}
	if bootstrapMode != "" {
		switch bootstrapMode {
		case config.BootstrapModeOff, config.BootstrapModePre, config.BootstrapModeInline:
		default:
			return fmt.Errorf("--bootstrap must be one of off|pre|inline, got %q", bootstrapMode)
		}
		cfg.Glossary.Bootstrap.Mode = bootstrapMode
		if bootstrapMode != config.BootstrapModeOff {
			cfg.Glossary.Enabled = true
		}
	}
	return nil
}

// newReporter 根据 --progress 标志与 stderr 是否 TTY 选择 Reporter。
//   - auto: TTY → bar，非 TTY → log
//   - bar:  强制进度条
//   - log:  强制周期日志（每 5s 或每 10 段，取先到）
//   - none: 静默（仅保留既有 slog 输出）
func newReporter(rt *appCtx) (progress.Reporter, error) {
	mode := rt.progressMode
	if mode == "" {
		mode = "auto"
	}
	if mode == "auto" {
		if term.IsTerminal(int(os.Stderr.Fd())) {
			mode = "bar"
		} else {
			mode = "log"
		}
	}
	switch mode {
	case "bar":
		return progress.NewTerminal(os.Stderr), nil
	case "log":
		return progress.NewLog(rt.logger, 5*time.Second, 10), nil
	case "none":
		return progress.Nop{}, nil
	default:
		return nil, fmt.Errorf("unknown --progress mode %q (want auto|bar|log|none)", mode)
	}
}
