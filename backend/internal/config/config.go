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
	Concurrency     int         `yaml:"concurrency"`
	BatchSize       int         `yaml:"batch_size"`      // 一次合并发送的段数；<=1 表示禁用批量
	FallbackShrink  float64     `yaml:"fallback_shrink"` // 整批失败时下一级 batch = floor(cur*shrink)；0 = 直接降到单段；必须 <1
	RateLimitPerSec int         `yaml:"rate_limit_per_sec"`
	Retry           RetryConfig `yaml:"retry"`
}

type RetryConfig struct {
	MaxAttempts int           `yaml:"max_attempts"`
	Backoff     time.Duration `yaml:"backoff"`
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

// BootstrapConfig 控制术语自举：在翻译前用 LLM 抽取并翻译领域术语。
//
// Enabled 与 Glossary.Enabled 同时为 true 才生效，否则抽取出的术语无处可去。
// Save 控制翻译完成后是否把增量术语回写到 Glossary.Path。
type BootstrapConfig struct {
	Enabled          bool `yaml:"enabled"`
	Save             bool `yaml:"save"`
	MaxTermsPerBatch int  `yaml:"max_terms_per_batch"`
	MinSourceLen     int  `yaml:"min_source_len"`
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
				Enabled:          false,
				Save:             true,
				MaxTermsPerBatch: 20,
				MinSourceLen:     2,
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
	if c.Glossary.Bootstrap.MaxTermsPerBatch < 1 {
		c.Glossary.Bootstrap.MaxTermsPerBatch = 20
	}
	if c.Glossary.Bootstrap.MinSourceLen < 1 {
		c.Glossary.Bootstrap.MinSourceLen = 2
	}
	return nil
}
