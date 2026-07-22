package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

var databaseEnvNames = []string{
	"LINGUAFLOW_DATABASE_DRIVER",
	"LINGUAFLOW_DATABASE_DSN",
	"LINGUAFLOW_DATABASE_MAX_OPEN_CONNS",
	"LINGUAFLOW_DATABASE_MAX_IDLE_CONNS",
	"LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME",
}

func clearDatabaseEnv(t *testing.T) {
	t.Helper()
	for _, name := range databaseEnvNames {
		value, ok := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		name := name
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}

func TestValidateServerConfig_Defaults(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.Host = ""
	cfg.Port = 0
	cfg.DataDir = ""
	cfg.JWTSecret = ""
	cfg.JWTIssuer = ""
	cfg.JWTExpiry = 0
	cfg.RefreshExpiry = 0
	cfg.ShutdownTimeout = 0
	cfg.CORS.AllowedOrigins = nil

	if err := ValidateServerConfig(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.Host != "0.0.0.0" {
		t.Fatalf("host=%q want 0.0.0.0", cfg.Host)
	}
	if cfg.Port != 8080 {
		t.Fatalf("port=%d want 8080", cfg.Port)
	}
	if cfg.DataDir != "./data" {
		t.Fatalf("data_dir=%q want ./data", cfg.DataDir)
	}
	if cfg.JWTSecret == "" {
		t.Fatal("jwt_secret should be defaulted")
	}
	if cfg.JWTIssuer != "linguaflow" {
		t.Fatalf("jwt_issuer=%q want linguaflow", cfg.JWTIssuer)
	}
	if cfg.JWTExpiry != 15*time.Minute {
		t.Fatalf("jwt_expiry=%v want 15m", cfg.JWTExpiry)
	}
	if cfg.RefreshExpiry != 30*24*time.Hour {
		t.Fatalf("refresh_token_expiry=%v want 720h", cfg.RefreshExpiry)
	}
	if cfg.ShutdownTimeout <= 0 {
		t.Fatalf("shutdown_timeout=%v want > 0", cfg.ShutdownTimeout)
	}
	if len(cfg.CORS.AllowedOrigins) != 1 || cfg.CORS.AllowedOrigins[0] != "*" {
		t.Fatalf("allowed_origins=%v want [*]", cfg.CORS.AllowedOrigins)
	}
}

func TestDefaultServerConfig_ServeUI(t *testing.T) {
	cfg := DefaultServerConfig()
	if !cfg.ServeUI {
		t.Fatal("ServeUI should default to true")
	}
}

func TestLoadServerConfig_ServeUIEnv(t *testing.T) {
	clearDatabaseEnv(t)

	t.Run("false", func(t *testing.T) {
		t.Setenv("LINGUAFLOW_SERVE_UI", "false")
		cfg, err := LoadServerConfig(ModeServer)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.ServeUI {
			t.Fatal("ServeUI should be false when LINGUAFLOW_SERVE_UI=false")
		}
	})

	for _, value := range []string{"true", "1", "yes"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("LINGUAFLOW_SERVE_UI", value)
			cfg, err := LoadServerConfig(ModeServer)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if !cfg.ServeUI {
				t.Fatalf("ServeUI should be true when LINGUAFLOW_SERVE_UI=%q", value)
			}
		})
	}

	t.Run("empty keeps default", func(t *testing.T) {
		t.Setenv("LINGUAFLOW_SERVE_UI", "")
		cfg, err := LoadServerConfig(ModeServer)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if !cfg.ServeUI {
			t.Fatal("ServeUI should remain true when LINGUAFLOW_SERVE_UI is empty")
		}
	})
}

func TestValidateServerConfig_InvalidMode(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.Mode = "invalid"
	err := ValidateServerConfig(cfg)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !strings.Contains(err.Error(), "server.mode must be one of") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateServerConfig_LocalMode(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.Mode = ModeLocal
	cfg.Port = 0
	if err := ValidateServerConfig(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != ModeLocal {
		t.Fatalf("mode=%q want %q", cfg.Mode, ModeLocal)
	}
	if cfg.Port != 0 {
		t.Fatalf("port=%d want 0", cfg.Port)
	}
}

func TestDatabaseDSN_CustomSQLiteForcesForeignKeys(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.Database.DSN = "custom.db?_pragma=foreign_keys(0)"

	got := cfg.DatabaseDSN()
	if !strings.HasSuffix(got, "&_pragma=foreign_keys(1)") {
		t.Fatalf("database DSN=%q does not force foreign keys", got)
	}
}

func TestLoadServerConfig_DefaultSQLite(t *testing.T) {
	clearDatabaseEnv(t)

	cfg, err := LoadServerConfig(ModeServer)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	want := DatabaseConfig{Driver: DatabaseDriverSQLite, MaxIdleConns: 2}
	if !reflect.DeepEqual(cfg.Database, want) {
		t.Fatalf("database=%+v want %+v", cfg.Database, want)
	}
	if !strings.Contains(cfg.DatabaseDSN(), "linguaflow.db?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)") {
		t.Fatalf("unexpected SQLite DSN: %q", cfg.DatabaseDSN())
	}
}

func TestLoadServerConfig_Postgres(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("LINGUAFLOW_DATABASE_DRIVER", DatabaseDriverPostgres)
	t.Setenv("LINGUAFLOW_DATABASE_DSN", "postgres://localhost/linguaflow")
	t.Setenv("LINGUAFLOW_DATABASE_MAX_OPEN_CONNS", "40")
	t.Setenv("LINGUAFLOW_DATABASE_MAX_IDLE_CONNS", "8")
	t.Setenv("LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME", "45m")

	cfg, err := LoadServerConfig(ModeServer)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	want := DatabaseConfig{
		Driver:          DatabaseDriverPostgres,
		DSN:             "postgres://localhost/linguaflow",
		MaxOpenConns:    40,
		MaxIdleConns:    8,
		ConnMaxLifetime: 45 * time.Minute,
	}
	if !reflect.DeepEqual(cfg.Database, want) {
		t.Fatalf("database=%+v want %+v", cfg.Database, want)
	}
}

func TestLoadServerConfig_PostgresDefaults(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("LINGUAFLOW_DATABASE_DRIVER", DatabaseDriverPostgres)
	t.Setenv("LINGUAFLOW_DATABASE_DSN", "postgres://localhost/linguaflow")

	cfg, err := LoadServerConfig(ModeServer)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	want := DatabaseConfig{
		Driver:          DatabaseDriverPostgres,
		DSN:             "postgres://localhost/linguaflow",
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 30 * time.Minute,
	}
	if !reflect.DeepEqual(cfg.Database, want) {
		t.Fatalf("database=%+v want %+v", cfg.Database, want)
	}
}

func TestLoadServerConfig_LocalIgnoresDatabaseEnv(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("LINGUAFLOW_DATABASE_DRIVER", DatabaseDriverPostgres)
	t.Setenv("LINGUAFLOW_DATABASE_DSN", "postgres://localhost/linguaflow")
	t.Setenv("LINGUAFLOW_DATABASE_MAX_OPEN_CONNS", "invalid")

	cfg, err := LoadServerConfig(ModeLocal)
	if err != nil {
		t.Fatalf("load local config: %v", err)
	}
	want := DatabaseConfig{Driver: DatabaseDriverSQLite, MaxIdleConns: 2}
	if !reflect.DeepEqual(cfg.Database, want) {
		t.Fatalf("database=%+v want %+v", cfg.Database, want)
	}
}

func TestLoadServerConfig_DatabaseErrors(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantError string
	}{
		{
			name:      "postgres DSN missing",
			env:       map[string]string{"LINGUAFLOW_DATABASE_DRIVER": DatabaseDriverPostgres},
			wantError: "dsn is required",
		},
		{
			name: "postgres DSN whitespace",
			env: map[string]string{
				"LINGUAFLOW_DATABASE_DRIVER": DatabaseDriverPostgres,
				"LINGUAFLOW_DATABASE_DSN":    "   ",
			},
			wantError: "dsn is required",
		},
		{
			name:      "unknown driver",
			env:       map[string]string{"LINGUAFLOW_DATABASE_DRIVER": "mysql"},
			wantError: "LINGUAFLOW_DATABASE_DRIVER",
		},
		{
			name:      "invalid max open",
			env:       map[string]string{"LINGUAFLOW_DATABASE_MAX_OPEN_CONNS": "many"},
			wantError: "LINGUAFLOW_DATABASE_MAX_OPEN_CONNS",
		},
		{
			name:      "invalid duration",
			env:       map[string]string{"LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME": "later"},
			wantError: "LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME",
		},
		{
			name:      "negative max open",
			env:       map[string]string{"LINGUAFLOW_DATABASE_MAX_OPEN_CONNS": "-1"},
			wantError: "max_open_conns must not be negative",
		},
		{
			name:      "negative max idle",
			env:       map[string]string{"LINGUAFLOW_DATABASE_MAX_IDLE_CONNS": "-1"},
			wantError: "max_idle_conns must not be negative",
		},
		{
			name:      "negative lifetime",
			env:       map[string]string{"LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME": "-1s"},
			wantError: "conn_max_lifetime must not be negative",
		},
		{
			name: "idle exceeds open",
			env: map[string]string{
				"LINGUAFLOW_DATABASE_MAX_OPEN_CONNS": "2",
				"LINGUAFLOW_DATABASE_MAX_IDLE_CONNS": "3",
			},
			wantError: "max_idle_conns must not exceed max_open_conns",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearDatabaseEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			_, err := LoadServerConfig(ModeServer)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error=%v want containing %q", err, tt.wantError)
			}
		})
	}
}
