package cli

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/api"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
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
	cfg, err := config.Load(rt.configPath)
	if err != nil {
		return err
	}

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

	if err := os.MkdirAll(cfg.Server.DataDir, 0o755); err != nil {
		return fmt.Errorf("create server data dir %s: %w", cfg.Server.DataDir, err)
	}

	dbPath := cfg.Server.DatabasePath()
	dbDSN := cfg.Server.DatabaseDSN()
	db, err := sql.Open("sqlite", dbDSN)
	if err != nil {
		return fmt.Errorf("open sqlite database %s: %w", dbPath, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("ping sqlite database %s: %w", dbPath, err)
	}

	driver := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(driver))
	defer func() { _ = client.Close() }()

	if cfg.Server.AutoMigrate {
		if err := client.Schema.Create(ctx); err != nil {
			return fmt.Errorf("run ent schema migration: %w", err)
		}
	}

	rt.logger.Info("web service initialized",
		"addr", cfg.Server.Address(),
		"database_path", dbPath,
		"auto_migrate", cfg.Server.AutoMigrate)

	server, err := api.NewServer(cfg, rt.logger, db, client)
	if err != nil {
		return err
	}
	return server.Run(ctx)
}
