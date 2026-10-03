package config

import "time"

// FieldInfo 是显式声明的部署输入契约。环境变量在此逐一列出，
// 有意不做运行时结构体反射推断。
type FieldInfo struct {
	Key         string
	Environment string
	Type        string
	Modes       string
	Sensitive   bool
	Effect      string
}
type serverField struct {
	FieldInfo
	read  func(*ResolvedServer) any
	write func(*ResolvedServer, any)
}

func field[T any](key, env, kind, modes string, sensitive bool, target func(*ResolvedServer) *T) serverField {
	effect := "restart"
	if len(key) >= 10 && key[:10] == "bootstrap." {
		effect = "initialization only"
	}
	return serverField{FieldInfo: FieldInfo{key, env, kind, modes, sensitive, effect}, read: func(r *ResolvedServer) any { return *target(r) }, write: func(r *ResolvedServer, v any) { *target(r) = v.(T) }}
}
func admin(r *ResolvedServer) *BootstrapAdmin {
	if r.Bootstrap.Admin == nil {
		r.Bootstrap.Admin = &BootstrapAdmin{}
	}
	return r.Bootstrap.Admin
}

var serverFields = []serverField{
	field("server.storage.enabled", "LINGUAFLOW_STORAGE_ENABLED", "boolean", "serve/local", false, func(r *ResolvedServer) *bool { return &r.Config.Storage.Enabled }),
	field("server.storage.maintenance", "LINGUAFLOW_STORAGE_MAINTENANCE", "boolean", "serve/local", false, func(r *ResolvedServer) *bool { return &r.Config.Storage.Maintenance }),
	field("server.storage.backends", "LINGUAFLOW_STORAGE_BACKENDS", "storage_backends", "serve/local", true, func(r *ResolvedServer) *[]StorageBackendConfig { return &r.Config.Storage.Backends }),
	field("server.storage.default_site_space", "LINGUAFLOW_STORAGE_DEFAULT_SITE_SPACE", "string", "serve/local", false, func(r *ResolvedServer) *string { return &r.Config.Storage.DefaultSiteSpace }),
	field("server.storage.work_dir", "LINGUAFLOW_STORAGE_WORK_DIR", "path", "serve/local", false, func(r *ResolvedServer) *string { return &r.Config.Storage.WorkDir }),
	field("server.storage.cache_dir", "LINGUAFLOW_STORAGE_CACHE_DIR", "path", "serve/local", false, func(r *ResolvedServer) *string { return &r.Config.Storage.CacheDir }),
	field("server.storage.limits.max_file_bytes", "LINGUAFLOW_STORAGE_LIMITS_MAX_FILE_BYTES", "integer64", "serve/local", false, func(r *ResolvedServer) *int64 { return &r.Config.Storage.Limits.MaxFileBytes }),
	field("server.storage.limits.max_temp_bytes", "LINGUAFLOW_STORAGE_LIMITS_MAX_TEMP_BYTES", "integer64", "serve/local", false, func(r *ResolvedServer) *int64 { return &r.Config.Storage.Limits.MaxTempBytes }),
	field("server.storage.limits.max_output_bytes", "LINGUAFLOW_STORAGE_LIMITS_MAX_OUTPUT_BYTES", "integer64", "serve/local", false, func(r *ResolvedServer) *int64 { return &r.Config.Storage.Limits.MaxOutputBytes }),
	field("server.storage.limits.max_expanded_bytes", "LINGUAFLOW_STORAGE_LIMITS_MAX_EXPANDED_BYTES", "integer64", "serve/local", false, func(r *ResolvedServer) *int64 { return &r.Config.Storage.Limits.MaxExpandedBytes }),
	field("server.storage.limits.max_metadata_bytes", "LINGUAFLOW_STORAGE_LIMITS_MAX_METADATA_BYTES", "integer64", "serve/local", false, func(r *ResolvedServer) *int64 { return &r.Config.Storage.Limits.MaxMetadataBytes }),
	field("server.storage.limits.max_cache_bytes", "LINGUAFLOW_STORAGE_LIMITS_MAX_CACHE_BYTES", "integer64", "serve/local", false, func(r *ResolvedServer) *int64 { return &r.Config.Storage.Limits.MaxCacheBytes }),
	field("server.storage.limits.capacity_bytes", "LINGUAFLOW_STORAGE_LIMITS_CAPACITY_BYTES", "integer64", "serve/local", false, func(r *ResolvedServer) *int64 { return &r.Config.Storage.Limits.CapacityBytes }),
	field("server.storage.limits.max_archive_entries", "LINGUAFLOW_STORAGE_LIMITS_MAX_ARCHIVE_ENTRIES", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Storage.Limits.MaxArchiveEntries }),
	field("server.storage.limits.max_segments", "LINGUAFLOW_STORAGE_LIMITS_MAX_SEGMENTS", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Storage.Limits.MaxSegments }),
	field("server.storage.limits.max_concurrency", "LINGUAFLOW_STORAGE_LIMITS_MAX_CONCURRENCY", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Storage.Limits.MaxConcurrency }),
	field("server.storage.network.allowed_hosts", "LINGUAFLOW_STORAGE_NETWORK_ALLOWED_HOSTS", "strings", "serve/local", false, func(r *ResolvedServer) *[]string { return &r.Config.Storage.Network.AllowedHosts }),
	field("server.storage.network.allowed_cidrs", "LINGUAFLOW_STORAGE_NETWORK_ALLOWED_CIDRS", "strings", "serve/local", false, func(r *ResolvedServer) *[]string { return &r.Config.Storage.Network.AllowedCIDRs }),
	field("server.storage.intent_ttl", "LINGUAFLOW_STORAGE_INTENT_TTL", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.IntentTTL }),
	field("server.storage.metadata_timeout", "LINGUAFLOW_STORAGE_METADATA_TIMEOUT", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.MetadataTimeout }),
	field("server.storage.transfer_timeout", "LINGUAFLOW_STORAGE_TRANSFER_TIMEOUT", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.TransferTimeout }),
	field("server.storage.idle_timeout", "LINGUAFLOW_STORAGE_IDLE_TIMEOUT", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.IdleTimeout }),
	field("server.storage.retry_window", "LINGUAFLOW_STORAGE_RETRY_WINDOW", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.RetryWindow }),
	field("server.storage.retry_base_delay", "LINGUAFLOW_STORAGE_RETRY_BASE_DELAY", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.RetryBaseDelay }),
	field("server.storage.retry_max_delay", "LINGUAFLOW_STORAGE_RETRY_MAX_DELAY", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.RetryMaxDelay }),
	field("server.storage.signed_url_ttl", "LINGUAFLOW_STORAGE_SIGNED_URL_TTL", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.SignedURLTTL }),
	field("server.storage.signed_url_max_ttl", "LINGUAFLOW_STORAGE_SIGNED_URL_MAX_TTL", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.SignedURLMaxTTL }),
	field("server.storage.source_retention", "LINGUAFLOW_STORAGE_SOURCE_RETENTION", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.SourceRetention }),
	field("server.storage.deletion_grace", "LINGUAFLOW_STORAGE_DELETION_GRACE", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.DeletionGrace }),
	field("server.storage.reconcile_interval", "LINGUAFLOW_STORAGE_RECONCILE_INTERVAL", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Storage.ReconcileInterval }),
	field("server.storage.retry_max_attempts", "LINGUAFLOW_STORAGE_RETRY_MAX_ATTEMPTS", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Storage.RetryMaxAttempts }),
	field("server.storage.reconcile_batch_size", "LINGUAFLOW_STORAGE_RECONCILE_BATCH_SIZE", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Storage.ReconcileBatchSize }),
	field("server.host", "LINGUAFLOW_HOST", "string", "serve/local", false, func(r *ResolvedServer) *string { return &r.Config.Host }),
	field("server.port", "LINGUAFLOW_PORT", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Port }),
	field("server.data_dir", "LINGUAFLOW_DATA_DIR", "path", "serve/local", false, func(r *ResolvedServer) *string { return &r.Config.DataDir }),
	field("server.service_name", "LINGUAFLOW_SERVICE_NAME", "string", "serve/local", false, func(r *ResolvedServer) *string { return &r.Config.ServiceName }),
	field("server.auto_migrate", "LINGUAFLOW_AUTO_MIGRATE", "boolean", "serve/local", false, func(r *ResolvedServer) *bool { return &r.Config.AutoMigrate }),
	field("server.serve_ui", "LINGUAFLOW_SERVE_UI", "boolean", "serve/local", false, func(r *ResolvedServer) *bool { return &r.Config.ServeUI }),
	field("server.jwt_secret", "LINGUAFLOW_JWT_SECRET", "string", "serve/local", true, func(r *ResolvedServer) *string { return &r.Config.JWTSecret }),
	field("server.jwt_issuer", "LINGUAFLOW_JWT_ISSUER", "string", "serve/local", false, func(r *ResolvedServer) *string { return &r.Config.JWTIssuer }),
	field("server.jwt_expiry", "LINGUAFLOW_JWT_EXPIRY", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.JWTExpiry }),
	field("server.refresh_token_expiry", "LINGUAFLOW_REFRESH_TOKEN_EXPIRY", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.RefreshExpiry }),
	field("server.shutdown_timeout", "LINGUAFLOW_SHUTDOWN_TIMEOUT", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.ShutdownTimeout }),
	field("server.revision_retention", "LINGUAFLOW_REVISION_RETENTION", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.RevisionRetention }),
	field("server.database.driver", "LINGUAFLOW_DATABASE_DRIVER", "string", "serve", false, func(r *ResolvedServer) *string { return &r.Config.Database.Driver }),
	field("server.database.dsn", "LINGUAFLOW_DATABASE_DSN", "string", "serve", true, func(r *ResolvedServer) *string { return &r.Config.Database.DSN }),
	field("server.database.max_open_conns", "LINGUAFLOW_DATABASE_MAX_OPEN_CONNS", "integer", "serve", false, func(r *ResolvedServer) *int { return &r.Config.Database.MaxOpenConns }),
	field("server.database.max_idle_conns", "LINGUAFLOW_DATABASE_MAX_IDLE_CONNS", "integer", "serve", false, func(r *ResolvedServer) *int { return &r.Config.Database.MaxIdleConns }),
	field("server.database.conn_max_lifetime", "LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME", "duration", "serve", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Database.ConnMaxLifetime }),
	field("server.workers.translation.count", "LINGUAFLOW_WORKERS_TRANSLATION_COUNT", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Workers.Translation.Count }),
	field("server.workers.translation.queue_capacity", "LINGUAFLOW_WORKERS_TRANSLATION_QUEUE_CAPACITY", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Workers.Translation.QueueCapacity }),
	field("server.workers.sync.count", "LINGUAFLOW_WORKERS_SYNC_COUNT", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Workers.Sync.Count }),
	field("server.workers.sync.queue_capacity", "LINGUAFLOW_WORKERS_SYNC_QUEUE_CAPACITY", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Workers.Sync.QueueCapacity }),
	field("server.pipeline.max_inflight_weight_mb", "LINGUAFLOW_PIPELINE_MAX_INFLIGHT_WEIGHT_MB", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Pipeline.MaxInflightWeightMB }),
	field("server.pipeline.max_inflight_resources", "LINGUAFLOW_PIPELINE_MAX_INFLIGHT_RESOURCES", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Pipeline.MaxInflightResources }),
	field("server.pipeline.rss_limit_mb", "LINGUAFLOW_PIPELINE_RSS_LIMIT_MB", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Pipeline.RssLimitMB }),
	field("server.preview.max_concurrency", "LINGUAFLOW_PREVIEW_MAX_CONCURRENCY", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.Preview.MaxConcurrency }),
	field("server.preview.timeout", "LINGUAFLOW_PREVIEW_TIMEOUT", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Preview.Timeout }),
	field("server.preview.apply_token_ttl", "LINGUAFLOW_PREVIEW_APPLY_TOKEN_TTL", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.Preview.ApplyTokenTTL }),
	field("server.quick_translate.max_concurrency", "LINGUAFLOW_QUICK_TRANSLATE_MAX_CONCURRENCY", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.QuickTranslate.MaxConcurrency }),
	field("server.quick_translate.timeout", "LINGUAFLOW_QUICK_TRANSLATE_TIMEOUT", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.QuickTranslate.Timeout }),
	field("server.quick_translate.max_timeout", "LINGUAFLOW_QUICK_TRANSLATE_MAX_TIMEOUT", "duration", "serve/local", false, func(r *ResolvedServer) *time.Duration { return &r.Config.QuickTranslate.MaxTimeout }),
	field("server.sse.ring_buffer_capacity", "LINGUAFLOW_SSE_RING_BUFFER_CAPACITY", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.SSE.RingBufferCapacity }),
	field("server.sse.replay_batch_size", "LINGUAFLOW_SSE_REPLAY_BATCH_SIZE", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.SSE.ReplayBatchSize }),
	field("server.sse.max_replay_events", "LINGUAFLOW_SSE_MAX_REPLAY_EVENTS", "integer", "serve/local", false, func(r *ResolvedServer) *int { return &r.Config.SSE.MaxReplayEvents }),
	field("server.cors.allowed_origins", "LINGUAFLOW_CORS_ORIGINS", "strings", "serve/local", false, func(r *ResolvedServer) *[]string { return &r.Config.CORS.AllowedOrigins }),
	field("server.credentials.master_key", "LINGUAFLOW_CREDENTIALS_MASTER_KEY", "string", "serve/local", true, func(r *ResolvedServer) *string { return &r.masterKey }),
	field("server.credentials.keyring_file", "LINGUAFLOW_CREDENTIALS_KEYRING_FILE", "path", "serve/local", false, func(r *ResolvedServer) *string { return &r.Config.Credentials.KeyringFile }),
	field("log.level", "LINGUAFLOW_LOG_LEVEL", "string", "serve/local", false, func(r *ResolvedServer) *string { return &r.Log.Level }),
	field("log.format", "LINGUAFLOW_LOG_FORMAT", "string", "serve/local", false, func(r *ResolvedServer) *string { return &r.Log.Format }),
	field("bootstrap.registration_enabled", "LINGUAFLOW_BOOTSTRAP_REGISTRATION_ENABLED", "boolean", "serve/local", false, func(r *ResolvedServer) *bool { return &r.Bootstrap.RegistrationEnabled }),
	field("bootstrap.admin.username", "LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME", "string", "serve", false, func(r *ResolvedServer) *string { return &admin(r).Username }),
	field("bootstrap.admin.email", "LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL", "string", "serve", false, func(r *ResolvedServer) *string { return &admin(r).Email }),
	field("bootstrap.admin.password", "LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD", "string", "serve", true, func(r *ResolvedServer) *string { return &admin(r).Password }),
}

func DeploymentFields() []FieldInfo {
	fields := make([]FieldInfo, len(serverFields))
	for i, f := range serverFields {
		fields[i] = f.FieldInfo
	}
	return fields
}
