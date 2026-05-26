package config

import (
	"testing"
	"time"
)

func TestValidateTranslatePlan_InheritsDefaults(t *testing.T) {
	cfg := Default()
	cfg.Pipeline.Translate.BatchSize = 40
	cfg.Pipeline.Translate.Concurrency = 4
	cfg.Pipeline.Translate.BackendMode = BackendModeRestrict
	cfg.Pipeline.Translate.BackendOrder = []string{"openai-default"}
	cfg.Pipeline.Translate.Plan = []TranslateRoundConfig{{Name: "bulk"}}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	round := cfg.Pipeline.Translate.Plan[0]
	if round.BatchSize != 40 {
		t.Fatalf("batch_size=%d want 40", round.BatchSize)
	}
	if round.Concurrency != 4 {
		t.Fatalf("concurrency=%d want 4", round.Concurrency)
	}
	if round.BackendMode != BackendModeRestrict {
		t.Fatalf("backend_mode=%q want %q", round.BackendMode, BackendModeRestrict)
	}
	if len(round.BackendOrder) != 1 || round.BackendOrder[0] != "openai-default" {
		t.Fatalf("backend_order=%v want [openai-default]", round.BackendOrder)
	}
}

func TestValidateTranslatePlan_InvalidBackendOrder(t *testing.T) {
	cfg := Default()
	cfg.Pipeline.Translate.Plan = []TranslateRoundConfig{{
		Name:         "bulk",
		BackendMode:  BackendModeRestrict,
		BackendOrder: []string{"missing-backend"},
	}}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validate error for invalid plan backend_order")
	}
}

func TestValidateServerConfig_Defaults(t *testing.T) {
	cfg := Default()
	cfg.Server.Host = ""
	cfg.Server.Port = 0
	cfg.Server.DataDir = ""
	cfg.Server.JWTSecret = ""
	cfg.Server.JWTIssuer = ""
	cfg.Server.JWTExpiry = 0
	cfg.Server.RefreshExpiry = 0
	cfg.Server.ShutdownTimeout = 0
	cfg.Server.CORS.AllowedOrigins = nil

	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Fatalf("host=%q want 0.0.0.0", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Fatalf("port=%d want 8080", cfg.Server.Port)
	}
	if cfg.Server.DataDir != "./data" {
		t.Fatalf("data_dir=%q want ./data", cfg.Server.DataDir)
	}
	if cfg.Server.JWTSecret == "" {
		t.Fatal("jwt_secret should be defaulted")
	}
	if cfg.Server.JWTIssuer != "linguaflow" {
		t.Fatalf("jwt_issuer=%q want linguaflow", cfg.Server.JWTIssuer)
	}
	if cfg.Server.JWTExpiry != time.Hour {
		t.Fatalf("jwt_expiry=%v want 1h", cfg.Server.JWTExpiry)
	}
	if cfg.Server.RefreshExpiry != 30*24*time.Hour {
		t.Fatalf("refresh_token_expiry=%v want 720h", cfg.Server.RefreshExpiry)
	}
	if cfg.Server.ShutdownTimeout <= 0 {
		t.Fatalf("shutdown_timeout=%v want > 0", cfg.Server.ShutdownTimeout)
	}
	if len(cfg.Server.CORS.AllowedOrigins) != 1 || cfg.Server.CORS.AllowedOrigins[0] != "*" {
		t.Fatalf("allowed_origins=%v want [*]", cfg.Server.CORS.AllowedOrigins)
	}
}
