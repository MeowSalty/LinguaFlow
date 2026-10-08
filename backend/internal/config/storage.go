package config

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/diskspace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storagenet"
	"gopkg.in/yaml.v3"
)

// StorageConfig 描述部署侧的存储设施。用户策略与项目绑定属于持久化的
// 领域数据，绝不作为部署配置覆盖项。
type StorageConfig struct {
	Enabled            bool                   `yaml:"enabled"`
	Maintenance        bool                   `yaml:"maintenance"`
	Backends           []StorageBackendConfig `yaml:"backends"`
	DefaultSiteSpace   string                 `yaml:"default_site_space"`
	WorkDir            string                 `yaml:"work_dir"`
	CacheDir           string                 `yaml:"cache_dir"`
	Limits             StorageLimits          `yaml:"limits"`
	Initialization     StorageInitialization  `yaml:"initialization"`
	Disk               StorageDiskConfig      `yaml:"disk"`
	Network            StorageNetworkConfig   `yaml:"network"`
	IntentTTL          time.Duration          `yaml:"intent_ttl"`
	MetadataTimeout    time.Duration          `yaml:"metadata_timeout"`
	TransferTimeout    time.Duration          `yaml:"transfer_timeout"`
	IdleTimeout        time.Duration          `yaml:"idle_timeout"`
	RetryMaxAttempts   int                    `yaml:"retry_max_attempts"`
	RetryWindow        time.Duration          `yaml:"retry_window"`
	RetryBaseDelay     time.Duration          `yaml:"retry_base_delay"`
	RetryMaxDelay      time.Duration          `yaml:"retry_max_delay"`
	SignedURLTTL       time.Duration          `yaml:"signed_url_ttl"`
	SignedURLMaxTTL    time.Duration          `yaml:"signed_url_max_ttl"`
	SourceRetention    time.Duration          `yaml:"source_retention"`
	DeletionGrace      time.Duration          `yaml:"deletion_grace"`
	ReconcileInterval  time.Duration          `yaml:"reconcile_interval"`
	ReconcileBatchSize int                    `yaml:"reconcile_batch_size"`
}

type StorageBackendConfig struct {
	ID              string `yaml:"id" json:"id"`
	Driver          string `yaml:"driver" json:"driver"`
	Root            string `yaml:"root" json:"root"`
	Endpoint        string `yaml:"endpoint" json:"endpoint"`
	Region          string `yaml:"region" json:"region"`
	Bucket          string `yaml:"bucket" json:"bucket"`
	Prefix          string `yaml:"prefix" json:"prefix"`
	PathStyle       bool   `yaml:"path_style" json:"path_style"`
	AccessKeyID     string `yaml:"access_key_id" json:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key" json:"secret_access_key"`
	SessionToken    string `yaml:"session_token" json:"session_token"`
}

func (StorageBackendConfig) String() string   { return "storage backend (redacted)" }
func (StorageBackendConfig) GoString() string { return "storage backend (redacted)" }

type StorageLimits struct {
	MaxFileBytes      int64 `yaml:"max_file_bytes"`
	MaxTempBytes      int64 `yaml:"max_temp_bytes"`
	MaxOutputBytes    int64 `yaml:"max_output_bytes"`
	MaxExpandedBytes  int64 `yaml:"max_expanded_bytes"`
	MaxArchiveEntries int   `yaml:"max_archive_entries"`
	MaxSegments       int   `yaml:"max_segments"`
	MaxMetadataBytes  int64 `yaml:"max_metadata_bytes"`
	MaxConcurrency    int   `yaml:"max_concurrency"`
	MaxCacheBytes     int64 `yaml:"max_cache_bytes"`
}

const MaxQuotaBytes int64 = 9007199254740991

// QuotaInput distinguishes an omitted initialization setting from explicit unlimited.
type QuotaInput struct {
	Set   bool
	Value *int64
}

func (q QuotaInput) String() string {
	if !q.Set {
		return "not supplied"
	}
	if q.Value == nil {
		return "null (unlimited)"
	}
	return strconv.FormatInt(*q.Value, 10)
}

type StorageInitialization struct {
	CapacityBytes     QuotaInput `yaml:"capacity_bytes"`
	LogicalLimitBytes QuotaInput `yaml:"logical_limit_bytes"`
}

type StorageDiskConfig struct {
	MinimumFree string `yaml:"minimum_free"`
}

func parseQuotaInput(value string) (QuotaInput, error) {
	if value == "null" {
		return QuotaInput{Set: true}, nil
	}
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return QuotaInput{}, fmt.Errorf("must be null or a positive decimal integer no greater than %d", MaxQuotaBytes)
	}
	v, err := strconv.ParseInt(value, 10, 64)
	if err != nil || v <= 0 || v > MaxQuotaBytes {
		return QuotaInput{}, fmt.Errorf("must be null or a positive decimal integer no greater than %d", MaxQuotaBytes)
	}
	return QuotaInput{Set: true, Value: &v}, nil
}

type StorageNetworkConfig struct {
	AllowedHosts []string `yaml:"allowed_hosts"`
	AllowedCIDRs []string `yaml:"allowed_cidrs"`
}

func DefaultStorageConfig() StorageConfig {
	return StorageConfig{
		DefaultSiteSpace: "local",
		Limits:           StorageLimits{MaxFileBytes: 100 << 20, MaxTempBytes: 4 << 30, MaxOutputBytes: 512 << 20, MaxExpandedBytes: 1 << 30, MaxArchiveEntries: 10000, MaxSegments: 100000, MaxMetadataBytes: 64 << 20, MaxConcurrency: 2},
		Disk:             StorageDiskConfig{MinimumFree: "1%"},
		IntentTTL:        24 * time.Hour, MetadataTimeout: 30 * time.Second, TransferTimeout: 15 * time.Minute, IdleTimeout: 30 * time.Second,
		RetryMaxAttempts: 8, RetryWindow: 30 * time.Minute, RetryBaseDelay: time.Second, RetryMaxDelay: time.Minute,
		SignedURLTTL: 5 * time.Minute, SignedURLMaxTTL: 15 * time.Minute, SourceRetention: 30 * 24 * time.Hour,
		DeletionGrace: 24 * time.Hour, ReconcileInterval: time.Minute, ReconcileBatchSize: 100,
	}
}

func (c StorageConfig) NetworkPolicy(bucket string) storagenet.Policy {
	return storagenet.Policy{AllowedHosts: append([]string(nil), c.Network.AllowedHosts...), AllowedCIDRs: append([]string(nil), c.Network.AllowedCIDRs...), Bucket: bucket, HeaderTimeout: c.MetadataTimeout, IdleTimeout: c.IdleTimeout, TransferTimeout: c.TransferTimeout}
}

func (c StorageConfig) Validate() error {
	for key, q := range map[string]QuotaInput{"capacity_bytes": c.Initialization.CapacityBytes, "logical_limit_bytes": c.Initialization.LogicalLimitBytes} {
		if q.Value != nil && (!q.Set || *q.Value <= 0 || *q.Value > MaxQuotaBytes) {
			return fmt.Errorf("server.storage.initialization.%s must be null or a positive integer no greater than %d", key, MaxQuotaBytes)
		}
	}
	if _, err := diskspace.ParseThreshold(c.Disk.MinimumFree); err != nil {
		return fmt.Errorf("server.storage.disk.minimum_free: %w", err)
	}
	for key, value := range map[string]time.Duration{
		"intent_ttl": c.IntentTTL, "metadata_timeout": c.MetadataTimeout, "transfer_timeout": c.TransferTimeout, "idle_timeout": c.IdleTimeout,
		"retry_window": c.RetryWindow, "retry_base_delay": c.RetryBaseDelay, "retry_max_delay": c.RetryMaxDelay,
		"signed_url_ttl": c.SignedURLTTL, "signed_url_max_ttl": c.SignedURLMaxTTL, "source_retention": c.SourceRetention,
		"deletion_grace": c.DeletionGrace, "reconcile_interval": c.ReconcileInterval,
	} {
		if value <= 0 {
			return fmt.Errorf("server.storage.%s must be positive", key)
		}
	}
	if c.MetadataTimeout > c.TransferTimeout || c.IdleTimeout > c.TransferTimeout || c.TransferTimeout > c.RetryWindow || c.RetryWindow > c.IntentTTL || c.RetryBaseDelay > c.RetryMaxDelay || c.RetryMaxDelay > c.RetryWindow || c.SignedURLTTL > c.SignedURLMaxTTL || c.SignedURLMaxTTL > c.DeletionGrace {
		return fmt.Errorf("server.storage time limits are inconsistent")
	}
	if c.RetryMaxAttempts <= 0 || c.ReconcileBatchSize <= 0 {
		return fmt.Errorf("server.storage retry and reconciliation counts must be positive")
	}
	for key, value := range map[string]int64{"max_file_bytes": c.Limits.MaxFileBytes, "max_temp_bytes": c.Limits.MaxTempBytes, "max_output_bytes": c.Limits.MaxOutputBytes, "max_expanded_bytes": c.Limits.MaxExpandedBytes, "max_metadata_bytes": c.Limits.MaxMetadataBytes} {
		if value <= 0 {
			return fmt.Errorf("server.storage.limits.%s must be positive", key)
		}
	}
	if c.Limits.MaxArchiveEntries <= 0 || c.Limits.MaxSegments <= 0 || c.Limits.MaxConcurrency <= 0 || c.Limits.MaxCacheBytes < 0 {
		return fmt.Errorf("server.storage.limits counts must be positive and cache size nonnegative")
	}
	if c.Limits.MaxFileBytes > c.Limits.MaxTempBytes || c.Limits.MaxOutputBytes > c.Limits.MaxTempBytes || c.Limits.MaxMetadataBytes > c.Limits.MaxTempBytes {
		return fmt.Errorf("server.storage.limits temporary capacity is smaller than a processing limit")
	}
	if err := storagenet.ValidatePolicy(c.NetworkPolicy("")); err != nil {
		return fmt.Errorf("server.storage.network contains an invalid target policy")
	}
	seen := map[string]bool{}
	for i, b := range c.Backends {
		if b.ID == "" || strings.TrimSpace(b.ID) != b.ID || seen[b.ID] {
			return fmt.Errorf("server.storage.backends[%d].id must be unique and nonempty", i)
		}
		seen[b.ID] = true
		switch b.Driver {
		case "local":
			if strings.TrimSpace(b.Root) == "" || b.Endpoint != "" || b.Bucket != "" || b.AccessKeyID != "" || b.SecretAccessKey != "" || b.SessionToken != "" {
				return fmt.Errorf("server.storage.backends[%d] local backend requires root and cannot contain remote credentials", i)
			}
		case "s3":
			if _, err := storagenet.NormalizeEndpoint(b.Endpoint); err != nil {
				return fmt.Errorf("server.storage.backends[%d].endpoint must be an HTTPS origin without secrets", i)
			}
			if strings.TrimSpace(b.Bucket) == "" || strings.TrimSpace(b.Region) == "" || b.Root != "" || strings.TrimSpace(b.AccessKeyID) == "" || strings.TrimSpace(b.SecretAccessKey) == "" {
				return fmt.Errorf("server.storage.backends[%d] requires bucket, region and explicit credentials", i)
			}
			if strings.ContainsAny(b.Prefix, "\\\x00") || strings.HasPrefix(b.Prefix, "/") {
				return fmt.Errorf("server.storage.backends[%d].prefix is invalid", i)
			}
			for _, part := range strings.Split(b.Prefix, "/") {
				if part == ".." || part == "." {
					return fmt.Errorf("server.storage.backends[%d].prefix is invalid", i)
				}
			}
		default:
			return fmt.Errorf("server.storage.backends[%d].driver must be local or s3", i)
		}
	}
	if strings.TrimSpace(c.DefaultSiteSpace) == "" || (len(c.Backends) > 0 && !seen[c.DefaultSiteSpace]) {
		return fmt.Errorf("server.storage.default_site_space must identify a configured backend")
	}
	return nil
}

// Backends 是一个显式注册的复合字段。每个子项的名称与标量类型仍会
// 校验；未知键绝不通过 options 映射透传。
func parseStorageBackends(n *yaml.Node, env map[string]string) ([]StorageBackendConfig, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("must be a backend array")
	}
	backends := make([]StorageBackendConfig, 0, len(n.Content))
	for _, child := range n.Content {
		if child.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("backends must be objects")
		}
		var b StorageBackendConfig
		seen := map[string]bool{}
		for i := 0; i < len(child.Content); i += 2 {
			key, node := child.Content[i], child.Content[i+1]
			if key.Tag != "!!str" || seen[key.Value] {
				return nil, fmt.Errorf("invalid or duplicate backend field")
			}
			seen[key.Value] = true
			if key.Value == "path_style" {
				v, err := parseYAMLField(serverField{FieldInfo: FieldInfo{Type: "boolean"}}, node, env)
				if err != nil {
					return nil, fmt.Errorf("backend path_style must be boolean")
				}
				b.PathStyle = v.(bool)
				continue
			}
			var target *string
			switch key.Value {
			case "id":
				target = &b.ID
			case "driver":
				target = &b.Driver
			case "root":
				target = &b.Root
			case "endpoint":
				target = &b.Endpoint
			case "region":
				target = &b.Region
			case "bucket":
				target = &b.Bucket
			case "prefix":
				target = &b.Prefix
			case "access_key_id":
				target = &b.AccessKeyID
			case "secret_access_key":
				target = &b.SecretAccessKey
			case "session_token":
				target = &b.SessionToken
			default:
				return nil, fmt.Errorf("unknown storage backend field")
			}
			v, err := parseYAMLField(serverField{FieldInfo: FieldInfo{Type: "string"}}, node, env)
			if err != nil {
				return nil, fmt.Errorf("storage backend field must be a string with valid references")
			}
			*target = v.(string)
		}
		backends = append(backends, b)
	}
	return backends, nil
}

func parseStorageBackendsEnvironment(value string) ([]StorageBackendConfig, error) {
	var n yaml.Node
	d := yaml.NewDecoder(strings.NewReader(value))
	if err := d.Decode(&n); err != nil || len(n.Content) != 1 {
		return nil, fmt.Errorf("must be a backend array")
	}
	if err := d.Decode(&yaml.Node{}); err != io.EOF {
		return nil, fmt.Errorf("must contain one backend array")
	}
	return parseStorageBackends(n.Content[0], map[string]string{})
}

func absoluteBackendRoots(value []StorageBackendConfig, base string) []StorageBackendConfig {
	for i := range value {
		if value[i].Root != "" {
			value[i].Root = absolutePath(value[i].Root, base)
		}
	}
	return value
}

func resolveStorageDirectories(c *ServerConfig, values map[string]sourcedValue) {
	if _, ok := values["server.storage.work_dir"]; !ok {
		c.Storage.WorkDir = filepath.Join(c.DataDir, "tmp")
	}
	if _, ok := values["server.storage.cache_dir"]; !ok {
		c.Storage.CacheDir = filepath.Join(c.DataDir, "cache")
	}
}
