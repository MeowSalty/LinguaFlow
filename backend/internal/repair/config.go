package repair

import "math"

// Config 是修复策略的运行时配置。
type Config struct {
	Enabled              bool
	JSONStructural       bool
	SchemaAliases        bool
	Partial              bool
	PartialThreshold     float64
	PlaceholderNormalize bool
	PromptUpgrade        bool
}

// Normalize 规范化 Config：
//   - Enabled=false 时强制清零所有子开关
//   - PartialThreshold 不在 (0,1] 时归 0.5
func (c *Config) Normalize() {
	if !c.Enabled {
		c.JSONStructural = false
		c.SchemaAliases = false
		c.Partial = false
		c.PlaceholderNormalize = false
		c.PromptUpgrade = false
	}
	if c.PartialThreshold <= 0 || c.PartialThreshold > 1 || math.IsNaN(c.PartialThreshold) {
		c.PartialThreshold = 0.5
	}
}

// ToOptions 将 Config 转换为 repair.Options。
func (c Config) ToOptions() Options {
	return Options{
		JSONStructural:       c.JSONStructural,
		SchemaAliases:        c.SchemaAliases,
		Partial:              c.Partial,
		PartialThreshold:     c.PartialThreshold,
		PlaceholderNormalize: c.PlaceholderNormalize,
		PromptUpgrade:        c.PromptUpgrade,
	}
}
