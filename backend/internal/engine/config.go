package engine

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// Config 是 Engine 的运行时配置。
type Config struct {
	SourceLang        string
	TargetLang        string
	TranslateDefaults TranslateDefaults
	Repair            repair.Options
	Ruby              RubyConfig
	Glossary          GlossaryConfig
	TMEnabled         bool
	PromptVars        map[string]any
}

// TranslateDefaults 是翻译轮次的全局默认值。
type TranslateDefaults struct {
	BatchSize        int
	MaxWordsPerBatch int
	Concurrency      int
	FallbackShrink   float64
	Retry            backend.RetryPolicy
}

// GlossaryConfig 是术语表的运行时配置。
type GlossaryConfig struct {
	Enabled    bool
	Path       string
	Save       bool
	Bootstrap  config.BootstrapConfig
	Standalone config.StandaloneBootstrapConfig
}

// RubyConfig 是 Ruby 注音保护的运行时配置。
type RubyConfig struct {
	Enabled       bool
	PreserveKinds []string
}
