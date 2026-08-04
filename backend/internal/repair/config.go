package repair

// Config 是修复策略的运行时配置。
type Config struct {
	Enabled              bool
	JSONStructural       bool
	SchemaAliases        bool
	PlaceholderNormalize bool
	PromptUpgrade        bool
}

// Normalize 规范化 Config：
//   - Enabled=false 时强制清零所有子开关
func (c *Config) Normalize() {
	if !c.Enabled {
		c.JSONStructural = false
		c.SchemaAliases = false
		c.PlaceholderNormalize = false
		c.PromptUpgrade = false
	}
}

// ToOptions 将 Config 转换为 repair.Options。
func (c Config) ToOptions() Options {
	return Options{
		JSONStructural:       c.JSONStructural,
		SchemaAliases:        c.SchemaAliases,
		PlaceholderNormalize: c.PlaceholderNormalize,
		PromptUpgrade:        c.PromptUpgrade,
	}
}
