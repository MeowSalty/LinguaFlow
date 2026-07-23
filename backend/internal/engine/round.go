package engine

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// RoundConfig 描述一轮的执行配置。
// Translate / Extract / Adjudicate 互斥，恰好一个必须非 nil。
type RoundConfig struct {
	Backend          backend.Backend
	BatchSize        int
	MaxWordsPerBatch int
	Concurrency      int
	FallbackShrink   float64
	Retry            backend.RetryPolicy
	Context          *pipeline.ContextConfig

	Translate  *TranslateRoundConfig
	Extract    *ExtractRoundConfig
	Adjudicate *AdjudicateRoundConfig
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
	ResponseMode         string
}

// AdjudicateRoundConfig 质量裁决轮次的特有配置。
type AdjudicateRoundConfig struct {
	Renderer        *prompt.AdjudicationRenderer
	AdjudicateCodes []string
	ResponseMode    string
	// MaxBatchIndexSpan 同批段落索引跨度上限；<=0 不限制（默认关闭）。
	MaxBatchIndexSpan int
}
