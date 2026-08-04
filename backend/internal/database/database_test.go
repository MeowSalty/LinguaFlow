package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestDriverSettings(t *testing.T) {
	tests := []struct {
		driver        string
		wantSQLDriver string
		wantDialect   string
	}{
		{config.DatabaseDriverSQLite, "sqlite", dialect.SQLite},
		{config.DatabaseDriverPostgres, "pgx", dialect.Postgres},
	}

	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			sqlDriver, entDialect, err := driverSettings(tt.driver)
			if err != nil {
				t.Fatalf("driver settings: %v", err)
			}
			if sqlDriver != tt.wantSQLDriver || entDialect != tt.wantDialect {
				t.Fatalf("settings=(%q, %q) want (%q, %q)", sqlDriver, entDialect, tt.wantSQLDriver, tt.wantDialect)
			}
		})
	}
}

func TestOpenSQLite(t *testing.T) {
	tests := []struct {
		name      string
		customDSN bool
	}{
		{name: "default DSN"},
		{name: "custom DSN", customDSN: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultServerConfig()
			cfg.DataDir = t.TempDir()
			if tt.customDSN {
				cfg.Database.DSN = filepath.Join(cfg.DataDir, "custom.db")
			}

			db, client, err := Open(context.Background(), cfg)
			if err != nil {
				t.Fatalf("open SQLite: %v", err)
			}
			var foreignKeys int
			if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
				t.Fatalf("read foreign_keys pragma: %v", err)
			}
			if foreignKeys != 1 {
				t.Fatalf("foreign_keys=%d want 1", foreignKeys)
			}
			if err := client.Close(); err != nil {
				t.Fatalf("close client: %v", err)
			}
			if err := db.Ping(); err == nil {
				t.Fatal("database should be closed with the ent client")
			}

			path := cfg.DatabasePath()
			if tt.customDSN {
				path = filepath.Join(cfg.DataDir, "custom.db")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("stat SQLite database: %v", err)
			}
		})
	}
}

func TestAcquireMigrationLockSQLiteIsNoop(t *testing.T) {
	unlock, err := AcquireMigrationLock(context.Background(), nil, config.DatabaseDriverSQLite)
	if err != nil {
		t.Fatalf("acquire SQLite migration lock: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("release SQLite migration lock: %v", err)
	}
}

func TestOpenAppliesPoolSettings(t *testing.T) {
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.MaxOpenConns = 3
	cfg.Database.MaxIdleConns = 1
	cfg.Database.ConnMaxLifetime = time.Millisecond

	db, client, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	defer func() { _ = client.Close() }()

	if got := db.Stats().MaxOpenConnections; got != 3 {
		t.Fatalf("max open connections=%d want 3", got)
	}

	conns := make([]interface{ Close() error }, 0, 3)
	for range 3 {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("get connection: %v", err)
		}
		if err := conn.PingContext(context.Background()); err != nil {
			t.Fatalf("ping connection: %v", err)
		}
		conns = append(conns, conn)
	}
	for _, conn := range conns {
		if err := conn.Close(); err != nil {
			t.Fatalf("close connection: %v", err)
		}
	}
	if got := db.Stats().Idle; got > 1 {
		t.Fatalf("idle connections=%d want at most 1", got)
	}

	time.Sleep(5 * time.Millisecond)
	if err := db.Ping(); err != nil {
		t.Fatalf("ping after max lifetime: %v", err)
	}
	if got := db.Stats().MaxLifetimeClosed; got == 0 {
		t.Fatal("expected an expired connection to be closed")
	}
}

func TestOpenRejectsUnknownDriver(t *testing.T) {
	cfg := config.DefaultServerConfig()
	cfg.Database.Driver = "mysql"

	_, _, err := Open(context.Background(), cfg)
	if err == nil || err.Error() != "database configure failed: unsupported driver" {
		t.Fatalf("error=%v", err)
	}
}

func TestOpenDoesNotExposeDSNInErrors(t *testing.T) {
	cfg := config.DefaultServerConfig()
	cfg.Database.Driver = config.DatabaseDriverPostgres
	cfg.Database.DSN = "postgres://secret-user:secret-password@%gh/linguaflow"

	_, _, err := Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected PostgreSQL open error")
	}
	if !strings.Contains(err.Error(), "invalid connection string") {
		t.Fatalf("error=%q does not identify invalid connection string", err)
	}
	for _, secret := range []string{"secret-user", "secret-password", cfg.Database.DSN} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("error exposes connection details")
		}
	}
}
