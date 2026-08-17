package engine

import (
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/correct"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// Options 是 Engine 的构造参数。
type Options struct {
	Rounds            []Round
	RubyRetryBackends []backend.Backend
	RubyRetryAttempts int // 注音对齐定向重试轮数；<=0 由 handler 兜底为 1（仅 backends 非空时生效）
	Config            *Config
	Logger            *slog.Logger
	Reporter          progress.Reporter
	Resources         RuntimeResources
}

// Round 描述一轮翻译的执行配置（Engine 级别）。
type Round struct {
	RoundIndex       int
	Backend          backend.Backend
	BatchSize        int
	MaxWordsPerBatch int
	Concurrency      int
	FallbackShrink   float64
	Retry            backend.RetryPolicy
	Renderer         *prompt.Renderer
	Repair           *repair.Config
	ResponseMode     string

	Mode              string
	ProtectRules      []string
	RubyEnabled       bool
	RubyPreserveKinds []string
	Context           *pipeline.ContextConfig
	Postprocess       *pipeline.PostprocessConfig

	// 抽取轮次专用字段
	ExtractRenderer             *prompt.BootstrapRenderer
	ExtractMaxTermsPer1000Chars float64
	ExtractMinSourceLen         int
	ExtractMaxWordsPerBatch     int
	ExtractRepair               repair.Options

	// 裁决轮次专用字段
	AdjudicateRenderer *prompt.AdjudicationRenderer
	AdjudicateCodes    []string
	// MaxBatchIndexSpan 同批段落索引跨度上限；<=0 不限制（默认关闭，预埋）。
	MaxBatchIndexSpan int

	// 语义质检轮次专用字段
	SemanticQARenderer *prompt.SemanticQARenderer
	SegmentScope       string   // "all"(默认) | "with_issues" | "with_issue_codes"
	IssueCodes         []string // 仅 with_issue_codes 生效

	// 本地改写轮次专用字段
	CorrectRules []correct.RuleConfig
}

// RuntimeResources 封装可选的运行时资源。
type RuntimeResources struct {
	Glossary glossary.Glossary
	TM       tm.TranslationMemory
}

// buildRoundConfigs 将 engine.Round 转换为 RoundConfig（中间配置）。
func buildRoundConfigs(in []Round, cfg *Config) []RoundConfig {
	if len(in) == 0 {
		return nil
	}
	globalRetry := cfg.TranslateDefaults.Retry
	out := make([]RoundConfig, 0, len(in))
	for _, r := range in {
		retry := r.Retry
		if retry.MaxAttempts == 0 {
			retry = globalRetry
		}

		mode := r.Mode
		if mode == "" {
			mode = pipeline.RoundModeTranslate
		}

		var roundCtx *pipeline.ContextConfig
		if r.Context != nil {
			roundCtx = r.Context
		}

		rc := RoundConfig{
			RoundIndex:       r.RoundIndex,
			Backend:          r.Backend,
			BatchSize:        r.BatchSize,
			MaxWordsPerBatch: r.MaxWordsPerBatch,
			Concurrency:      r.Concurrency,
			FallbackShrink:   r.FallbackShrink,
			Retry:            retry,
			Context:          roundCtx,
		}

		switch mode {
		case pipeline.RoundModeTranslate:
			var roundRepair *repair.Config
			if r.Repair != nil {
				rr := *r.Repair
				rr.Normalize()
				roundRepair = &rr
			}

			var roundPostprocess *pipeline.PostprocessConfig
			if r.Postprocess != nil {
				roundPostprocess = &pipeline.PostprocessConfig{
					TrimSpaces: r.Postprocess.TrimSpaces,
				}
			}

			rc.Translate = &TranslateRoundConfig{
				Renderer:          r.Renderer,
				Repair:            roundRepair,
				ResponseMode:      r.ResponseMode,
				ProtectRules:      r.ProtectRules,
				RubyEnabled:       r.RubyEnabled,
				RubyPreserveKinds: r.RubyPreserveKinds,
				Postprocess:       roundPostprocess,
			}

		case pipeline.RoundModeExtract:
			rc.Extract = &ExtractRoundConfig{
				Renderer:             r.ExtractRenderer,
				MaxTermsPer1000Chars: r.ExtractMaxTermsPer1000Chars,
				MinSourceLen:         r.ExtractMinSourceLen,
				MaxWordsPerBatch:     r.ExtractMaxWordsPerBatch,
				Repair:               r.ExtractRepair,
				ResponseMode:         r.ResponseMode,
			}

		case pipeline.RoundModeAdjudicate:
			rc.Adjudicate = &AdjudicateRoundConfig{
				Renderer:          r.AdjudicateRenderer,
				AdjudicateCodes:   r.AdjudicateCodes,
				ResponseMode:      r.ResponseMode,
				MaxBatchIndexSpan: r.MaxBatchIndexSpan,
			}

		case pipeline.RoundModeSemanticQA:
			rc.SemanticQA = &SemanticQARoundConfig{
				Renderer:          r.SemanticQARenderer,
				ResponseMode:      r.ResponseMode,
				MaxBatchIndexSpan: r.MaxBatchIndexSpan,
				SegmentScope:      r.SegmentScope,
				IssueCodes:        r.IssueCodes,
			}

		case pipeline.RoundModeCorrect:
			rc.Correct = &CorrectRoundConfig{
				Rules: r.CorrectRules,
			}

		default:
			// 未知模式默认为翻译
			rc.Translate = &TranslateRoundConfig{
				Renderer: r.Renderer,
			}
		}

		out = append(out, rc)
	}
	return out
}

// buildPipelineRounds 将 RoundConfig 转换为 pipeline.Round（含 Handler）。
// 注入引擎级资源：glossary、TM、ruby restorer 等。
func buildPipelineRounds(
	configs []RoundConfig,
	glossaryRes glossary.Glossary,
	tmRes tm.TranslationMemory,
	rubyRestorer *ruby.Restorer,
	rubyRetryBackends []backend.Backend,
	defaultRepair repair.Options,
	inlineBootstrap bool,
	maxTermsPer1000 float64,
	minSourceLen int,
	inlineConflictStr string,
	logger *slog.Logger,
	reporter progress.Reporter,
	rubyRetryAttempts int,
) ([]pipeline.Round, error) {
	out := make([]pipeline.Round, 0, len(configs))
	for _, rc := range configs {
		round, err := buildSinglePipelineRound(
			rc, glossaryRes, tmRes, rubyRestorer, rubyRetryBackends,
			defaultRepair, inlineBootstrap, maxTermsPer1000, minSourceLen,
			inlineConflictStr, logger, reporter, rubyRetryAttempts,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, round)
	}
	return out, nil
}

func buildSinglePipelineRound(
	rc RoundConfig,
	glossaryRes glossary.Glossary,
	tmRes tm.TranslationMemory,
	rubyRestorer *ruby.Restorer,
	rubyRetryBackends []backend.Backend,
	defaultRepair repair.Options,
	inlineBootstrap bool,
	maxTermsPer1000 float64,
	minSourceLen int,
	inlineConflictStr string,
	logger *slog.Logger,
	reporter progress.Reporter,
	rubyRetryAttempts int,
) (pipeline.Round, error) {
	if rc.Extract != nil {
		return buildExtractPipelineRound(rc, glossaryRes, logger, reporter)
	}
	if rc.Adjudicate != nil {
		return buildAdjudicatePipelineRound(rc, defaultRepair, logger, reporter)
	}
	if rc.SemanticQA != nil {
		return buildSemanticQAPipelineRound(rc, defaultRepair, logger, reporter)
	}
	if rc.Correct != nil {
		return buildCorrectPipelineRound(rc, logger, reporter)
	}
	return buildTranslatePipelineRound(
		rc, glossaryRes, tmRes, rubyRestorer, rubyRetryBackends,
		defaultRepair, inlineBootstrap, maxTermsPer1000, minSourceLen,
		inlineConflictStr, logger, reporter, rubyRetryAttempts,
	)
}

func buildTranslatePipelineRound(
	rc RoundConfig,
	glossaryRes glossary.Glossary,
	tmRes tm.TranslationMemory,
	rubyRestorer *ruby.Restorer,
	rubyRetryBackends []backend.Backend,
	defaultRepair repair.Options,
	inlineBootstrap bool,
	maxTermsPer1000 float64,
	minSourceLen int,
	inlineConflictStr string,
	logger *slog.Logger,
	reporter progress.Reporter,
	rubyRetryAttempts int,
) (pipeline.Round, error) {
	t := rc.Translate
	if t == nil {
		t = &TranslateRoundConfig{}
	}

	repairOpts := defaultRepair
	if t.Repair != nil {
		repairOpts = t.Repair.ToOptions()
	}

	// 构建 per-round Protector
	var prot protect.Protector
	if t.RubyEnabled || len(t.ProtectRules) > 0 {
		ps := []protect.Protector{}
		if t.RubyEnabled {
			ps = append(ps, &ruby.Extractor{})
		}
		if len(t.ProtectRules) > 0 {
			ps = append(ps, protect.FromRules(t.ProtectRules))
		}
		prot = protect.Compose(ps...)
	}

	rubyMode := ""
	if t.RubyEnabled {
		rubyMode = prompt.RubyModeJSON
		if t.ResponseMode == "text" {
			rubyMode = prompt.RubyModeSection
		}
	}

	ctxConfig := pipeline.DefaultContextConfig()
	if rc.Context != nil {
		ctxConfig = *rc.Context
	}

	handler := &pipeline.TranslateHandler{
		Backend:                rc.Backend,
		RoundIndex:             rc.RoundIndex,
		BatchSize:              rc.BatchSize,
		MaxWordsPerBatch:       rc.MaxWordsPerBatch,
		FallbackShrink:         rc.FallbackShrink,
		Retry:                  rc.Retry,
		ResponseMode:           t.ResponseMode,
		Renderer:               t.Renderer,
		Glossary:               glossaryRes,
		TM:                     tmRes,
		Repair:                 repairOpts,
		Context:                ctxConfig,
		Protector:              prot,
		RubyEnabled:            t.RubyEnabled,
		RubyPreserveKinds:      t.RubyPreserveKinds,
		RubyMode:               rubyMode,
		Postprocess:            t.Postprocess,
		RubyRestorer:           rubyRestorer,
		RubyRetryBackends:      rubyRetryBackends,
		RubyRetryAttempts:      rubyRetryAttempts,
		InlineBootstrap:        inlineBootstrap,
		MaxTermsPer1000Chars:   maxTermsPer1000,
		MinBootstrapSourceLen:  minSourceLen,
		InlineConflictStrategy: inlineConflictStr,
		Reporter:               reporter,
		Logger:                 logger,
	}

	return pipeline.Round{
		Concurrency: rc.Concurrency,
		Retry:       rc.Retry,
		Context:     rc.Context,
		Shrink:      rc.FallbackShrink,
		Handler:     handler,
	}, nil
}

func buildExtractPipelineRound(
	rc RoundConfig,
	glossaryRes glossary.Glossary,
	logger *slog.Logger,
	reporter progress.Reporter,
) (pipeline.Round, error) {
	e := rc.Extract
	if e == nil {
		e = &ExtractRoundConfig{}
	}

	handler := &pipeline.ExtractHandler{
		Backends:             []backend.Backend{rc.Backend},
		RoundIndex:           rc.RoundIndex,
		Renderer:             e.Renderer,
		Glossary:             glossaryRes,
		Retry:                rc.Retry,
		BatchSize:            rc.BatchSize,
		MaxWordsPerBatch:     e.MaxWordsPerBatch,
		MaxTermsPer1000Chars: e.MaxTermsPer1000Chars,
		MinSourceLen:         e.MinSourceLen,
		Repair:               e.Repair,
		ResponseMode:         e.ResponseMode,
		Logger:               logger,
		Reporter:             reporter,
	}

	// NOTE: extract 不接缩批（BuildBatches 不读 poolIndex，批次约束恒为原始值）；
	// Shrink 留零值，RunRound 入口会兜底为 1.0 供 SSE 文案展示"重切"。
	// 若未来需要缩批，在此补 Shrink: rc.FallbackShrink（=require schema/OpenAPI/校验全部接通）。
	return pipeline.Round{
		Concurrency: rc.Concurrency,
		Retry:       rc.Retry,
		Context:     rc.Context,
		Handler:     handler,
	}, nil
}

func buildAdjudicatePipelineRound(
	rc RoundConfig,
	defaultRepair repair.Options,
	logger *slog.Logger,
	reporter progress.Reporter,
) (pipeline.Round, error) {
	a := rc.Adjudicate
	if a == nil {
		a = &AdjudicateRoundConfig{}
	}

	handler := &pipeline.AdjudicateHandler{
		Backend:           rc.Backend,
		RoundIndex:        rc.RoundIndex,
		Renderer:          a.Renderer,
		BatchSize:         rc.BatchSize,
		MaxWordsPerBatch:  rc.MaxWordsPerBatch,
		MaxBatchIndexSpan: a.MaxBatchIndexSpan,
		Retry:             rc.Retry,
		ResponseMode:      a.ResponseMode,
		Repair:            defaultRepair,
		AdjudicateCodes:   a.AdjudicateCodes,
		Reporter:          reporter,
		Logger:            logger,
	}

	// NOTE: adjudicate 不接缩批（BuildBatches 不读 poolIndex）；Shrink 留零值，RunRound 兜底 1.0。
	// 若未来需要缩批，在此补 Shrink: rc.FallbackShrink（=require schema/OpenAPI/校验全部接通）。
	return pipeline.Round{
		Concurrency: rc.Concurrency,
		Retry:       rc.Retry,
		Context:     rc.Context,
		Handler:     handler,
	}, nil
}

func buildSemanticQAPipelineRound(
	rc RoundConfig,
	defaultRepair repair.Options,
	logger *slog.Logger,
	reporter progress.Reporter,
) (pipeline.Round, error) {
	s := rc.SemanticQA
	if s == nil {
		s = &SemanticQARoundConfig{}
	}

	handler := &pipeline.SemanticQAHandler{
		Backend:           rc.Backend,
		RoundIndex:        rc.RoundIndex,
		Renderer:          s.Renderer,
		BatchSize:         rc.BatchSize,
		MaxWordsPerBatch:  rc.MaxWordsPerBatch,
		MaxBatchIndexSpan: s.MaxBatchIndexSpan,
		Retry:             rc.Retry,
		ResponseMode:      s.ResponseMode,
		Repair:            defaultRepair,
		SegmentScope:      s.SegmentScope,
		IssueCodes:        s.IssueCodes,
		Reporter:          reporter,
		Logger:            logger,
	}

	// NOTE: semantic_qa 不接缩批（BuildBatches 不读 poolIndex）；Shrink 留零值，RunRound 兜底 1.0。
	// 若未来需要缩批，在此补 Shrink: rc.FallbackShrink（=require schema/OpenAPI/校验全部接通）。
	return pipeline.Round{
		Concurrency: rc.Concurrency,
		Retry:       rc.Retry,
		Context:     rc.Context,
		Handler:     handler,
	}, nil
}

func buildCorrectPipelineRound(
	rc RoundConfig,
	logger *slog.Logger,
	reporter progress.Reporter,
) (pipeline.Round, error) {
	c := rc.Correct
	if c == nil {
		c = &CorrectRoundConfig{}
	}

	// 构建 correct.Engine：按白名单 + rule-level enabled 过滤规则。
	// round 级 enabled 已由"轮次是否出现在 rounds 数组"决定（与其他轮次一致）。
	// Concurrency 由顶层 RoundConfig 承载（与 service 快照一致）。
	rulesEngine := correct.New(correct.Config{
		Rules:       c.Rules,
		Concurrency: rc.Concurrency,
	})

	// 幂等引擎：仅含本规则消费的 issue code（如 punctuation_missing），
	// 用与原 checker 同一的 PunctuationMissingChecker 验幂等，防成环不级联其他 checker。
	var idem *qa.Engine
	consumed := rulesEngine.ConsumedIssueCodes()
	if len(consumed) > 0 {
		idem = qa.NewEngine(qa.Config{Enabled: true, Checks: consumed}, logger)
	}

	handler := &pipeline.CorrectHandler{
		Rules:       rulesEngine,
		Idempotency: idem,
		Reporter:    reporter,
		Logger:      logger,
	}

	// NOTE: correct 不接缩批（BuildBatches 不读 poolIndex）；Shrink 留零值，RunRound 兜底 1.0。
	// 无 Backend（纯本地）、无 Context、无 Retry 的实际语义（Retry 零值 = 单池，无重试）。
	return pipeline.Round{
		Concurrency: rc.Concurrency,
		Retry:       rc.Retry,
		Context:     rc.Context,
		Handler:     handler,
	}, nil
}
