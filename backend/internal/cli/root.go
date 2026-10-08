// Package cli 装配 cobra 命令树。main.go 调用 Execute() 启动。
package cli

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/inconshreveable/mousetrap"
	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/logging"
)

// appCtx 持有全局可变项：日志、配置路径，由子命令读取。
type appCtx struct {
	configPath   string
	logLevel     string
	logFormat    string
	verbose      bool
	progressMode string // auto | bar | log | none
	logger       *slog.Logger
}

func newRoot() (*cobra.Command, *appCtx) {
	rt := &appCtx{}
	root := &cobra.Command{
		Use:   "linguaflow",
		Short: "LinguaFlow: 面向开发者和内容创作者的 AI 辅助翻译引擎",
		Long: `LinguaFlow 是一个 Go 实现的、可编程的 AI 翻译流水线。
开箱即用：一行命令翻译 Markdown / 字幕 / 结构化数据。
高度可定制：提示词模板、术语表、上下文注入、Lua 脚本扩展。`,
		SilenceUsage: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			if cmd.Parent() == nil && mousetrap.StartedByExplorer() {
				return
			}
			// Deployment commands construct their logger after all sources resolve.
			for current := cmd; current != nil; current = current.Parent() {
				if current.Name() == "serve" || current.Name() == "local" || current.Name() == "config" || current.Name() == "admin" || current.Name() == "translate" || current.Name() == "secrets" {
					return
				}
			}
			lvl := rt.logLevel
			if rt.verbose && !cmd.Flags().Changed("log-level") {
				lvl = "debug"
			}
			rt.logger = logging.New(os.Stderr, lvl, rt.logFormat)
			slog.SetDefault(rt.logger)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if mousetrap.StartedByExplorer() {
				resolved, err := resolveDeployment(cmd, rt, config.ModeLocal)
				if err != nil {
					return err
				}
				return runLocal(cmd.Context(), resolved, false)
			}
			return cmd.Help()
		},
	}
	root.PersistentFlags().StringVarP(&rt.configPath, "config", "c", "", "配置文件路径（默认仅用内置默认值）")
	root.PersistentFlags().StringVar(&rt.logLevel, "log-level", "info", "日志级别 debug|info|warn|error")
	root.PersistentFlags().StringVar(&rt.logFormat, "log-format", "text", "日志格式 text|json")
	root.PersistentFlags().BoolVarP(&rt.verbose, "verbose", "v", false, "等同于 --log-level=debug")
	root.PersistentFlags().StringVar(&rt.progressMode, "progress", "auto", "进度反馈 auto|bar|log|none")

	root.AddCommand(newServeCmd(rt))
	root.AddCommand(newLocalCmd(rt))
	root.AddCommand(newTranslateCmd(rt))
	root.AddCommand(newInitCmd())
	root.AddCommand(newConfigCmd(rt))
	root.AddCommand(newAdminCmd(rt))
	root.AddCommand(newSecretsCmd())
	root.AddCommand(newVersionCmd())
	return root, rt
}

// Execute 是 main.go 的唯一入口。
func Execute() int {
	// 禁用 cobra 的 mousetrap 拦截，允许双击启动时正常执行。
	cobra.MousetrapHelpText = ""

	root, _ := newRoot()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		// cobra 已打印用户友好错误；这里用 slog 再记一条便于排查
		slog.Error("command failed", "err", err)
		return 1
	}
	return 0
}
