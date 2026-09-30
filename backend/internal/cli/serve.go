package cli

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/spf13/cobra"
)

func newServeCmd(rt *appCtx) *cobra.Command {
	cmd := &cobra.Command{
		Use: "serve", Short: "启动 LinguaFlow Web Service", Args: cobra.NoArgs,
		Long: "通过 kind: server, version: 1 部署文档、环境变量和显式 flags 启动服务。\n文件仅由 --config 或 LINGUAFLOW_SERVER_CONFIG 选择；不会自动读取 .env。\n必须配置 JWT secret 和 credentials.keyring_file；首次实例需要 bootstrap.admin。\n使用 config explain --mode serve 查看最终脱敏配置和来源。",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := resolveDeployment(cmd, rt, config.ModeServer)
			if err != nil {
				return err
			}
			return runServe(cmd.Context(), resolved)
		},
	}
	addDeploymentFlags(cmd, false)
	return cmd
}
func runServe(ctx context.Context, resolved *config.ResolvedServer) error {
	server, ln, cleanup, err := bootstrapServer(ctx, BootOptions{Resolved: resolved})
	if err != nil {
		return err
	}
	defer func() { _ = cleanup() }()
	return server.Run(ctx, ln)
}
