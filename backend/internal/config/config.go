package config

import (
	"fmt"
	"math"
	"net"
	"path/filepath"
	"strconv"
	"time"
)

// Config 是 LinguaFlow 的根配置。字段顺序与 linguaflow.example.yaml 对齐。
type Config struct {
	Version    int    `yaml:"version"`
	SourceLang string `yaml:"source_lang"`
	TargetLang string `yaml:"target_lang"`

	// Deprecated: CLI 端应使用 CLIConfig.Execution 替代。
	// 保留此字段是为了 Web Server 端兼容。
	Backends []BackendConfig `yaml:"backends"`

	// Deprecated: CLI 端应使用 CLIConfig.TranslationProfiles 替代。
	Pipeline PipelineConfig `yaml:"pipeline"`

	// Deprecated: CLI 端应使用 CLIConfig.PromptTemplates 替代。
	Prompt PromptConfig `yaml:"prompt"`

	Glossary          GlossaryConfig `yaml:"glossary"`
	TranslationMemory TMConfig       `yaml:"translation_memory"`
	Plugins           PluginsConfig  `yaml:"plugins"`

	Output OutputConfig `yaml:"output"`
	Log    LogConfig    `yaml:"log"`
	Server ServerConfig `yaml:"server"`
}

type BackendConfig struct {
	Name               string         `yaml:"name"`
	Type               string         `yaml:"type"`
	Enabled            bool           `yaml:"enabled"`
	RateLimitPerMinute int            `yaml:"rate_limit_per_minute"` // 按后端独立限流（每分钟）；0 表示不限速
	Options            map[string]any `yaml:"options"`
}

type PipelineConfig struct {
	Protect   ProtectConfig   `yaml:"protect"`
	Translate TranslateConfig `yaml:"translate"`
	Ruby      RubyConfig      `yaml:"ruby"`
}

type ProtectConfig struct {
	Enabled bool     `yaml:"enabled"`
	Rules   []string `yaml:"rules"`
}

// RubyConfig 控制 Ruby 注音保护的行为。
type RubyConfig struct {
	Enabled       bool     `yaml:"enabled"`
	RetryBackend  string   `yaml:"retry_backend"`  // 注音对齐重试后端名称；空时使用翻译主后端
	PreserveKinds []string `yaml:"preserve_kinds"` // 保留的注音 kind 列表：phonetic/semantic/creative
}

// RubyMode 常量（引擎内部使用，根据响应模式自动选择）。
const (
	RubyModeJSON    = "json"
	RubyModeInline  = "inline" // TODO: inline 模式尚未激活，待其他功能适配完成后启用
	RubyModeSection = "section"
)

// ResponseFormatText 是纯文本响应模式的取值。
// 与 json_schema / json_object / none 对齐，由各 backend factory 独立校验。
const ResponseFormatText = "text"

// FallbackShrink 默认值：0 表示未设置，回退到此值。
const defaultFallbackShrink = 0.5

type TranslateConfig struct {
	Concurrency      int          `yaml:"concurrency"`
	BatchSize        int          `yaml:"batch_size"`          // 一次合并发送的段数；<=1 表示禁用批量
	MaxWordsPerBatch int          `yaml:"max_words_per_batch"` // 每批字词数上限；0=不限制
	FallbackShrink   float64      `yaml:"fallback_shrink"`     // (0,1) 整批失败时下一级 batch = floor(cur*shrink)；0 = 回退默认；>=1 非法
	Retry            RetryConfig  `yaml:"retry"`
	Repair           RepairConfig `yaml:"repair"`
}

type RetryConfig struct {
	MaxAttempts int  `yaml:"max_attempts"` // 重试次数；0=不重试，1=重试 1 次，以此类推；负值归 0
	BackoffMs   int  `yaml:"backoff_ms"`   // 429/503 限流退避基础时间（毫秒）
	Jitter      bool `yaml:"jitter"`       // 退避时是否添加随机抖动
}

// RepairConfig 控制 LLM 响应解析失败 / 部分缺失时的"主动修复"行为。
//
// 各子开关默认开启（见 Default）；Enabled=false 时强制全部清零，调用方可一键关闭。
// 修复算子无错时是 no-op，对正常响应零成本；主要受益场景是 Anthropic Tool Use 模拟、
// Google 等非 strict JSON Schema 后端。
type RepairConfig struct {
	Enabled              bool    `yaml:"enabled"`
	JSONStructural       bool    `yaml:"json_structural"`       // L1: BOM 剥离、多对象合并、尾随逗号、控制字符、括号补齐
	SchemaAliases        bool    `yaml:"schema_aliases"`        // L2: translation/result/output/data.translations 同义化为 translations
	Partial              bool    `yaml:"partial"`               // L2: 部分 ID 缺失时仅对缺失段重试，而非整批 shrink
	PartialThreshold     float64 `yaml:"partial_threshold"`     // (0,1]; 缺失率 ≥ 阈值时仍走 shrink，避免单段爆炸
	PlaceholderNormalize bool    `yaml:"placeholder_normalize"` // L3: 占位符大小写/下划线变体归一（仅 normalize 已知 key 的变体）
	PromptUpgrade        bool    `yaml:"prompt_upgrade"`        // L4: 解析失败或占位符仍缺失时附加反例 reminder 重试一次
}

type PostprocessConfig struct {
	Enabled    bool `yaml:"enabled"`
	TrimSpaces bool `yaml:"trim_spaces"`
}

// ContextConfig 控制翻译上下文窗口。
type ContextConfig struct {
	Enabled  bool `yaml:"enabled"`   // 是否启用上下文，默认 true
	Before   int  `yaml:"before"`    // 上下文取前 N 段，默认 1
	After    int  `yaml:"after"`     // 上下文取后 N 段，默认 1
	MaxChars int  `yaml:"max_chars"` // 每个上下文段落的字符数上限，0=不限制
}

// DefaultContextConfig 返回默认的上下文配置。
func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		Enabled:  true,
		Before:   1,
		After:    1,
		MaxChars: 0,
	}
}

type PromptConfig struct {
	SystemTemplate        string         `yaml:"system_template"`
	SystemTemplateContent string         `yaml:"system_template_content"` // 新增：内联内容，优先级高于 SystemTemplate
	UserTemplate          string         `yaml:"user_template"`           // Deprecated: 保留兼容，不再使用
	Vars                  map[string]any `yaml:"vars"`
}

type GlossaryConfig struct {
	Enabled    bool                      `yaml:"enabled"`
	Path       string                    `yaml:"path"`
	Save       bool                      `yaml:"save"`
	Bootstrap  BootstrapConfig           `yaml:"bootstrap"`  // inline 配置
	Standalone StandaloneBootstrapConfig `yaml:"standalone"` // 独立自举配置
}

// BootstrapConfig 控制内联自举（inline）：翻译的 LLM 调用顺带返回术语。
//
// InlineConflictStrategy 控制并发 worker 给同一 source 提交不同
// target 时的处理方式：First-Wins 保证全局术语表里只保留先到的版本，但后到 worker 的
// 译文已经用了被丢弃的版本，会导致文档内同一术语翻译不一致。
//   - rewrite-local（默认）：后到 worker 把本批译文里自己用的 target 字面值替换为
//     权威表中的版本；CJK 直接替换，拉丁系按词边界，歧义场景仅 Warn 不动。
//   - off：完全不处理，沿用旧行为（First-Wins + 不一致译文）。
type BootstrapConfig struct {
	Enabled                bool    `yaml:"enabled"`
	MaxTermsPer1000Chars   float64 `yaml:"max_terms_per_1000_chars"`
	MinSourceLen           int     `yaml:"min_source_len"`
	InlineConflictStrategy string  `yaml:"inline_conflict_strategy"`
}

// StandaloneBootstrapConfig 控制独立自举（pre）：translate 之前独立扫一遍文档，
// 整篇翻译都能用上抽到的术语；多一次 LLM 调用。
type StandaloneBootstrapConfig struct {
	Enabled          bool   `yaml:"enabled"`
	Template         string `yaml:"template"` // 引用 prompt_templates 中的 key（CLI 用）
	TemplateContent  string `yaml:"-"`        // 运行时解析后的 bootstrap 模板内容（不序列化）
	BatchSize        int    `yaml:"batch_size"`
	Concurrency      int    `yaml:"concurrency"`
	MaxTermsPerBatch int    `yaml:"max_terms_per_batch"`
	MinSourceLen     int    `yaml:"min_source_len"`
}

// Bootstrap 模式常量（保留用于向后兼容）。
const (
	BootstrapModeOff    = "off"
	BootstrapModePre    = "pre"
	BootstrapModeInline = "inline"
)

// Inline 模式下并发术语冲突的处理策略。
const (
	InlineConflictOff          = "off"
	InlineConflictRewriteLocal = "rewrite-local"
)

// Normalize 规范化 RepairConfig：
//   - Enabled=false 时强制清零所有子开关，调用方据此短路所有修复逻辑
//   - PartialThreshold 不在 (0,1] 时归 0.5（最常见默认）
func (r *RepairConfig) Normalize() {
	if !r.Enabled {
		r.JSONStructural = false
		r.SchemaAliases = false
		r.Partial = false
		r.PlaceholderNormalize = false
		r.PromptUpgrade = false
	}
	if r.PartialThreshold <= 0 || r.PartialThreshold > 1 || math.IsNaN(r.PartialThreshold) {
		r.PartialThreshold = 0.5
	}
}

type TMConfig struct {
	Enabled bool   `yaml:"enabled"`
	Driver  string `yaml:"driver"`
	DSN     string `yaml:"dsn"`
}

type PluginsConfig struct {
	Enabled bool     `yaml:"enabled"`
	Scripts []string `yaml:"scripts"`
}

type OutputConfig struct {
	Mode              string `yaml:"mode"`
	PreserveExtension bool   `yaml:"preserve_extension"`
	Incremental       bool   `yaml:"incremental"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type ServerConfig struct {
	Host            string             `yaml:"host"`
	Port            int                `yaml:"port"`
	Mode            string             `yaml:"mode"` // "server" (default) | "local"
	ServiceName     string             `yaml:"service_name"`
	DataDir         string             `yaml:"data_dir"`
	AutoMigrate     bool               `yaml:"auto_migrate"`
	JWTSecret       string             `yaml:"jwt_secret"`
	JWTIssuer       string             `yaml:"jwt_issuer"`
	JWTExpiry       time.Duration      `yaml:"jwt_expiry"`
	RefreshExpiry   time.Duration      `yaml:"refresh_token_expiry"`
	ShutdownTimeout time.Duration      `yaml:"shutdown_timeout"`
	CORS            CORSConfig         `yaml:"cors"`
	Registration    RegistrationConfig `yaml:"registration"`
}

// RegistrationConfig 定义用户注册的初始默认值。
//
// 仅用于首次启动时初始化数据库中的 system_setting（registration_enabled），
// 运行时以数据库为准，管理员可通过 API 热修改。修改此值对已初始化的实例无影响。
type RegistrationConfig struct {
	Enabled   bool `yaml:"enabled"`
	AutoAdmin bool `yaml:"auto_admin"`
}

const (
	ModeServer = "server"
	ModeLocal  = "local"
)

func (c ServerConfig) IsLocal() bool {
	return c.Mode == ModeLocal
}

type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
}

func (c ServerConfig) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

func (c ServerConfig) DatabasePath() string {
	return filepath.Join(c.DataDir, "linguaflow.db")
}

func (c ServerConfig) DatabaseDSN() string {
	return c.DatabasePath() +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)"
}

// Default 返回内置默认配置。loader 在解析 yaml 前以此为基底合并。
func Default() *Config {
	return &Config{
		Version:    1,
		SourceLang: "auto",
		TargetLang: "zh",
		Backends: []BackendConfig{{
			Name:    "openai-default",
			Type:    "openai",
			Enabled: true,
			Options: map[string]any{
				"api_key":         "${OPENAI_API_KEY}",
				"base_url":        "https://api.openai.com/v1",
				"model":           "gpt-4o-mini",
				"temperature":     0.2,
				"max_tokens":      0,
				"timeout":         "60s",
				"response_format": "json_schema",
			},
		}},
		Pipeline: PipelineConfig{
			Protect: ProtectConfig{
				Enabled: true,
				Rules:   []string{"code", "link", "placeholder", "xml"},
			},
			Ruby: RubyConfig{Enabled: false},
			Translate: TranslateConfig{
				Concurrency:    4,
				BatchSize:      1,
				FallbackShrink: defaultFallbackShrink,
				Retry:          RetryConfig{MaxAttempts: 3, BackoffMs: 2000},
				Repair: RepairConfig{
					Enabled:              true,
					JSONStructural:       true,
					SchemaAliases:        true,
					Partial:              true,
					PartialThreshold:     0.5,
					PlaceholderNormalize: true,
					PromptUpgrade:        true,
				},
			},
		},
		Prompt: PromptConfig{
			Vars: map[string]any{"style": "concise, technical", "audience": "developers"},
		},
		Glossary: GlossaryConfig{
			Enabled: false,
			Path:    "./glossary.csv",
			Save:    true,
			Bootstrap: BootstrapConfig{
				MaxTermsPer1000Chars:   3.0,
				MinSourceLen:           2,
				InlineConflictStrategy: InlineConflictRewriteLocal,
			},
			Standalone: StandaloneBootstrapConfig{
				Enabled:          false,
				BatchSize:        20,
				Concurrency:      2,
				MaxTermsPerBatch: 20,
				MinSourceLen:     2,
			},
		},
		TranslationMemory: TMConfig{Enabled: false, Driver: "sqlite", DSN: "./.linguaflow/tm.db"},
		Plugins:           PluginsConfig{Enabled: false},
		Output:            OutputConfig{Mode: "overwrite", PreserveExtension: true, Incremental: false},
		Log:               LogConfig{Level: "info", Format: "text"},
		Server: ServerConfig{
			Host:            "0.0.0.0",
			Port:            8080,
			ServiceName:     "linguaflow",
			DataDir:         "./data",
			AutoMigrate:     true,
			JWTSecret:       "dev-insecure-secret-change-me",
			JWTIssuer:       "linguaflow",
			JWTExpiry:       15 * time.Minute,
			RefreshExpiry:   30 * 24 * time.Hour,
			ShutdownTimeout: 10 * time.Second,
			CORS: CORSConfig{
				AllowedOrigins: []string{"*"},
			},
			Registration: RegistrationConfig{
				Enabled:   true,
				AutoAdmin: true,
			},
		},
	}
}

// Validate 检查关键字段是否合法。loader 在合并后调用。
func (c *Config) Validate() error {
	if c.TargetLang == "" {
		return errEmptyTargetLang
	}
	// 校验后端 name 唯一性和 name/type 非空
	seen := make(map[string]int, len(c.Backends))
	for i, b := range c.Backends {
		if b.Name == "" {
			return fmt.Errorf("配置错误：backends[%d].name 不能为空", i)
		}
		if b.Type == "" {
			return fmt.Errorf("配置错误：backends[%d].type 不能为空（后端 %q）", i, b.Name)
		}
		if prev, dup := seen[b.Name]; dup {
			return fmt.Errorf("%w：%q 在 backends[%d] 与 backends[%d] 重复",
				errDuplicateBackendName, b.Name, i, prev)
		}
		seen[b.Name] = i
	}
	enabled := 0
	for _, b := range c.Backends {
		if b.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return errNoEnabledBackend
	}
	if c.Pipeline.Translate.Concurrency < 1 {
		c.Pipeline.Translate.Concurrency = 1
	}
	if c.Pipeline.Translate.BatchSize <= 0 && c.Pipeline.Translate.MaxWordsPerBatch <= 0 {
		c.Pipeline.Translate.BatchSize = 1
	}
	if c.Pipeline.Translate.Retry.BackoffMs < 1000 {
		c.Pipeline.Translate.Retry.BackoffMs = 1000
	}
	if c.Pipeline.Translate.Retry.MaxAttempts < 0 {
		c.Pipeline.Translate.Retry.MaxAttempts = 0
	}
	if shrink := c.Pipeline.Translate.FallbackShrink; math.IsNaN(shrink) || math.IsInf(shrink, 0) || shrink < 0 {
		c.Pipeline.Translate.FallbackShrink = defaultFallbackShrink
	} else if shrink >= 1 {
		return fmt.Errorf("pipeline.translate.fallback_shrink must be < 1, got %v", shrink)
	}
	validRubyKinds := map[string]bool{"phonetic": true, "semantic": true, "creative": true}
	for _, k := range c.Pipeline.Ruby.PreserveKinds {
		if !validRubyKinds[k] {
			return fmt.Errorf("pipeline.ruby.preserve_kinds: invalid kind %q (must be one of phonetic, semantic, creative)", k)
		}
	}
	c.Pipeline.Translate.Repair.Normalize()
	// Inline bootstrap 校验
	if c.Glossary.Bootstrap.MaxTermsPer1000Chars <= 0 {
		c.Glossary.Bootstrap.MaxTermsPer1000Chars = 3.0
	}
	if c.Glossary.Bootstrap.MinSourceLen < 1 {
		c.Glossary.Bootstrap.MinSourceLen = 2
	}
	switch c.Glossary.Bootstrap.InlineConflictStrategy {
	case "":
		c.Glossary.Bootstrap.InlineConflictStrategy = InlineConflictRewriteLocal
	case InlineConflictOff, InlineConflictRewriteLocal:
		// ok
	default:
		return fmt.Errorf("glossary.bootstrap.inline_conflict_strategy must be one of off|rewrite-local, got %q",
			c.Glossary.Bootstrap.InlineConflictStrategy)
	}
	// Standalone bootstrap 校验
	if c.Glossary.Standalone.Enabled {
		c.Glossary.Enabled = true
		if c.Glossary.Standalone.TemplateContent == "" {
			return fmt.Errorf("glossary.standalone.template_content is required when enabled is true")
		}
	}
	if c.Glossary.Standalone.BatchSize < 1 {
		c.Glossary.Standalone.BatchSize = 20
	}
	if c.Glossary.Standalone.Concurrency < 1 {
		c.Glossary.Standalone.Concurrency = 2
	}
	if c.Glossary.Standalone.MaxTermsPerBatch < 1 {
		c.Glossary.Standalone.MaxTermsPerBatch = 20
	}
	if c.Glossary.Standalone.MinSourceLen < 1 {
		c.Glossary.Standalone.MinSourceLen = 2
	}
	switch c.Server.Mode {
	case "", ModeServer:
		c.Server.Mode = ModeServer
	case ModeLocal:
		// ok
	default:
		return fmt.Errorf("server.mode must be one of %s|%s, got %q", ModeServer, ModeLocal, c.Server.Mode)
	}
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		c.Server.Port = 8080
	}
	if c.Server.DataDir == "" {
		c.Server.DataDir = "./data"
	}
	if c.Server.JWTSecret == "" {
		c.Server.JWTSecret = "dev-insecure-secret-change-me"
	}
	if c.Server.JWTIssuer == "" {
		c.Server.JWTIssuer = "linguaflow"
	}
	if c.Server.JWTExpiry <= 0 {
		c.Server.JWTExpiry = 15 * time.Minute
	}
	if c.Server.RefreshExpiry <= 0 {
		c.Server.RefreshExpiry = 30 * 24 * time.Hour
	}
	if c.Server.ShutdownTimeout <= 0 {
		c.Server.ShutdownTimeout = 10 * time.Second
	}
	if len(c.Server.CORS.AllowedOrigins) == 0 {
		c.Server.CORS.AllowedOrigins = []string{"*"}
	}
	// Registration defaults: Enabled and AutoAdmin both default to true.
	// Go bools default to false, so we normalize zero-values here.
	// If the user explicitly sets enabled: false, that will be preserved
	// because the YAML parser will have set it. We only default when
	// both fields are false AND the registration block was likely omitted.
	// However, since we can't distinguish "omitted" from "explicitly false",
	// we use a separate flag approach: if Enabled is false AND AutoAdmin is false,
	// we check if the registration block exists in the config.
	// For simplicity, we default both to true only in Default(), and
	// here in Validate() we just ensure they're valid.
	return nil
}
