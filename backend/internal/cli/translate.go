package cli

import (
	"github.com/spf13/cobra"
)

func newTranslateCmd(rt *appCtx) *cobra.Command {
	var (
		inputs        []string
		output        string
		from          string
		to            string
		glossaryPath  string
		bootstrapMode string
		profile       string
		prompt        string
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
			return runTranslate(cmd, rt, translateOptions{
				inputs:        inputs,
				output:        output,
				from:          from,
				to:            to,
				glossaryPath:  glossaryPath,
				bootstrapMode: bootstrapMode,
				profile:       profile,
				prompt:        prompt,
			})
		},
	}
	cmd.Flags().StringSliceVarP(&inputs, "input", "i", nil, "输入文件或目录路径；可传多个")
	cmd.Flags().StringVarP(&output, "output", "o", "", "单文件输入时为输出文件；多文件或目录输入时必须为输出目录")
	cmd.Flags().StringVar(&from, "from", "", "源语言（留空则用配置）")
	cmd.Flags().StringVar(&to, "to", "", "目标语言（留空则用配置）")
	cmd.Flags().StringVar(&glossaryPath, "glossary-path", "", "术语表 CSV 路径；指定后强制启用 glossary")
	cmd.Flags().StringVar(&bootstrapMode, "bootstrap", "", "术语自举模式 off|pre|inline；留空沿用配置（非 off 隐含启用 glossary）")
	cmd.Flags().StringVar(&profile, "profile", "", "翻译策略名称（引用 translation_profiles 中的 key）")
	cmd.Flags().StringVar(&prompt, "prompt", "", "提示词模板名称（引用 prompt_templates 中的 key）")
	return cmd
}
