package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

type serveOptions struct {
	host           string
	port           int
	dataDir        string
	autoMigrate    bool
	autoMigrateSet bool
}

func newServeCmd(rt *appCtx) *cobra.Command {
	opts := serveOptions{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动 LinguaFlow Web Service",
		Example: `  linguaflow serve
  linguaflow serve --host 127.0.0.1 --port 18080
  linguaflow serve -c ./linguaflow.yaml --data-dir ./data`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.autoMigrateSet = cmd.Flags().Changed("auto-migrate")
			return runServe(cmd.Context(), rt, opts)
		},
	}

	cmd.Flags().StringVar(&opts.host, "host", "", "覆盖 server.host")
	cmd.Flags().IntVar(&opts.port, "port", 0, "覆盖 server.port")
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "", "覆盖 server.data_dir")
	cmd.Flags().BoolVar(&opts.autoMigrate, "auto-migrate", true, "覆盖 server.auto_migrate")
	return cmd
}

func runServe(ctx context.Context, rt *appCtx, opts serveOptions) error {
	server, ln, cleanup, err := bootstrapServer(ctx, BootOptions{
		ConfigPath: rt.configPath,
		Logger:     rt.logger,
		Mode:       config.ModeServer,
		Overrides: func(cfg *config.Config) {
			if opts.host != "" {
				cfg.Server.Host = opts.host
			}
			if opts.port > 0 {
				cfg.Server.Port = opts.port
			}
			if opts.dataDir != "" {
				cfg.Server.DataDir = opts.dataDir
			}
			if opts.autoMigrateSet {
				cfg.Server.AutoMigrate = opts.autoMigrate
			}
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = cleanup() }()
	return server.Run(ctx, ln)
}
