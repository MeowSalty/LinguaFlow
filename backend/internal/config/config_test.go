package config

import (
	"strings"
	"testing"
	"time"
)

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
	if err := ValidateServerConfig(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != ModeLocal {
		t.Fatalf("mode=%q want %q", cfg.Mode, ModeLocal)
	}
}
