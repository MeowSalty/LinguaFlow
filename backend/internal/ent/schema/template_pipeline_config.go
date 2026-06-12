package schema

// 注意：此文件仅包含数据结构体定义，不再有独立的 ent.Schema。
// TemplatePipelineConfigData 作为 field.JSON 内联到 TranslationTemplate 主表。

// TemplatePipelineConfigData 是管线配置的 JSON 存储结构。
// 字段结构与 config.PipelineConfig 的子集保持一致，便于直接转换。
type TemplatePipelineConfigData struct {
	Split       TemplateSplitConfig       `json:"split"       yaml:"split"`
	Protect     TemplateProtectConfig     `json:"protect"     yaml:"protect"`
	Retry       TemplateRetryConfig       `json:"retry"       yaml:"retry"`
	Repair      TemplateRepairConfig      `json:"repair"      yaml:"repair"`
	Postprocess TemplatePostprocessConfig `json:"postprocess" yaml:"postprocess"`
}

// TemplateSplitConfig 分割策略配置。
type TemplateSplitConfig struct {
	Enabled  bool   `json:"enabled"  yaml:"enabled"`
	Strategy string `json:"strategy" yaml:"strategy"`
	MaxChars int    `json:"max_chars" yaml:"max_chars"`
}

// TemplateProtectConfig 保护规则配置。
type TemplateProtectConfig struct {
	Enabled bool     `json:"enabled" yaml:"enabled"`
	Rules   []string `json:"rules"   yaml:"rules"`
}

// TemplateRetryConfig 重试策略配置。
// BackoffMs 使用 int 毫秒，避免 time.Duration 在 JSON/SQLite 中的序列化歧义。
type TemplateRetryConfig struct {
	MaxAttempts int  `json:"max_attempts" yaml:"max_attempts"`
	BackoffMs   int  `json:"backoff_ms"   yaml:"backoff_ms"`
	Jitter      bool `json:"jitter"       yaml:"jitter"`
}

// TemplateRepairConfig 修复策略配置。
type TemplateRepairConfig struct {
	Enabled              bool    `json:"enabled"               yaml:"enabled"`
	JSONStructural       bool    `json:"json_structural"       yaml:"json_structural"`
	SchemaAliases        bool    `json:"schema_aliases"        yaml:"schema_aliases"`
	Partial              bool    `json:"partial"               yaml:"partial"`
	PartialThreshold     float64 `json:"partial_threshold"     yaml:"partial_threshold"`
	PlaceholderNormalize bool    `json:"placeholder_normalize" yaml:"placeholder_normalize"`
	PromptUpgrade        bool    `json:"prompt_upgrade"        yaml:"prompt_upgrade"`
}

// TemplatePostprocessConfig 后处理配置。
type TemplatePostprocessConfig struct {
	Enabled    bool `json:"enabled"     yaml:"enabled"`
	TrimSpaces bool `json:"trim_spaces" yaml:"trim_spaces"`
}

// DefaultPipelineConfig 返回默认的管线配置。
func DefaultPipelineConfig() TemplatePipelineConfigData {
	return TemplatePipelineConfigData{
		Split: TemplateSplitConfig{
			Enabled:  true,
			Strategy: "paragraph",
			MaxChars: 1200,
		},
		Protect: TemplateProtectConfig{
			Enabled: true,
			Rules:   []string{"code", "link", "placeholder", "xml"},
		},
		Retry: TemplateRetryConfig{
			MaxAttempts: 3,
			BackoffMs:   2000,
			Jitter:      true,
		},
		Repair: TemplateRepairConfig{
			Enabled:              true,
			JSONStructural:       true,
			SchemaAliases:        true,
			Partial:              true,
			PartialThreshold:     0.5,
			PlaceholderNormalize: true,
			PromptUpgrade:        true,
		},
		Postprocess: TemplatePostprocessConfig{
			Enabled:    true,
			TrimSpaces: true,
		},
	}
}
