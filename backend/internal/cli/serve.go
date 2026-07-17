package cli

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

type serveOptions struct {
	host           string
	port           int
	dataDir        string
	autoMigrate    bool
	autoMigrateSet bool
	jwtSecret      string
	corsOrigins    string
}

func newServeCmd(rt *appCtx) *cobra.Command {
	opts := serveOptions{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动 LinguaFlow Web Service",
		Long: `启动 LinguaFlow Web Service。

数据库通过环境变量配置：
  LINGUAFLOW_DATABASE_DRIVER                 sqlite（默认）或 postgres
  LINGUAFLOW_DATABASE_DSN                    PostgreSQL 必填；SQLite 为空时使用 data_dir/linguaflow.db
  LINGUAFLOW_DATABASE_MAX_OPEN_CONNS         最大打开连接数
  LINGUAFLOW_DATABASE_MAX_IDLE_CONNS         最大空闲连接数
  LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME      连接最大寿命（Go duration）`,
		Example: `  linguaflow serve
  linguaflow serve --host 127.0.0.1 --port 18080
  LINGUAFLOW_DATABASE_DRIVER=postgres LINGUAFLOW_DATABASE_DSN='postgres://localhost:5432/linguaflow?sslmode=disable' linguaflow serve`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.autoMigrateSet = cmd.Flags().Changed("auto-migrate")
			return runServe(cmd.Context(), rt, opts)
		},
	}

	cmd.Flags().StringVar(&opts.host, "host", "", "覆盖 server.host")
	cmd.Flags().IntVar(&opts.port, "port", 0, "覆盖 server.port")
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "", "覆盖 server.data_dir")
	cmd.Flags().BoolVar(&opts.autoMigrate, "auto-migrate", true, "覆盖 server.auto_migrate")
	cmd.Flags().StringVar(&opts.jwtSecret, "jwt-secret", "", "覆盖 LINGUAFLOW_JWT_SECRET")
	cmd.Flags().StringVar(&opts.corsOrigins, "cors-origins", "", "覆盖 LINGUAFLOW_CORS_ORIGINS（逗号分隔）")
	return cmd
}

func runServe(ctx context.Context, rt *appCtx, opts serveOptions) error {
	server, ln, cleanup, err := bootstrapServer(ctx, BootOptions{
		Logger: rt.logger,
		Mode:   config.ModeServer,
		Overrides: func(cfg *config.ServerConfig) {
			if opts.host != "" {
				cfg.Host = opts.host
			}
			if opts.port > 0 {
				cfg.Port = opts.port
			}
			if opts.dataDir != "" {
				cfg.DataDir = opts.dataDir
			}
			if opts.autoMigrateSet {
				cfg.AutoMigrate = opts.autoMigrate
			}
			if opts.jwtSecret != "" {
				cfg.JWTSecret = opts.jwtSecret
			}
			if opts.corsOrigins != "" {
				cfg.CORS.AllowedOrigins = strings.Split(opts.corsOrigins, ",")
			}
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = cleanup() }()
	return server.Run(ctx, ln)
}
