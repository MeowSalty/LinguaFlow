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
		input  string
		output string
		from   string
		to     string
	)
	cmd := &cobra.Command{
		Use:   "translate",
		Short: "翻译指定文件",
		Example: `  linguaflow translate -i README.md -o README_zh.md --to zh
  linguaflow translate -i docs.md -o out.md --from en --to ja -c linguaflow.yaml`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if input == "" {
				return fmt.Errorf("--input/-i 必填")
			}
			if output == "" {
				return fmt.Errorf("--output/-o 必填")
			}
			cfg, err := config.Load(rt.configPath)
			if err != nil {
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
	return cmd
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
