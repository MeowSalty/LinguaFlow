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

// PipelineConfig 流水线准入控制（资源入线的字节配额与资源数上限）。
// weight 语义为「在途工作配额」：以源文本字节为代理，同时关联内存与
// LLM 成本，不承诺精确等于进程内存——每资源峰值 ≈ 2~4× 源文本字节
// + 每轮批提示词固定开销；固定开销由 MaxInflightResources 兜底。
// 注意：配额是并发节流而非内存上限（内存上限由 RssLimitMB 保险丝
// 进程级兜底），超预算的单资源在预算空置时独跑放行，不会饥饿。
// 口径为「每任务」：预算随每个任务的执行实例化，并发任务各自独占
// 一份——进程级实际在途量 ≈ 配额 × 并发任务数，进程级兜底仅 RssLimitMB。
type PipelineConfig struct {
	// MaxInflightWeightMB 在途工作配额上限（源文本字节，单位 MB，每任务口径）；
	// 必须为正，缺省为 32。它控制并发入线源文本量，不是硬性内存上限。
	MaxInflightWeightMB int `yaml:"max_inflight_weight_mb"`
	// MaxInflightResources 在途资源数上限（每任务口径；兜住每资源句柄开销）；
	// 必须为正；缺省为 8。
	MaxInflightResources int `yaml:"max_inflight_resources"`
	// RssLimitMB 进程级 RSS 保险丝上限（MB）；0 = 关闭。
	// 双水位：≥85% 暂停所有任务的新资源准入（只出不进），≤70% 恢复。
	// 触发不改变任务状态（任务保持 running，资源排队），仅记结构化日志。
	RssLimitMB int `yaml:"rss_limit_mb"`
}

// 流水线准入默认值。
const (
	defaultMaxInflightWeightMB  = 32
	defaultMaxInflightResources = 8
)

// DefaultPipelineConfig 返回默认的流水线准入配置（RSS 保险丝默认关闭）。
func DefaultPipelineConfig() PipelineConfig {
	return PipelineConfig{
		MaxInflightWeightMB:  defaultMaxInflightWeightMB,
		MaxInflightResources: defaultMaxInflightResources,
		RssLimitMB:           0,
	}
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
	MaxConcurrency int           `yaml:"max_concurrency"` // 全局同时进行的预览数；必须为正，缺省为 2
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
	// 全局并发上限 = MaxConcurrency × 4。必须在 1–32 范围内，缺省为 2，
	// 避免误配放大 AI 速率/成本预算。
	MaxConcurrency int `yaml:"max_concurrency"`
	// Timeout 为单次即时翻译执行超时（默认 5 分钟，对齐 Preview——二者复用同一套
	// 多轮 LLM pipeline，含 429 指数退避，单轮 LLM 调用可能较慢）。必须为正，
	// 超过 MaxTimeout 明确报错。
	Timeout time.Duration `yaml:"timeout"`
	// MaxTimeout 为 Timeout 的硬上限，防止运维或用户误配过长超时占满并发槽位。
	// 必须为正且不超过 30 分钟；缺省为 30 分钟。
	MaxTimeout time.Duration `yaml:"max_timeout"`
}

// quickTranslateMaxConcurrencyUpper 是 per-actor 并发绝对上限。
const quickTranslateMaxConcurrencyUpper = 32

// quickTranslateMaxTimeoutUpper 是 Timeout 与 MaxTimeout 的绝对上限，
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
	// 必须为正；缺省为 256。
	RingBufferCapacity int `yaml:"ring_buffer_capacity"`
	// ReplayBatchSize 为 SSE 首次回放（历史补进）从 DB 拉取的每批事件数。
	// 必须为正；缺省为 200。
	ReplayBatchSize int `yaml:"replay_batch_size"`
	// MaxReplayEvents 为 SSE 单次连接历史回放的总量上限。
	// 达到上限即停止回放，缺口交给前端通过 Last-Event-ID 续传或 REST 历史端点补全。
	// 必须为正；缺省按最终 RingBufferCapacity 的 2 倍派生。
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
	Host              string               `yaml:"host"`
	Port              int                  `yaml:"port"`
	Mode              string               `yaml:"mode"` // "server" (default) | "local"
	ServiceName       string               `yaml:"service_name"`
	DataDir           string               `yaml:"data_dir"`
	AutoMigrate       bool                 `yaml:"auto_migrate"`
	JWTSecret         string               `yaml:"jwt_secret"`
	JWTIssuer         string               `yaml:"jwt_issuer"`
	JWTExpiry         time.Duration        `yaml:"jwt_expiry"`
	RefreshExpiry     time.Duration        `yaml:"refresh_token_expiry"`
	ShutdownTimeout   time.Duration        `yaml:"shutdown_timeout"`
	RevisionRetention time.Duration        `yaml:"revision_retention"`
	Database          DatabaseConfig       `yaml:"database"`
	Workers           WorkerConfig         `yaml:"workers"`
	Pipeline          PipelineConfig       `yaml:"pipeline"`
	Preview           PreviewConfig        `yaml:"preview"`
	QuickTranslate    QuickTranslateConfig `yaml:"quick_translate"`
	SSE               SSEConfig            `yaml:"sse"`
	CORS              CORSConfig           `yaml:"cors"`
	Credentials       CredentialsConfig    `yaml:"credentials"`
	Storage           StorageConfig        `yaml:"storage"`
	ServeUI           bool                 `yaml:"serve_ui"`
}

// CredentialsConfig 标识部署侧持有的加密密钥。
type CredentialsConfig struct {
	KeyringFile string `yaml:"keyring_file"`
}

// BootstrapInput 仅由实例初始化事务消费。
type BootstrapInput struct {
	RegistrationEnabled bool
	Admin               *BootstrapAdmin
}

type BootstrapAdmin struct {
	Username string
	Email    string
	Password string
}

// RuntimeAddress 是实际绑定的地址，与请求的配置相互独立。
type RuntimeAddress struct {
	Host string
	Port int
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

// DefaultServerConfig 返回纯部署默认值。它不包含可用 JWT secret；
// loader 在合并显式输入后补派生默认，再进行只读校验。
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Mode:              ModeServer,
		Host:              "0.0.0.0",
		Port:              8080,
		ServiceName:       "linguaflow",
		DataDir:           "./data",
		AutoMigrate:       true,
		JWTIssuer:         "linguaflow",
		JWTExpiry:         15 * time.Minute,
		RefreshExpiry:     30 * 24 * time.Hour,
		ShutdownTimeout:   10 * time.Second,
		RevisionRetention: 90 * 24 * time.Hour,
		Database:          defaultDatabaseConfig(DatabaseDriverSQLite),
		Workers:           DefaultWorkerConfig(),
		Pipeline:          DefaultPipelineConfig(),
		Preview:           DefaultPreviewConfig(),
		QuickTranslate:    DefaultQuickTranslateConfig(),
		SSE:               DefaultSSEConfig(),
		Storage:           DefaultStorageConfig(),
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
		},
		ServeUI: true,
	}
}

// ValidateServerConfig 校验完整解析后的配置，且不做任何修改。
func ValidateServerConfig(c *ServerConfig) error {
	return validateServerConfig(c, true)
}

func validateServerConfig(c *ServerConfig, requireSecret bool) error {
	if c.Mode != ModeServer && c.Mode != ModeLocal {
		return fmt.Errorf("server.mode must be one of %s|%s", ModeServer, ModeLocal)
	}
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("server.host must not be empty")
	}
	if c.Port < 0 || c.Port > 65535 || (c.Port == 0 && !c.IsLocal()) {
		return fmt.Errorf("server.port must be between 1 and 65535 (local also accepts 0)")
	}
	for key, value := range map[string]string{"data_dir": c.DataDir, "jwt_issuer": c.JWTIssuer, "service_name": c.ServiceName} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("server.%s must not be empty", key)
		}
	}
	if requireSecret && len(c.JWTSecret) < 32 {
		return fmt.Errorf("server.jwt_secret must contain at least 32 bytes")
	}
	for key, value := range map[string]time.Duration{
		"jwt_expiry": c.JWTExpiry, "refresh_token_expiry": c.RefreshExpiry, "shutdown_timeout": c.ShutdownTimeout,
		"revision_retention": c.RevisionRetention, "preview.timeout": c.Preview.Timeout, "preview.apply_token_ttl": c.Preview.ApplyTokenTTL,
		"quick_translate.timeout": c.QuickTranslate.Timeout, "quick_translate.max_timeout": c.QuickTranslate.MaxTimeout,
	} {
		if value <= 0 {
			return fmt.Errorf("server.%s must be positive", key)
		}
	}
	if c.Database.Driver != DatabaseDriverSQLite && c.Database.Driver != DatabaseDriverPostgres {
		return fmt.Errorf("server.database.driver must be sqlite or postgres")
	}
	if c.IsLocal() && c.Database.Driver != DatabaseDriverSQLite {
		return fmt.Errorf("server.database.driver is not configurable in local mode")
	}
	if c.Database.Driver == DatabaseDriverPostgres && strings.TrimSpace(c.Database.DSN) == "" {
		return fmt.Errorf("server.database.dsn is required for postgres")
	}
	if c.Database.Driver == DatabaseDriverSQLite && c.Database.DSN != "" {
		if err := validateSQLitePath(c.Database.DSN); err != nil {
			return err
		}
	}
	if c.Database.MaxOpenConns < 0 {
		return fmt.Errorf("server.database.max_open_conns must not be negative")
	}
	if c.Database.MaxIdleConns < 0 {
		return fmt.Errorf("server.database.max_idle_conns must not be negative")
	}
	if c.Database.ConnMaxLifetime < 0 {
		return fmt.Errorf("server.database.conn_max_lifetime must not be negative")
	}
	if c.Database.MaxOpenConns > 0 && c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		return fmt.Errorf("server.database.max_idle_conns must not exceed max_open_conns")
	}
	for key, value := range map[string]int{
		"workers.translation.count": c.Workers.Translation.Count, "workers.translation.queue_capacity": c.Workers.Translation.QueueCapacity,
		"workers.sync.count": c.Workers.Sync.Count, "workers.sync.queue_capacity": c.Workers.Sync.QueueCapacity,
		"pipeline.max_inflight_weight_mb": c.Pipeline.MaxInflightWeightMB, "pipeline.max_inflight_resources": c.Pipeline.MaxInflightResources,
		"preview.max_concurrency": c.Preview.MaxConcurrency, "quick_translate.max_concurrency": c.QuickTranslate.MaxConcurrency,
		"sse.ring_buffer_capacity": c.SSE.RingBufferCapacity, "sse.replay_batch_size": c.SSE.ReplayBatchSize, "sse.max_replay_events": c.SSE.MaxReplayEvents,
	} {
		if value <= 0 {
			return fmt.Errorf("server.%s must be positive", key)
		}
	}
	if c.Pipeline.RssLimitMB < 0 {
		return fmt.Errorf("server.pipeline.rss_limit_mb must not be negative")
	}
	maxInt := int(^uint(0) >> 1)
	if c.Workers.Translation.Count > maxInt/4 || c.Workers.Sync.Count > maxInt/8 {
		return fmt.Errorf("server.workers counts exceed supported queue size arithmetic")
	}
	if c.SSE.RingBufferCapacity > maxInt/2 {
		return fmt.Errorf("server.sse.ring_buffer_capacity exceeds supported replay size arithmetic")
	}
	if uint64(c.Pipeline.MaxInflightWeightMB) > uint64(1<<63-1)/(1024*1024) || uint64(c.Pipeline.RssLimitMB) > uint64(1<<63-1)/(1024*1024) {
		return fmt.Errorf("server.pipeline MB limits exceed supported byte size arithmetic")
	}
	if c.QuickTranslate.MaxConcurrency > quickTranslateMaxConcurrencyUpper {
		return fmt.Errorf("server.quick_translate.max_concurrency must not exceed 32")
	}
	if c.QuickTranslate.MaxTimeout > quickTranslateMaxTimeoutUpper {
		return fmt.Errorf("server.quick_translate.max_timeout must not exceed 30m")
	}
	if c.QuickTranslate.Timeout > c.QuickTranslate.MaxTimeout {
		return fmt.Errorf("server.quick_translate.timeout must not exceed max_timeout")
	}
	return c.Storage.Validate()
}

func validateSQLitePath(dsn string) error {
	path, _, _ := strings.Cut(dsn, "?")
	path = strings.TrimPrefix(path, "file:")
	if path == ":memory:" || strings.Contains(dsn, "mode=memory") {
		return nil
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("server.database.dsn SQLite file path must be absolute")
	}
	return nil
}
