package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// CredentialsConfig identifies deployment-owned encryption keys.
type CredentialsConfig struct {
	KeyringFile string `yaml:"keyring_file"`
}

// BootstrapInput is consumed only by the instance initialization transaction.
type BootstrapInput struct {
	RegistrationEnabled bool
	Admin               *BootstrapAdmin
}

type BootstrapAdmin struct {
	Username string
	Email    string
	Password string
}

// RuntimeAddress is the bound address, separate from the requested configuration.
type RuntimeAddress struct {
	Host string
	Port int
}

// defaultDeploymentConfig 返回纯部署默认值。它不包含可用 JWT secret；
// loader 在合并显式输入后补派生默认，再进行只读校验。
func defaultDeploymentConfig() *ServerConfig {
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
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
		},
		ServeUI: true,
	}
}

// ValidateDeploymentConfig validates a fully resolved configuration without changing it.
func ValidateDeploymentConfig(c *ServerConfig) error {
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
	return nil
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
