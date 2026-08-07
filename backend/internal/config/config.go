package config

import (
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	Enabled              bool `yaml:"enabled"`
	JSONStructural       bool `yaml:"json_structural"`       // L1: BOM 剥离、多对象合并、尾随逗号、控制字符、括号补齐
	SchemaAliases        bool `yaml:"schema_aliases"`        // L2: translation/result/output/data.translations 同义化为 translations
	PlaceholderNormalize bool `yaml:"placeholder_normalize"` // L3: 占位符大小写/下划线变体归一（仅 normalize 已知 key 的变体）
	PromptUpgrade        bool `yaml:"prompt_upgrade"`        // L4: 解析失败或占位符仍缺失时附加反例 reminder 重试一次
}

type PostprocessConfig struct {
	Enabled    bool `yaml:"enabled"`
	TrimSpaces bool `yaml:"trim_spaces"`
}

// QAConfig 控制翻译质量检测的行为。
type QAConfig struct {
	Enabled        bool     `yaml:"enabled"`
	AutoReject     bool     `yaml:"auto_reject"`
	Checks         []string `yaml:"checks,omitempty"`
	LengthMethod   string   `yaml:"length_method"`
	LengthRatioMin float64  `yaml:"length_ratio_min"`
	LengthRatioMax float64  `yaml:"length_ratio_max"`
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

// Bootstrap 模式常量（保留用于向后兼容）。
const (
	BootstrapModeOff    = "off"
	BootstrapModePre    = "pre"
	BootstrapModeInline = "inline"
)

// Normalize 规范化 RepairConfig：
//   - Enabled=false 时强制清零所有子开关，调用方据此短路所有修复逻辑
func (r *RepairConfig) Normalize() {
	if !r.Enabled {
		r.JSONStructural = false
		r.SchemaAliases = false
		r.PlaceholderNormalize = false
		r.PromptUpgrade = false
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

// PreviewConfig 控制单段翻译预览（同步接口）的并发与生命周期。
type PreviewConfig struct {
	MaxConcurrency int           `yaml:"max_concurrency"` // 全局同时进行的预览数；<=0 时使用默认值 2
	Timeout        time.Duration `yaml:"timeout"`         // 单次预览执行超时
	ApplyTokenTTL  time.Duration `yaml:"apply_token_ttl"` // apply_token 有效期
}

// DefaultPreviewConfig 返回默认的 Preview 配置。
func DefaultPreviewConfig() PreviewConfig {
	return PreviewConfig{
		MaxConcurrency: 2,
		Timeout:        5 * time.Minute,
		ApplyTokenTTL:  15 * time.Minute,
	}
}

// QuickTranslateConfig 控制即时翻译（同步单段在线翻译）的并发与生命周期。
// 译文纯临时不落库，故无 apply_token_ttl。
type QuickTranslateConfig struct {
	// MaxConcurrency 为单 actor 同时进行的即时翻译并发上限（per-actor 信号量）；
	// 全局并发上限 = MaxConcurrency × 4。<=0 时使用默认值 2，>32 时钳制为 32，
	// 避免误配放大 AI 速率/成本预算。
	MaxConcurrency int `yaml:"max_concurrency"`
	// Timeout 为单次即时翻译执行超时（默认 5 分钟，对齐 Preview——二者复用同一套
	// 多轮 LLM pipeline，含 429 指数退避，单轮 LLM 调用可能较慢）。<=0 用默认值，
	// >MaxTimeout 钳制为 MaxTimeout，给服务器管理者一个硬安全阀。
	Timeout time.Duration `yaml:"timeout"`
	// MaxTimeout 为 Timeout 的硬上限，防止运维或用户误配过长超时占满并发槽位。
	// <=0 时使用默认值 30 分钟。
	MaxTimeout time.Duration `yaml:"max_timeout"`
}

// quickTranslateMaxConcurrencyUpper 钳制 per-actor 并发上限，避免误配放大全局负载。
const quickTranslateMaxConcurrencyUpper = 32

// quickTranslateMaxTimeoutUpper 钳制 Timeout 与 MaxTimeout 的绝对上限，
// 防止误配过长超时长时间占用并发槽位与 handler goroutine。
const quickTranslateMaxTimeoutUpper = 30 * time.Minute

// DefaultQuickTranslateConfig 返回默认的 QuickTranslate 配置。
func DefaultQuickTranslateConfig() QuickTranslateConfig {
	return QuickTranslateConfig{
		MaxConcurrency: 2,
		Timeout:        5 * time.Minute,
		MaxTimeout:     quickTranslateMaxTimeoutUpper,
	}
}

// SSEConfig 控制实时事件（SSE）回放与历史事件存储的行为。
type SSEConfig struct {
	// RingBufferCapacity 为每个 job 内存 ring buffer 的容量（用于 SSE 重连窗口补进）。
	// <=0 用默认值 256。
	RingBufferCapacity int `yaml:"ring_buffer_capacity"`
	// ReplayBatchSize 为 SSE 首次回放（历史补进）从 DB 拉取的每批事件数。
	// <=0 用默认值 200。
	ReplayBatchSize int `yaml:"replay_batch_size"`
	// MaxReplayEvents 为 SSE 单次连接历史回放的总量上限。
	// 达到上限即停止回放，缺口交给前端通过 Last-Event-ID 续传或 REST 历史端点补全。
	// <=0 用默认值（RingBufferCapacity 的 2 倍）。
	MaxReplayEvents int `yaml:"max_replay_events"`
}

// DefaultSSEConfig 返回默认的 SSE 配置。
func DefaultSSEConfig() SSEConfig {
	return SSEConfig{
		RingBufferCapacity: 256,
		ReplayBatchSize:    200,
	}
}

type ServerConfig struct {
	Host            string               `yaml:"host"`
	Port            int                  `yaml:"port"`
	Mode            string               `yaml:"mode"` // "server" (default) | "local"
	ServiceName     string               `yaml:"service_name"`
	DataDir         string               `yaml:"data_dir"`
	AutoMigrate     bool                 `yaml:"auto_migrate"`
	JWTSecret       string               `yaml:"jwt_secret"`
	JWTIssuer       string               `yaml:"jwt_issuer"`
	JWTExpiry       time.Duration        `yaml:"jwt_expiry"`
	RefreshExpiry   time.Duration        `yaml:"refresh_token_expiry"`
	ShutdownTimeout time.Duration        `yaml:"shutdown_timeout"`
	Database        DatabaseConfig       `yaml:"database"`
	Workers         WorkerConfig         `yaml:"workers"`
	Preview         PreviewConfig        `yaml:"preview"`
	QuickTranslate  QuickTranslateConfig `yaml:"quick_translate"`
	SSE             SSEConfig            `yaml:"sse"`
	CORS            CORSConfig           `yaml:"cors"`
	Registration    RegistrationConfig   `yaml:"registration"`
	ServeUI         bool                 `yaml:"serve_ui"`
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

	DatabaseDriverSQLite   = "sqlite"
	DatabaseDriverPostgres = "postgres"
)

// DatabaseConfig 定义数据库驱动、连接串和 database/sql 连接池参数。
type DatabaseConfig struct {
	Driver          string        `yaml:"driver"`
	DSN             string        `yaml:"dsn"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
}

func defaultDatabaseConfig(driver string) DatabaseConfig {
	switch driver {
	case DatabaseDriverPostgres:
		return DatabaseConfig{
			Driver:          DatabaseDriverPostgres,
			MaxOpenConns:    25,
			MaxIdleConns:    5,
			ConnMaxLifetime: 30 * time.Minute,
		}
	default:
		return DatabaseConfig{
			Driver:       DatabaseDriverSQLite,
			MaxIdleConns: 2,
		}
	}
}

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
	if c.Database.DSN != "" {
		if c.Database.Driver == DatabaseDriverSQLite {
			return sqliteDSNWithForeignKeys(c.Database.DSN)
		}
		return c.Database.DSN
	}
	return c.DatabasePath() +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)"
}

func sqliteDSNWithForeignKeys(dsn string) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	if strings.HasSuffix(dsn, "?") || strings.HasSuffix(dsn, "&") {
		separator = ""
	}
	return dsn + separator + "_pragma=foreign_keys(1)"
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
		Database:        defaultDatabaseConfig(DatabaseDriverSQLite),
		Workers:         DefaultWorkerConfig(),
		Preview:         DefaultPreviewConfig(),
		QuickTranslate:  DefaultQuickTranslateConfig(),
		SSE:             DefaultSSEConfig(),
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
		},
		Registration: RegistrationConfig{
			Enabled:   true,
			AutoAdmin: true,
		},
		ServeUI: true,
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
	if c.Port < 0 || c.Port > 65535 || (c.Port == 0 && !c.IsLocal()) {
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
	switch c.Database.Driver {
	case DatabaseDriverSQLite:
		// SQLite DSN 为空时由 DatabaseDSN 根据 data_dir 生成。
	case DatabaseDriverPostgres:
		if strings.TrimSpace(c.Database.DSN) == "" {
			return fmt.Errorf("server.database.dsn is required for postgres")
		}
	default:
		return fmt.Errorf("server.database.driver must be one of %s|%s, got %q", DatabaseDriverSQLite, DatabaseDriverPostgres, c.Database.Driver)
	}
	if c.Database.MaxOpenConns < 0 {
		return fmt.Errorf("server.database.max_open_conns must not be negative")
	}
	if c.Database.MaxIdleConns < 0 {
		return fmt.Errorf("server.database.max_idle_conns must not be negative")
	}
	if c.Database.MaxOpenConns > 0 && c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		return fmt.Errorf("server.database.max_idle_conns must not exceed max_open_conns")
	}
	if c.Database.ConnMaxLifetime < 0 {
		return fmt.Errorf("server.database.conn_max_lifetime must not be negative")
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
	if c.Preview.MaxConcurrency <= 0 {
		c.Preview.MaxConcurrency = DefaultPreviewConfig().MaxConcurrency
	}
	if c.Preview.Timeout <= 0 {
		c.Preview.Timeout = DefaultPreviewConfig().Timeout
	}
	if c.Preview.ApplyTokenTTL <= 0 {
		c.Preview.ApplyTokenTTL = DefaultPreviewConfig().ApplyTokenTTL
	}
	if c.QuickTranslate.MaxConcurrency <= 0 {
		c.QuickTranslate.MaxConcurrency = DefaultQuickTranslateConfig().MaxConcurrency
	}
	if c.QuickTranslate.MaxConcurrency > quickTranslateMaxConcurrencyUpper {
		c.QuickTranslate.MaxConcurrency = quickTranslateMaxConcurrencyUpper
	}
	// MaxTimeout 硬上限：先回填默认，再钳制到绝对上限。
	if c.QuickTranslate.MaxTimeout <= 0 {
		c.QuickTranslate.MaxTimeout = DefaultQuickTranslateConfig().MaxTimeout
	}
	if c.QuickTranslate.MaxTimeout > quickTranslateMaxTimeoutUpper {
		c.QuickTranslate.MaxTimeout = quickTranslateMaxTimeoutUpper
	}
	// Timeout：回填默认，再钳制到 MaxTimeout（管理者的运行时安全阀）。
	if c.QuickTranslate.Timeout <= 0 {
		c.QuickTranslate.Timeout = DefaultQuickTranslateConfig().Timeout
	}
	if c.QuickTranslate.Timeout > c.QuickTranslate.MaxTimeout {
		c.QuickTranslate.Timeout = c.QuickTranslate.MaxTimeout
	}
	if c.SSE.RingBufferCapacity <= 0 {
		c.SSE.RingBufferCapacity = DefaultSSEConfig().RingBufferCapacity
	}
	if c.SSE.ReplayBatchSize <= 0 {
		c.SSE.ReplayBatchSize = DefaultSSEConfig().ReplayBatchSize
	}
	if c.SSE.MaxReplayEvents <= 0 {
		c.SSE.MaxReplayEvents = c.SSE.RingBufferCapacity * 2
	}
	return nil
}
