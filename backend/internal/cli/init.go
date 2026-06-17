package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

func newInitCmd() *cobra.Command {
	var (
		path  string
		force bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "在当前目录生成 linguaflow.yaml",
		RunE: func(_ *cobra.Command, _ []string) error {
			if path == "" {
				path = "linguaflow.yaml"
			}
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s 已存在；使用 --force 覆盖", path)
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}

			// 1. 写入主配置文件（带注释，含 file 引用）
			if err := os.WriteFile(path, templates.DefaultConfigYAML(), 0o644); err != nil {
				return fmt.Errorf("写入失败：%w", err)
			}
			fmt.Printf("已写入 %s\n", path)

			// 2. 写入提示词模板
			promptDir := filepath.Join(filepath.Dir(path), "prompts")
			if err := os.MkdirAll(promptDir, 0o755); err != nil {
				return fmt.Errorf("创建 prompts 目录失败：%w", err)
			}
			promptPath := filepath.Join(promptDir, "default.tmpl")
			if err := os.WriteFile(promptPath, []byte(templates.EmbeddedPromptTemplate()), 0o644); err != nil {
				return fmt.Errorf("写入提示词模板失败：%w", err)
			}
			fmt.Printf("已写入 %s\n", promptPath)

			// 3. 写入翻译策略
			profileDir := filepath.Join(filepath.Dir(path), "profiles")
			if err := os.MkdirAll(profileDir, 0o755); err != nil {
				return fmt.Errorf("创建 profiles 目录失败：%w", err)
			}
			profilePath := filepath.Join(profileDir, "default.yaml")
			if err := os.WriteFile(profilePath, templates.EmbeddedProfileConfig(), 0o644); err != nil {
				return fmt.Errorf("写入翻译策略失败：%w", err)
			}
			fmt.Printf("已写入 %s\n", profilePath)

			return nil
		},
	}
	cmd.Flags().StringVarP(&path, "path", "p", "linguaflow.yaml", "目标配置文件路径")
	cmd.Flags().BoolVar(&force, "force", false, "如果文件已存在则覆盖")
	return cmd
}
