package schema

// 注意：此文件仅包含数据结构体定义，不包含独立的 ent.Schema。
// TranslationProfileConfigData 作为 field.JSON 内联到 TranslationProfile 主表。

// TranslationProfileConfigData 翻译配置模板的 JSON 存储结构。
type TranslationProfileConfigData struct {
	Split       ProfileSplitConfig       `json:"split"       yaml:"split"`
	Protect     ProfileProtectConfig     `json:"protect"     yaml:"protect"`
	Postprocess ProfilePostprocessConfig `json:"postprocess" yaml:"postprocess"`
	Repair      ProfileRepairConfig      `json:"repair"      yaml:"repair"`
	Glossary    ProfileGlossaryConfig    `json:"glossary"    yaml:"glossary"`
	Context     ProfileContextConfig     `json:"context"     yaml:"context"`
	Ruby        ProfileRubyConfig        `json:"ruby"        yaml:"ruby"`
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

// ProfileRubyConfig Ruby 注音保护配置。
type ProfileRubyConfig struct {
	Enabled       bool     `json:"enabled"       yaml:"enabled"`
	PreserveKinds []string `json:"preserve_kinds" yaml:"preserve_kinds"`
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

// ProfileGlossaryConfig 术语表配置。
type ProfileGlossaryConfig struct {
	Bootstrap ProfileBootstrapConfig `json:"bootstrap" yaml:"bootstrap"`
}

// ProfileBootstrapConfig 术语自举配置（仅内联自举相关）。
type ProfileBootstrapConfig struct {
	Enabled                bool    `json:"enabled"                  yaml:"enabled"`
	MaxTermsPer1000Chars   float64 `json:"max_terms_per_1000_chars" yaml:"max_terms_per_1000_chars"`
	MinSourceLen           int     `json:"min_source_len"           yaml:"min_source_len"`
	InlineConflictStrategy string  `json:"inline_conflict_strategy" yaml:"inline_conflict_strategy"`
}

// ProfileContextConfig 上下文窗口配置。
type ProfileContextConfig struct {
	Enabled  bool `json:"enabled"   yaml:"enabled"`
	Before   int  `json:"before"    yaml:"before"`
	After    int  `json:"after"     yaml:"after"`
	MaxChars int  `json:"max_chars" yaml:"max_chars"`
}

// DefaultProfileConfig 返回默认的翻译配置。
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
		Ruby: ProfileRubyConfig{Enabled: false, PreserveKinds: []string{"phonetic", "semantic", "creative"}},
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
		Glossary: ProfileGlossaryConfig{
			Bootstrap: ProfileBootstrapConfig{
				Enabled:                true,
				MaxTermsPer1000Chars:   3.0,
				MinSourceLen:           2,
				InlineConflictStrategy: "rewrite-local",
			},
		},
		Context: ProfileContextConfig{
			Enabled:  true,
			Before:   1,
			After:    1,
			MaxChars: 0,
		},
	}
}

// NormalizeContext 填充 Context 字段的默认值。
// 用于处理从数据库反序列化时缺少 context 字段的旧记录。
func (c *TranslationProfileConfigData) NormalizeContext() {
	if c.Context.Before < 1 {
		c.Context.Before = 1
	}
	if c.Context.After < 1 {
		c.Context.After = 1
	}
	// 零值 Enabled=false 且 Before/After 均为默认值时，视为未设置，回退到默认启用
	if !c.Context.Enabled && c.Context.MaxChars == 0 {
		c.Context.Enabled = true
	}
}

// NormalizePreserveKinds 填充 PreserveKinds 字段的默认值。
// 用于处理从数据库反序列化时缺少 preserve_kinds 字段的旧记录。
// nil 表示未设置（旧记录），回退到默认全集；
// 非 nil 空切片表示用户显式选择不保留任何注音，不做覆盖。
func (c *TranslationProfileConfigData) NormalizePreserveKinds() {
	if c.Ruby.PreserveKinds == nil {
		c.Ruby.PreserveKinds = []string{"phonetic", "semantic", "creative"}
	}
}
