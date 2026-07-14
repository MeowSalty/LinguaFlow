package engine

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// RoundConfig 描述一轮的执行配置。
// Translate 和 Extract 互斥，恰好一个必须非 nil。
type RoundConfig struct {
	Backend          backend.Backend
	BatchSize        int
	MaxWordsPerBatch int
	Concurrency      int
	FallbackShrink   float64
	Retry            backend.RetryPolicy
	Context          *pipeline.ContextConfig

	Translate *TranslateRoundConfig
	Extract   *ExtractRoundConfig
}

// TranslateRoundConfig 翻译轮次的特有配置。
type TranslateRoundConfig struct {
	Renderer     *prompt.Renderer
	Repair       *repair.Config
	ResponseMode string

	ProtectRules      []string
	RubyEnabled       bool
	RubyPreserveKinds []string
	Postprocess       *pipeline.PostprocessConfig
}

// ExtractRoundConfig 术语抽取轮次的特有配置。
type ExtractRoundConfig struct {
	Renderer             *prompt.BootstrapRenderer
	MaxTermsPer1000Chars float64
	MinSourceLen         int
	MaxWordsPerBatch     int
	Repair               repair.Options
}
