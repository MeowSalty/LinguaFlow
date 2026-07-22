package cli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestBootstrapServer_LocalIgnoresPostgresEnvironment(t *testing.T) {
	t.Setenv("LINGUAFLOW_DATABASE_DRIVER", config.DatabaseDriverPostgres)
	t.Setenv("LINGUAFLOW_DATABASE_DSN", "postgres://localhost/linguaflow")
	t.Setenv("LINGUAFLOW_ADMIN_USERNAME", "")
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, listener, cleanup, err := bootstrapServer(context.Background(), BootOptions{
		Logger: logger,
		Mode:   config.ModeLocal,
		Overrides: func(cfg *config.ServerConfig) {
			cfg.Host = "127.0.0.1"
			cfg.Port = 0
			cfg.DataDir = dataDir
		},
	})
	if err != nil {
		t.Fatalf("bootstrap local server: %v", err)
	}
	defer func() { _ = cleanup() }()
	defer func() { _ = listener.Close() }()

	if _, err := os.Stat(filepath.Join(dataDir, "linguaflow.db")); err != nil {
		t.Fatalf("stat local SQLite database: %v", err)
	}
}

func TestBootstrapServer_ServeUIDefault(t *testing.T) {
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var gotServeUI bool
	server, listener, cleanup, err := bootstrapServer(context.Background(), BootOptions{
		Logger: logger,
		Mode:   config.ModeServer,
		Overrides: func(cfg *config.ServerConfig) {
			cfg.Host = "127.0.0.1"
			cfg.Port = 0
			cfg.DataDir = dataDir
			gotServeUI = cfg.ServeUI
		},
	})
	if err != nil {
		t.Fatalf("bootstrap serve server: %v", err)
	}
	defer func() { _ = cleanup() }()
	defer func() { _ = listener.Close() }()

	if !gotServeUI {
		t.Fatal("ServeUI should default to true in serve mode")
	}
	if server == nil {
		t.Fatal("server should not be nil")
	}
}
