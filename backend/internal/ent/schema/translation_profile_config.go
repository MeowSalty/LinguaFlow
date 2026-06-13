package schema

// 注意：此文件仅包含数据结构体定义，不包含独立的 ent.Schema。
// TranslationProfileConfigData 作为 field.JSON 内联到 TranslationProfile 主表。

// TranslationProfileConfigData 翻译配置模板的 JSON 存储结构。
type TranslationProfileConfigData struct {
	Split       ProfileSplitConfig       `json:"split"       yaml:"split"`
	Protect     ProfileProtectConfig     `json:"protect"     yaml:"protect"`
	Postprocess ProfilePostprocessConfig `json:"postprocess" yaml:"postprocess"`
	Repair      ProfileRepairConfig      `json:"repair"      yaml:"repair"`
	Rounds      []ProfileRoundConfig     `json:"rounds"      yaml:"rounds"`
	Glossary    ProfileGlossaryConfig    `json:"glossary"    yaml:"glossary"`
}

// ProfileRoundConfig 单轮翻译的执行策略。
// BackendOrder 使用后端 ID，运行时根据 scope 解析。
type ProfileRoundConfig struct {
	BatchSize       int                `json:"batch_size"       yaml:"batch_size"`
	Concurrency     int                `json:"concurrency"      yaml:"concurrency"`
	FallbackShrink  float64            `json:"fallback_shrink"  yaml:"fallback_shrink"`
	RateLimitPerSec int                `json:"rate_limit_per_sec" yaml:"rate_limit_per_sec"`
	BackendMode     string             `json:"backend_mode"     yaml:"backend_mode"`
	BackendOrder    []int              `json:"backend_order"    yaml:"backend_order"`
	Retry           ProfileRetryConfig `json:"retry"            yaml:"retry"`
}

// ProfileSplitConfig 分割策略配置。
type ProfileSplitConfig struct {
	Enabled  bool   `json:"enabled"  yaml:"enabled"`
	Strategy string `json:"strategy" yaml:"strategy"`
	MaxChars int    `json:"max_chars" yaml:"max_chars"`
}

// ProfileProtectConfig 保护规则配置。
type ProfileProtectConfig struct {
	Enabled bool     `json:"enabled" yaml:"enabled"`
	Rules   []string `json:"rules"   yaml:"rules"`
}

// ProfilePostprocessConfig 后处理配置。
type ProfilePostprocessConfig struct {
	Enabled    bool `json:"enabled"     yaml:"enabled"`
	TrimSpaces bool `json:"trim_spaces" yaml:"trim_spaces"`
}

// ProfileRepairConfig 修复策略配置。
type ProfileRepairConfig struct {
	Enabled              bool    `json:"enabled"               yaml:"enabled"`
	JSONStructural       bool    `json:"json_structural"       yaml:"json_structural"`
	SchemaAliases        bool    `json:"schema_aliases"        yaml:"schema_aliases"`
	Partial              bool    `json:"partial"               yaml:"partial"`
	PartialThreshold     float64 `json:"partial_threshold"     yaml:"partial_threshold"`
	PlaceholderNormalize bool    `json:"placeholder_normalize" yaml:"placeholder_normalize"`
	PromptUpgrade        bool    `json:"prompt_upgrade"        yaml:"prompt_upgrade"`
}

// ProfileRetryConfig 重试策略配置。
// BackoffMs 使用 int 毫秒，避免 time.Duration 在 JSON/SQLite 中的序列化歧义。
type ProfileRetryConfig struct {
	MaxAttempts int  `json:"max_attempts" yaml:"max_attempts"`
	BackoffMs   int  `json:"backoff_ms"   yaml:"backoff_ms"`
	Jitter      bool `json:"jitter"       yaml:"jitter"`
}

// ProfileGlossaryConfig 术语表配置。
type ProfileGlossaryConfig struct {
	Enabled   bool                   `json:"enabled"   yaml:"enabled"`
	Bootstrap ProfileBootstrapConfig `json:"bootstrap" yaml:"bootstrap"`
}

// ProfileBootstrapConfig 术语自举配置。
type ProfileBootstrapConfig struct {
	Mode                   string `json:"mode"                     yaml:"mode"`
	Save                   bool   `json:"save"                     yaml:"save"`
	MaxTermsPerBatch       int    `json:"max_terms_per_batch"      yaml:"max_terms_per_batch"`
	MinSourceLen           int    `json:"min_source_len"           yaml:"min_source_len"`
	InlineConflictStrategy string `json:"inline_conflict_strategy" yaml:"inline_conflict_strategy"`
}

// DefaultProfileConfig 返回默认的翻译配置（单轮模式）。
func DefaultProfileConfig() TranslationProfileConfigData {
	return TranslationProfileConfigData{
		Split: ProfileSplitConfig{
			Enabled:  true,
			Strategy: "paragraph",
			MaxChars: 1200,
		},
		Protect: ProfileProtectConfig{
			Enabled: true,
			Rules:   []string{"code", "link", "placeholder", "xml"},
		},
		Postprocess: ProfilePostprocessConfig{
			Enabled:    true,
			TrimSpaces: true,
		},
		Repair: ProfileRepairConfig{
			Enabled:              true,
			JSONStructural:       true,
			SchemaAliases:        true,
			Partial:              true,
			PartialThreshold:     0.5,
			PlaceholderNormalize: true,
			PromptUpgrade:        true,
		},
		Rounds: []ProfileRoundConfig{
			{
				BatchSize:       10,
				Concurrency:     3,
				FallbackShrink:  0.5,
				RateLimitPerSec: 0,
				BackendMode:     "prepend",
				BackendOrder:    []int{},
				Retry: ProfileRetryConfig{
					MaxAttempts: 3,
					BackoffMs:   2000,
					Jitter:      true,
				},
			},
		},
		Glossary: ProfileGlossaryConfig{
			Enabled: false,
			Bootstrap: ProfileBootstrapConfig{
				Mode:                   "off",
				Save:                   true,
				MaxTermsPerBatch:       20,
				MinSourceLen:           2,
				InlineConflictStrategy: "rewrite-local",
			},
		},
	}
}
