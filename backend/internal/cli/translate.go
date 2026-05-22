package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

func newTranslateCmd(rt *appCtx) *cobra.Command {
	var (
		input        string
		output       string
		from         string
		to           string
		glossaryPath string
		bootstrap    bool
		noBootstrap  bool
	)
	cmd := &cobra.Command{
		Use:   "translate",
		Short: "翻译指定文件",
		Example: `  linguaflow translate -i README.md -o README_zh.md --to zh
  linguaflow translate -i docs.md -o out.md --from en --to ja -c linguaflow.yaml
  linguaflow translate -i docs.md -o out.md --to zh --glossary-path ./terms.csv --bootstrap`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if input == "" {
				return fmt.Errorf("--input/-i 必填")
			}
			if output == "" {
				return fmt.Errorf("--output/-o 必填")
			}
			if bootstrap && noBootstrap {
				return fmt.Errorf("--bootstrap 与 --no-bootstrap 互斥")
			}
			cfg, err := config.Load(rt.configPath)
			if err != nil {
				return err
			}
			applyTranslateFlags(cfg, glossaryPath,
				cmd.Flags().Changed("bootstrap"), bootstrap,
				cmd.Flags().Changed("no-bootstrap"), noBootstrap)

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
			return eng.Translate(cmd.Context(), engine.TranslateJob{
				InputPath:  input,
				OutputPath: output,
				SourceLang: from,
				TargetLang: to,
			})
		},
	}
	cmd.Flags().StringVarP(&input, "input", "i", "", "输入文件路径（必填）")
	cmd.Flags().StringVarP(&output, "output", "o", "", "输出文件路径（必填）")
	cmd.Flags().StringVar(&from, "from", "", "源语言（留空则用配置）")
	cmd.Flags().StringVar(&to, "to", "", "目标语言（留空则用配置）")
	cmd.Flags().StringVar(&glossaryPath, "glossary-path", "", "术语表 CSV 路径；指定后强制启用 glossary")
	cmd.Flags().BoolVar(&bootstrap, "bootstrap", false, "启用术语自举（覆盖配置；隐含启用 glossary）")
	cmd.Flags().BoolVar(&noBootstrap, "no-bootstrap", false, "禁用术语自举（覆盖配置）")
	return cmd
}

// applyTranslateFlags 把 CLI 覆盖应用到 cfg。
//
// glossary-path 非空：cfg.Glossary.Path 改写、Enabled 强制 true。
// bootstrap / no-bootstrap：三态——仅当 flag 被显式设置才覆盖；--bootstrap 还会
// 隐含 Glossary.Enabled=true，否则抽出的术语无处可去。
func applyTranslateFlags(cfg *config.Config, glossaryPath string,
	bootstrapSet, bootstrapVal bool,
	noBootstrapSet, noBootstrapVal bool,
) {
	if glossaryPath != "" {
		cfg.Glossary.Path = glossaryPath
		cfg.Glossary.Enabled = true
	}
	switch {
	case bootstrapSet && bootstrapVal:
		cfg.Glossary.Bootstrap.Enabled = true
		cfg.Glossary.Enabled = true
	case noBootstrapSet && noBootstrapVal:
		cfg.Glossary.Bootstrap.Enabled = false
	}
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
