package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
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
			eng, err := engine.New(cfg, rt.logger)
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
