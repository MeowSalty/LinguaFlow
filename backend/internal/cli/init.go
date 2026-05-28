package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func newInitCmd() *cobra.Command {
	var (
		path  string
		force bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "在当前目录生成带注释的 linguaflow.yaml",
		RunE: func(_ *cobra.Command, _ []string) error {
			if path == "" {
				path = "linguaflow.yaml"
			}
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s 已存在；使用 --force 覆盖", path)
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.WriteFile(path, config.DefaultYAML, 0o644); err != nil {
				return fmt.Errorf("写入失败：%w", err)
			}
			fmt.Printf("已写入 %s\n", path)
			return nil
		},
	}
	cmd.Flags().StringVarP(&path, "path", "p", "linguaflow.yaml", "目标配置文件路径")
	cmd.Flags().BoolVar(&force, "force", false, "如果文件已存在则覆盖")
	return cmd
}
