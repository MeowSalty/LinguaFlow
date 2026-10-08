package execution

// 注意：此文件仅包含数据结构体定义，不包含独立的 ent.Schema。
// ProfileSpec 作为 field.JSON 内联到 ExecutionProfile 主表。

// ProfileSpec 执行策略配置的 JSON 存储结构。
type ProfileSpec struct {
	SchemaVersion int                      `json:"schema_version" yaml:"schema_version"`
	Protect       ProfileProtectConfig     `json:"protect"     yaml:"protect"`
	Postprocess   ProfilePostprocessConfig `json:"postprocess" yaml:"postprocess"`
	Repair        ProfileRepairConfig      `json:"repair"      yaml:"repair"`
	Context       ProfileContextConfig     `json:"context"     yaml:"context"`
	Ruby          ProfileRubyConfig        `json:"ruby"        yaml:"ruby"`
	QA            ProfileQAConfig          `json:"qa"          yaml:"qa"`
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
	Enabled              bool `json:"enabled"               yaml:"enabled"`
	JSONStructural       bool `json:"json_structural"       yaml:"json_structural"`
	SchemaAliases        bool `json:"schema_aliases"        yaml:"schema_aliases"`
	PlaceholderNormalize bool `json:"placeholder_normalize" yaml:"placeholder_normalize"`
	PromptUpgrade        bool `json:"prompt_upgrade"        yaml:"prompt_upgrade"`
}

// ProfileContextConfig 上下文窗口配置。
type ProfileContextConfig struct {
	Enabled  bool `json:"enabled"   yaml:"enabled"`
	Before   int  `json:"before"    yaml:"before"`
	After    int  `json:"after"     yaml:"after"`
	MaxChars int  `json:"max_chars" yaml:"max_chars"`
}

// ProfileQAConfig 质量检测配置。
type ProfileQAConfig struct {
	Enabled        bool     `json:"enabled"          yaml:"enabled"`
	AutoReject     bool     `json:"auto_reject"      yaml:"auto_reject"`
	Checks         []string `json:"checks" yaml:"checks"`
	LengthMethod   string   `json:"length_method"    yaml:"length_method"`
	LengthRatioMin float64  `json:"length_ratio_min" yaml:"length_ratio_min"`
	LengthRatioMax float64  `json:"length_ratio_max" yaml:"length_ratio_max"`
}

// DefaultProfileConfig 返回默认的执行策略配置。
func DefaultProfile() ProfileSpec {
	return ProfileSpec{
		SchemaVersion: 1,
		Protect: ProfileProtectConfig{
			Enabled: true,
			Rules:   []string{"code", "link", "placeholder", "xml"},
		},
		Ruby: ProfileRubyConfig{Enabled: true, PreserveKinds: []string{"creative"}},
		Postprocess: ProfilePostprocessConfig{
			Enabled:    true,
			TrimSpaces: true,
		},
		Repair: ProfileRepairConfig{
			Enabled:              true,
			JSONStructural:       true,
			SchemaAliases:        true,
			PlaceholderNormalize: true,
			PromptUpgrade:        true,
		},
		Context: ProfileContextConfig{
			Enabled:  true,
			Before:   1,
			After:    1,
			MaxChars: 0,
		},
		QA: ProfileQAConfig{
			Enabled:        false,
			LengthMethod:   "char_weight",
			LengthRatioMin: 0.2,
			LengthRatioMax: 3.0,
		},
	}
}
