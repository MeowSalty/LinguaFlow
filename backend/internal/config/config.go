package config

import (
	"fmt"
	"math"
	"time"
)

// Config 是 LinguaFlow 的根配置。字段顺序与 linguaflow.example.yaml 对齐。
type Config struct {
	Version    int    `yaml:"version"`
	SourceLang string `yaml:"source_lang"`
	TargetLang string `yaml:"target_lang"`

	Backends []BackendConfig `yaml:"backends"`

	Pipeline PipelineConfig `yaml:"pipeline"`
	Prompt   PromptConfig   `yaml:"prompt"`

	Glossary          GlossaryConfig `yaml:"glossary"`
	TranslationMemory TMConfig       `yaml:"translation_memory"`
	Plugins           PluginsConfig  `yaml:"plugins"`

	Output OutputConfig `yaml:"output"`
	Log    LogConfig    `yaml:"log"`
}

type BackendConfig struct {
	Name     string         `yaml:"name"`
	Type     string         `yaml:"type"`
	Enabled  bool           `yaml:"enabled"`
	Priority int            `yaml:"priority"`
	Options  map[string]any `yaml:"options"`
}

type PipelineConfig struct {
	Split       SplitConfig       `yaml:"split"`
	Protect     ProtectConfig     `yaml:"protect"`
	Translate   TranslateConfig   `yaml:"translate"`
	Postprocess PostprocessConfig `yaml:"postprocess"`
}

type SplitConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Strategy string `yaml:"strategy"`
	MaxChars int    `yaml:"max_chars"`
}

type ProtectConfig struct {
	Enabled bool     `yaml:"enabled"`
	Rules   []string `yaml:"rules"`
}

type TranslateConfig struct {
	Concurrency     int          `yaml:"concurrency"`
	BatchSize       int          `yaml:"batch_size"`      // 一次合并发送的段数；<=1 表示禁用批量
	FallbackShrink  float64      `yaml:"fallback_shrink"` // 整批失败时下一级 batch = floor(cur*shrink)；0 = 直接降到单段；必须 <1
	RateLimitPerSec int          `yaml:"rate_limit_per_sec"`
	BackendMode     string       `yaml:"backend_mode"`
	BackendOrder    []string     `yaml:"backend_order"`
	Retry           RetryConfig  `yaml:"retry"`
	Repair          RepairConfig `yaml:"repair"`
}

type RetryConfig struct {
	MaxAttempts int           `yaml:"max_attempts"`
	Backoff     time.Duration `yaml:"backoff"`
}

// RepairConfig 控制 LLM 响应解析失败 / 部分缺失时的"主动修复"行为。
//
// 各子开关默认开启（见 Default）；Enabled=false 时强制全部清零，调用方可一键关闭。
// 修复算子无错时是 no-op，对正常响应零成本；主要受益场景是 Anthropic Tool Use 模拟、
// Google 等非 strict JSON Schema 后端。
type RepairConfig struct {
	Enabled              bool    `yaml:"enabled"`
	JSONStructural       bool    `yaml:"json_structural"`       // L1: BOM 剥离、多对象合并、尾随逗号、控制字符、括号补齐
	SchemaAliases        bool    `yaml:"schema_aliases"`        // L2: translation/result/output/data.translations 同义化为 translations
	Partial              bool    `yaml:"partial"`               // L2: 部分 ID 缺失时仅对缺失段重试，而非整批 shrink
	PartialThreshold     float64 `yaml:"partial_threshold"`     // (0,1]; 缺失率 ≥ 阈值时仍走 shrink，避免单段爆炸
	PlaceholderNormalize bool    `yaml:"placeholder_normalize"` // L3: 占位符大小写/下划线变体归一（仅 normalize 已知 key 的变体）
	PromptUpgrade        bool    `yaml:"prompt_upgrade"`        // L4: 解析失败或占位符仍缺失时附加反例 reminder 重试一次
}

type PostprocessConfig struct {
	Enabled    bool `yaml:"enabled"`
	TrimSpaces bool `yaml:"trim_spaces"`
}

type PromptConfig struct {
	SystemTemplate string         `yaml:"system_template"`
	UserTemplate   string         `yaml:"user_template"`
	Vars           map[string]any `yaml:"vars"`
}

type GlossaryConfig struct {
	Enabled   bool            `yaml:"enabled"`
	Path      string          `yaml:"path"`
	Bootstrap BootstrapConfig `yaml:"bootstrap"`
}

// BootstrapConfig 控制术语自举：用 LLM 抽取并翻译领域术语，写入运行时 Glossary。
//
// Mode 选择策略：
//   - off：关闭自举（默认）。
//   - pre：translate 之前独立扫一遍文档，整篇翻译都能用上抽到的术语；多一次 LLM 调用。
//   - inline：translate 的 LLM 调用顺带返回术语；只对后续 batch 生效，省一次扫描。
//
// 任一非 off 模式都隐含要求 Glossary.Enabled=true，Validate 会自动设上。
// Save 控制结束后是否把增量回写到 Glossary.Path；off 模式下没有 dirty 自然不会写。
//
// InlineConflictStrategy 仅 inline 模式下生效，控制并发 worker 给同一 source 提交不同
// target 时的处理方式：First-Wins 保证全局术语表里只保留先到的版本，但后到 worker 的
// 译文已经用了被丢弃的版本，会导致文档内同一术语翻译不一致。
//   - rewrite-local（默认）：后到 worker 把本批译文里自己用的 target 字面值替换为
//     权威表中的版本；CJK 直接替换，拉丁系按词边界，歧义场景仅 Warn 不动。
//   - off：完全不处理，沿用旧行为（First-Wins + 不一致译文）。
type BootstrapConfig struct {
	Mode                   string   `yaml:"mode"`
	Save                   bool     `yaml:"save"`
	MaxTermsPerBatch       int      `yaml:"max_terms_per_batch"`
	MinSourceLen           int      `yaml:"min_source_len"`
	BackendMode            string   `yaml:"backend_mode"`
	BackendOrder           []string `yaml:"backend_order"`
	InlineConflictStrategy string   `yaml:"inline_conflict_strategy"`
}

// Bootstrap 模式常量。
const (
	BootstrapModeOff    = "off"
	BootstrapModePre    = "pre"
	BootstrapModeInline = "inline"
)

// Inline 模式下并发术语冲突的处理策略。
const (
	InlineConflictOff          = "off"
	InlineConflictRewriteLocal = "rewrite-local"
)

const (
	BackendModePrepend  = "prepend"
	BackendModeRestrict = "restrict"
)

// normalize 规范化 RepairConfig：
//   - Enabled=false 时强制清零所有子开关，调用方据此短路所有修复逻辑
//   - PartialThreshold 不在 (0,1] 时归 0.5（最常见默认）
func (r *RepairConfig) normalize() {
	if !r.Enabled {
		r.JSONStructural = false
		r.SchemaAliases = false
		r.Partial = false
		r.PlaceholderNormalize = false
		r.PromptUpgrade = false
	}
	if r.PartialThreshold <= 0 || r.PartialThreshold > 1 || math.IsNaN(r.PartialThreshold) {
		r.PartialThreshold = 0.5
	}
}

type TMConfig struct {
	Enabled bool   `yaml:"enabled"`
	Driver  string `yaml:"driver"`
	DSN     string `yaml:"dsn"`
}

type PluginsConfig struct {
	Enabled bool     `yaml:"enabled"`
	Scripts []string `yaml:"scripts"`
}

type OutputConfig struct {
	Mode              string `yaml:"mode"`
	PreserveExtension bool   `yaml:"preserve_extension"`
	Incremental       bool   `yaml:"incremental"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Default 返回内置默认配置。loader 在解析 yaml 前以此为基底合并。
func Default() *Config {
	return &Config{
		Version:    1,
		SourceLang: "auto",
		TargetLang: "zh",
		Backends: []BackendConfig{{
			Name:     "openai-default",
			Type:     "openai",
			Enabled:  true,
			Priority: 100,
			Options: map[string]any{
				"api_key":         "${OPENAI_API_KEY}",
				"base_url":        "https://api.openai.com/v1",
				"model":           "gpt-4o-mini",
				"temperature":     0.2,
				"max_tokens":      0,
				"timeout":         "60s",
				"response_format": "json_schema",
			},
		}},
		Pipeline: PipelineConfig{
			Split:   SplitConfig{Enabled: true, Strategy: "paragraph", MaxChars: 1200},
			Protect: ProtectConfig{Enabled: true, Rules: []string{"code", "link", "placeholder", "xml"}},
			Translate: TranslateConfig{
				Concurrency:     4,
				BatchSize:       1,
				FallbackShrink:  0.5,
				RateLimitPerSec: 5,
				Retry:           RetryConfig{MaxAttempts: 3, Backoff: time.Second},
				Repair: RepairConfig{
					Enabled:              true,
					JSONStructural:       true,
					SchemaAliases:        true,
					Partial:              true,
					PartialThreshold:     0.5,
					PlaceholderNormalize: true,
					PromptUpgrade:        true,
				},
			},
			Postprocess: PostprocessConfig{Enabled: true, TrimSpaces: true},
		},
		Prompt: PromptConfig{
			Vars: map[string]any{"style": "concise, technical", "audience": "developers"},
		},
		Glossary: GlossaryConfig{
			Enabled: false,
			Path:    "./glossary.csv",
			Bootstrap: BootstrapConfig{
				Mode:                   BootstrapModeOff,
				Save:                   true,
				MaxTermsPerBatch:       20,
				MinSourceLen:           2,
				InlineConflictStrategy: InlineConflictRewriteLocal,
			},
		},
		TranslationMemory: TMConfig{Enabled: false, Driver: "sqlite", DSN: "./.linguaflow/tm.db"},
		Plugins:           PluginsConfig{Enabled: false},
		Output:            OutputConfig{Mode: "overwrite", PreserveExtension: true, Incremental: false},
		Log:               LogConfig{Level: "info", Format: "text"},
	}
}

// Validate 检查关键字段是否合法。loader 在合并后调用。
func (c *Config) Validate() error {
	if c.TargetLang == "" {
		return errEmptyTargetLang
	}
	enabled := 0
	for _, b := range c.Backends {
		if b.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return errNoEnabledBackend
	}
	if c.Pipeline.Translate.Concurrency < 1 {
		c.Pipeline.Translate.Concurrency = 1
	}
	if shrink := c.Pipeline.Translate.FallbackShrink; math.IsNaN(shrink) || math.IsInf(shrink, 0) || shrink < 0 {
		c.Pipeline.Translate.FallbackShrink = 0
	} else if shrink >= 1 {
		return fmt.Errorf("pipeline.translate.fallback_shrink must be < 1, got %v", shrink)
	}
	if c.Pipeline.Split.MaxChars < 1 {
		c.Pipeline.Split.MaxChars = 1200
	}
	if err := validateBackendOrder("pipeline.translate", c.Backends, c.Pipeline.Translate.BackendMode, c.Pipeline.Translate.BackendOrder); err != nil {
		return err
	}
	c.Pipeline.Translate.Repair.normalize()
	if c.Glossary.Bootstrap.MaxTermsPerBatch < 1 {
		c.Glossary.Bootstrap.MaxTermsPerBatch = 20
	}
	if c.Glossary.Bootstrap.MinSourceLen < 1 {
		c.Glossary.Bootstrap.MinSourceLen = 2
	}
	if err := validateBackendOrder("glossary.bootstrap", c.Backends, c.Glossary.Bootstrap.BackendMode, c.Glossary.Bootstrap.BackendOrder); err != nil {
		return err
	}
	switch c.Glossary.Bootstrap.Mode {
	case "":
		c.Glossary.Bootstrap.Mode = BootstrapModeOff
	case BootstrapModeOff, BootstrapModePre, BootstrapModeInline:
		// ok
	default:
		return fmt.Errorf("glossary.bootstrap.mode must be one of off|pre|inline, got %q", c.Glossary.Bootstrap.Mode)
	}
	if c.Glossary.Bootstrap.Mode != BootstrapModeOff {
		// 自举要落到 Glossary，强制开启。
		c.Glossary.Enabled = true
	}
	switch c.Glossary.Bootstrap.InlineConflictStrategy {
	case "":
		c.Glossary.Bootstrap.InlineConflictStrategy = InlineConflictRewriteLocal
	case InlineConflictOff, InlineConflictRewriteLocal:
		// ok
	default:
		return fmt.Errorf("glossary.bootstrap.inline_conflict_strategy must be one of off|rewrite-local, got %q",
			c.Glossary.Bootstrap.InlineConflictStrategy)
	}
	return nil
}

func validateBackendOrder(path string, backends []BackendConfig, mode string, order []string) error {
	enabled := make(map[string]struct{}, len(backends))
	for _, b := range backends {
		if b.Enabled {
			enabled[b.Name] = struct{}{}
		}
	}
	switch mode {
	case "", BackendModePrepend, BackendModeRestrict:
		// ok
	default:
		return fmt.Errorf("%s.backend_mode must be one of prepend|restrict, got %q", path, mode)
	}
	if mode == BackendModeRestrict && len(order) == 0 {
		return fmt.Errorf("%s.backend_order must not be empty when backend_mode=restrict", path)
	}
	seen := make(map[string]struct{}, len(order))
	for _, name := range order {
		if name == "" {
			return fmt.Errorf("%s.backend_order must not contain empty backend names", path)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("%s.backend_order contains duplicate backend %q", path, name)
		}
		seen[name] = struct{}{}
		if _, ok := enabled[name]; !ok {
			return fmt.Errorf("%s.backend_order references unknown or disabled backend %q", path, name)
		}
	}
	return nil
}
