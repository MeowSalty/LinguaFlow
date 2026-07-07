package config

import (
	"fmt"
	"math"
	"net"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// ProtectConfig 控制内容保护的行为。
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

// RepairConfig 控制 LLM 响应解析失败 / 部分缺失时的"主动修复"行为。
//
// 各子开关默认开启（见 DefaultServerConfig）；Enabled=false 时强制全部清零，调用方可一键关闭。
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

type RetryConfig struct {
	MaxAttempts int  `yaml:"max_attempts"` // 重试次数；0=不重试，1=重试 1 次，以此类推；负值归 0
	BackoffMs   int  `yaml:"backoff_ms"`   // 429/503 限流退避基础时间（毫秒）
	Jitter      bool `yaml:"jitter"`       // 退避时是否添加随机抖动
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

// WorkerConfig 控制后台任务 Worker 的并发和队列参数。
type WorkerConfig struct {
	Translation RunnerConfig `yaml:"translation"`
	Sync        RunnerConfig `yaml:"sync"`
}

// RunnerConfig 单个 Runner 的并发数和队列容量。
type RunnerConfig struct {
	Count         int `yaml:"count"`          // Worker goroutine 数，默认 NumCPU()（下限 2）
	QueueCapacity int `yaml:"queue_capacity"` // 队列最大排队深度
}

// DefaultWorkerConfig 返回默认的 Worker 配置。
// Worker 数量基于 CPU 核数，下限 2。
// 队列容量按 Worker 数倍增：翻译 4x，同步 8x。
func DefaultWorkerConfig() WorkerConfig {
	count := runtime.NumCPU()
	if count < 2 {
		count = 2
	}
	return WorkerConfig{
		Translation: RunnerConfig{Count: count, QueueCapacity: count * 4},
		Sync:        RunnerConfig{Count: count, QueueCapacity: count * 8},
	}
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
	Workers         WorkerConfig       `yaml:"workers"`
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

// DefaultServerConfig 返回内置默认服务器配置。loader 在解析 yaml 前以此为基底合并。
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
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
		Workers:         DefaultWorkerConfig(),
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
		},
		Registration: RegistrationConfig{
			Enabled:   true,
			AutoAdmin: true,
		},
	}
}

// ValidateServerConfig 检查服务器配置字段是否合法。loader 在合并后调用。
func ValidateServerConfig(c *ServerConfig) error {
	switch c.Mode {
	case "", ModeServer:
		c.Mode = ModeServer
	case ModeLocal:
		// ok
	default:
		return fmt.Errorf("server.mode must be one of %s|%s, got %q", ModeServer, ModeLocal, c.Mode)
	}
	if c.Host == "" {
		c.Host = "0.0.0.0"
	}
	if c.Port < 1 || c.Port > 65535 {
		c.Port = 8080
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.JWTSecret == "" {
		c.JWTSecret = "dev-insecure-secret-change-me"
	}
	if c.JWTIssuer == "" {
		c.JWTIssuer = "linguaflow"
	}
	if c.JWTExpiry <= 0 {
		c.JWTExpiry = 15 * time.Minute
	}
	if c.RefreshExpiry <= 0 {
		c.RefreshExpiry = 30 * 24 * time.Hour
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 10 * time.Second
	}
	if len(c.CORS.AllowedOrigins) == 0 {
		c.CORS.AllowedOrigins = []string{"*"}
	}
	if c.Workers.Translation.Count < 1 {
		c.Workers.Translation.Count = 1
	}
	if c.Workers.Translation.QueueCapacity < 1 {
		c.Workers.Translation.QueueCapacity = 1
	}
	if c.Workers.Sync.Count < 1 {
		c.Workers.Sync.Count = 1
	}
	if c.Workers.Sync.QueueCapacity < 1 {
		c.Workers.Sync.QueueCapacity = 1
	}
	return nil
}
