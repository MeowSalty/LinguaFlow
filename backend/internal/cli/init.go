package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var path, kind string
	var force bool
	cmd := &cobra.Command{Use: "init", Short: "生成配置模板及完整引用文件；不初始化数据库", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if kind != "translation" && kind != "server" {
			return errors.New("kind must be translation or server")
		}
		if !cmd.Flags().Changed("path") {
			path = "linguaflow.yaml"
			if kind == "server" {
				path = "server.yaml"
			}
		}
		if path == "" {
			return errors.New("configuration output path must not be empty")
		}
		type output struct {
			path string
			data []byte
		}
		var files []output
		if kind == "server" {
			files = append(files, output{path, templates.DefaultServerConfigYAML()})
		} else {
			base := filepath.Dir(path)
			files = append(files,
				output{filepath.Join(base, "prompts", "default_translation.tmpl"), []byte(templates.EmbeddedPromptTemplate())},
				output{filepath.Join(base, "prompts", "default_bootstrap.tmpl"), []byte(templates.EmbeddedBootstrapTemplate())},
				output{filepath.Join(base, "profiles", "default.yaml"), templates.EmbeddedProfileConfig()},
				output{path, templates.DefaultConfigYAML()},
			)
		}
		for _, file := range files {
			info, err := os.Stat(file.path)
			if err == nil {
				if info.IsDir() {
					return fmt.Errorf("%s is a directory", file.path)
				}
				if !force {
					return fmt.Errorf("%s already exists; use --force to overwrite", file.path)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		for _, file := range files {
			if err := os.MkdirAll(filepath.Dir(file.path), 0o755); err != nil {
				return fmt.Errorf("create template directory: %w", err)
			}
			if err := os.WriteFile(file.path, file.data, 0o644); err != nil {
				return fmt.Errorf("write template: %w", err)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", file.path); err != nil {
				return err
			}
		}
		return nil
	}}
	cmd.Flags().StringVarP(&path, "path", "p", "", "目标配置文件路径（默认 linguaflow.yaml 或 server.yaml）")
	cmd.Flags().StringVar(&kind, "kind", "translation", "配置文档 translation 或 server")
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已有模板及引用文件")
	return cmd
}
