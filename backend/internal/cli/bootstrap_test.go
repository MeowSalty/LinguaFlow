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
