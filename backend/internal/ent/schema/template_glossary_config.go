package schema

// 注意：此文件仅包含数据结构体定义，不再有独立的 ent.Schema。
// TemplateGlossaryConfigData 作为 field.JSON 内联到 TranslationTemplate 主表。

// TemplateGlossaryConfigData 是术语表配置的 JSON 存储结构。
type TemplateGlossaryConfigData struct {
	Enabled   bool                    `json:"enabled"   yaml:"enabled"`
	Bootstrap TemplateBootstrapConfig `json:"bootstrap" yaml:"bootstrap"`
}

// TemplateBootstrapConfig 术语自举配置。
type TemplateBootstrapConfig struct {
	Mode                   string `json:"mode"                     yaml:"mode"`
	Save                   bool   `json:"save"                     yaml:"save"`
	MaxTermsPerBatch       int    `json:"max_terms_per_batch"      yaml:"max_terms_per_batch"`
	MinSourceLen           int    `json:"min_source_len"           yaml:"min_source_len"`
	InlineConflictStrategy string `json:"inline_conflict_strategy" yaml:"inline_conflict_strategy"` // off / rewrite-local
}

// DefaultGlossaryConfig 返回默认的术语表配置。
func DefaultGlossaryConfig() TemplateGlossaryConfigData {
	return TemplateGlossaryConfigData{
		Enabled: false,
		Bootstrap: TemplateBootstrapConfig{
			Mode:                   "off",
			Save:                   true,
			MaxTermsPerBatch:       20,
			MinSourceLen:           2,
			InlineConflictStrategy: "rewrite-local",
		},
	}
}
