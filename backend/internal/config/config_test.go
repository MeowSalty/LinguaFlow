package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

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

func TestValidate_DuplicateBackendName(t *testing.T) {
	cfg := Default()
	cfg.Backends = []BackendConfig{
		{Name: "openai", Type: "openai", Enabled: true},
		{Name: "openai", Type: "anthropic", Enabled: true},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate backend name")
	}
	if !errors.Is(err, errDuplicateBackendName) {
		t.Errorf("expected errDuplicateBackendName, got: %v", err)
	}
}

func TestValidate_EmptyBackendName(t *testing.T) {
	cfg := Default()
	cfg.Backends = []BackendConfig{
		{Name: "", Type: "openai", Enabled: true},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty backend name")
	}
	if !strings.Contains(err.Error(), "name 不能为空") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidate_EmptyBackendType(t *testing.T) {
	cfg := Default()
	cfg.Backends = []BackendConfig{
		{Name: "my-backend", Type: "", Enabled: true},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty backend type")
	}
	if !strings.Contains(err.Error(), "type 不能为空") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidate_UniqueBackendNames(t *testing.T) {
	cfg := Default()
	cfg.Backends = []BackendConfig{
		{Name: "openai-primary", Type: "openai", Enabled: true},
		{Name: "anthropic-backup", Type: "anthropic", Enabled: false},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Bootstrap 模板必填校验
// ---------------------------------------------------------------------------

func TestValidate_BootstrapTemplateRequired_PreMode(t *testing.T) {
	cfg := Default()
	cfg.Glossary.Bootstrap.Mode = BootstrapModePre
	cfg.Glossary.Bootstrap.Template = "" // 缺少模板引用
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing bootstrap template in pre mode")
	}
	if !strings.Contains(err.Error(), "glossary.bootstrap.template is required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidate_BootstrapTemplateRequired_InlineMode(t *testing.T) {
	cfg := Default()
	cfg.Glossary.Bootstrap.Mode = BootstrapModeInline
	cfg.Glossary.Bootstrap.Template = "" // 缺少模板引用
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing bootstrap template in inline mode")
	}
	if !strings.Contains(err.Error(), "glossary.bootstrap.template is required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidate_BootstrapTemplateNotRequired_OffMode(t *testing.T) {
	cfg := Default()
	cfg.Glossary.Bootstrap.Mode = BootstrapModeOff
	cfg.Glossary.Bootstrap.Template = "" // off 模式下不要求模板
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error in off mode: %v", err)
	}
}

func TestValidate_BootstrapTemplateProvided(t *testing.T) {
	cfg := Default()
	cfg.Glossary.Bootstrap.Mode = BootstrapModePre
	cfg.Glossary.Bootstrap.Template = "通用提示词" // 提供模板引用
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error when template is provided: %v", err)
	}
	// 校验 Glossary 被自动开启
	if !cfg.Glossary.Enabled {
		t.Error("expected glossary.enabled to be auto-set to true when bootstrap mode is not off")
	}
}

func TestValidate_BootstrapModeInvalid(t *testing.T) {
	cfg := Default()
	cfg.Glossary.Bootstrap.Mode = "invalid"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid bootstrap mode")
	}
	if !strings.Contains(err.Error(), "glossary.bootstrap.mode must be one of") {
		t.Errorf("unexpected error message: %v", err)
	}
}
