// Source JSON types frozen from v0.13.0 (416b7fc909dda3ed9179bcdbaf4ba47b6b55779d).
// See SOURCE.md for source paths and compatibility policy.
package v013

type sourceJobExecutionSnapshot struct {
	ExecutionPlanID   int    `json:"execution_plan_id"`
	ExecutionPlanName string `json:"execution_plan_name"`
	// Strategy 计划级策略快照：来自计划引用的 ExecutionProfile（profile_id），
	// 为全管道（所有改写型轮次与引擎级行为）供 protect/ruby 等七项行为预设。
	Strategy                 sourceStrategySnapshot                `json:"strategy"`
	Rounds                   []sourceJobRoundSnapshot              `json:"rounds"`
	SourceLang               string                                `json:"source_lang"`
	TargetLang               string                                `json:"target_lang"`
	GlossaryEnabled          bool                                  `json:"glossary_enabled"`
	TMEnabled                bool                                  `json:"tm_enabled,omitempty"`
	AutoApprove              bool                                  `json:"auto_approve,omitempty"`
	ExplicitSegmentSelection bool                                  `json:"explicit_segment_selection,omitempty"`
	RubyRetry                *sourceExecutionPlanRubyRetrySnapshot `json:"ruby_retry,omitempty"`
}

type sourceExecutionPlanRubyRetrySnapshot struct {
	Enabled     bool                  `json:"enabled"`
	Backend     sourceBackendSnapshot `json:"backend"`
	MaxAttempts int                   `json:"max_attempts,omitempty"`
}

type sourceJobRoundSnapshot struct {
	Mode       string                            `json:"mode"` // "translate" | "extract" | "adjudicate" | "semantic_qa" | "revise" | "correct"
	Backend    sourceBackendSnapshot             `json:"backend"`
	Translate  *sourceJobTranslateRoundSnapshot  `json:"translate,omitempty"`
	Extract    *sourceJobExtractRoundSnapshot    `json:"extract,omitempty"`
	Adjudicate *sourceJobAdjudicateRoundSnapshot `json:"adjudicate,omitempty"`
	SemanticQA *sourceJobSemanticQARoundSnapshot `json:"semantic_qa,omitempty"`
	Revise     *sourceJobReviseRoundSnapshot     `json:"revise,omitempty"`
	Correct    *sourceJobCorrectRoundSnapshot    `json:"correct,omitempty"`
}

type sourceJobTranslateRoundSnapshot struct {
	Prompt           sourcePromptSnapshot         `json:"prompt"`
	BatchSize        int                          `json:"batch_size"`
	MaxWordsPerBatch int                          `json:"max_words_per_batch"`
	Concurrency      int                          `json:"concurrency"`
	FallbackShrink   float64                      `json:"fallback_shrink"`
	SegmentFilter    *sourceSegmentFilterSnapshot `json:"segment_filter,omitempty"`
	Retry            sourceRetryConfig            `json:"retry"`
}

type sourceJobExtractRoundSnapshot struct {
	TemplateContent      string            `json:"template_content"` // 从 BootstrapPromptTemplate.Content 快照
	BatchSize            int               `json:"batch_size"`
	MaxWordsPerBatch     int               `json:"max_words_per_batch"`
	Concurrency          int               `json:"concurrency"`
	MaxTermsPer1000Chars float64           `json:"max_terms_per_1000_chars"`
	MinSourceLen         int               `json:"min_source_len"`
	Retry                sourceRetryConfig `json:"retry"`
}

type sourceJobAdjudicateRoundSnapshot struct {
	BatchSize        int               `json:"batch_size"`
	MaxWordsPerBatch int               `json:"max_words_per_batch"`
	Concurrency      int               `json:"concurrency"`
	AdjudicateCodes  []string          `json:"adjudicate_codes"`
	Retry            sourceRetryConfig `json:"retry"`
}

type sourceJobSemanticQARoundSnapshot struct {
	BatchSize        int               `json:"batch_size"`
	MaxWordsPerBatch int               `json:"max_words_per_batch"`
	Concurrency      int               `json:"concurrency"`
	SegmentScope     string            `json:"segment_scope,omitempty"` // 物化后的 scope（空 → "all"）
	IssueCodes       []string          `json:"issue_codes"`             // 仅 with_issue_codes 有效
	Retry            sourceRetryConfig `json:"retry"`
}

type sourceJobReviseRoundSnapshot struct {
	BatchSize        int               `json:"batch_size"`
	MaxWordsPerBatch int               `json:"max_words_per_batch"`
	Concurrency      int               `json:"concurrency"`
	SegmentScope     string            `json:"segment_scope,omitempty"` // 物化后的 scope（空 → "with_issues"）
	IssueCodes       []string          `json:"issue_codes"`             // with_issues 为空时物化为完整语义白名单
	Retry            sourceRetryConfig `json:"retry"`
}

type sourceJobCorrectRoundSnapshot struct {
	Rules       []sourceJobCorrectRuleSnapshot `json:"rules,omitempty"`
	Concurrency int                            `json:"concurrency"`
}

type sourceJobCorrectRuleSnapshot struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type sourceSegmentFilterSnapshot struct {
	StatusFilter string `json:"status_filter"`        // "pending_only" | "skip_approved" | "all"
	Overridden   bool   `json:"overridden,omitempty"` // true 表示由任务创建时显式覆盖
}

type sourceBackendSnapshot struct {
	ID                 int            `json:"id"`
	Scope              string         `json:"scope"`
	Name               string         `json:"name"`
	Type               string         `json:"type"`
	Options            map[string]any `json:"options"`
	RateLimitPerMinute int            `json:"rate_limit_per_minute"`
}

type sourcePromptSnapshot struct {
	TemplateID   *int   `json:"template_id,omitempty"`
	TemplateName string `json:"template_name"`
	Content      string `json:"content"`
}

type sourceStrategySnapshot struct {
	ProfileID   *int                           `json:"profile_id,omitempty"`
	ProfileName string                         `json:"profile_name"`
	Protect     sourceProfileProtectConfig     `json:"protect"`
	Postprocess sourceProfilePostprocessConfig `json:"postprocess"`
	Repair      sourceProfileRepairConfig      `json:"repair"`
	Glossary    sourceProfileGlossaryConfig    `json:"glossary"`
	Context     sourceProfileContextConfig     `json:"context"`
	Ruby        sourceProfileRubyConfig        `json:"ruby"`
	QA          sourceProfileQAConfig          `json:"qa"`
}

type sourceExecutionProfileConfigData struct {
	Protect     sourceProfileProtectConfig     `json:"protect"     yaml:"protect"`
	Postprocess sourceProfilePostprocessConfig `json:"postprocess" yaml:"postprocess"`
	Repair      sourceProfileRepairConfig      `json:"repair"      yaml:"repair"`
	Glossary    sourceProfileGlossaryConfig    `json:"glossary"    yaml:"glossary"`
	Context     sourceProfileContextConfig     `json:"context"     yaml:"context"`
	Ruby        sourceProfileRubyConfig        `json:"ruby"        yaml:"ruby"`
	QA          sourceProfileQAConfig          `json:"qa"          yaml:"qa"`
}

type sourceProfileProtectConfig struct {
	Enabled bool     `json:"enabled" yaml:"enabled"`
	Rules   []string `json:"rules"   yaml:"rules"`
}

type sourceProfileRubyConfig struct {
	Enabled       bool     `json:"enabled"       yaml:"enabled"`
	PreserveKinds []string `json:"preserve_kinds" yaml:"preserve_kinds"`
}

type sourceProfilePostprocessConfig struct {
	Enabled    bool `json:"enabled"     yaml:"enabled"`
	TrimSpaces bool `json:"trim_spaces" yaml:"trim_spaces"`
}

type sourceProfileRepairConfig struct {
	Enabled              bool `json:"enabled"               yaml:"enabled"`
	JSONStructural       bool `json:"json_structural"       yaml:"json_structural"`
	SchemaAliases        bool `json:"schema_aliases"        yaml:"schema_aliases"`
	PlaceholderNormalize bool `json:"placeholder_normalize" yaml:"placeholder_normalize"`
	PromptUpgrade        bool `json:"prompt_upgrade"        yaml:"prompt_upgrade"`
}

type sourceProfileGlossaryConfig struct {
	Bootstrap sourceProfileBootstrapConfig `json:"bootstrap" yaml:"bootstrap"`
}

type sourceProfileBootstrapConfig struct {
	Enabled                bool    `json:"enabled"                  yaml:"enabled"`
	MaxTermsPer1000Chars   float64 `json:"max_terms_per_1000_chars" yaml:"max_terms_per_1000_chars"`
	MinSourceLen           int     `json:"min_source_len"           yaml:"min_source_len"`
	InlineConflictStrategy string  `json:"inline_conflict_strategy" yaml:"inline_conflict_strategy"`
}

type sourceProfileContextConfig struct {
	Enabled  bool `json:"enabled"   yaml:"enabled"`
	Before   int  `json:"before"    yaml:"before"`
	After    int  `json:"after"     yaml:"after"`
	MaxChars int  `json:"max_chars" yaml:"max_chars"`
}

type sourceProfileQAConfig struct {
	Enabled        bool     `json:"enabled"          yaml:"enabled"`
	AutoReject     bool     `json:"auto_reject"      yaml:"auto_reject"`
	Checks         []string `json:"checks" yaml:"checks,omitempty"`
	LengthMethod   string   `json:"length_method"    yaml:"length_method"`
	LengthRatioMin float64  `json:"length_ratio_min" yaml:"length_ratio_min"`
	LengthRatioMax float64  `json:"length_ratio_max" yaml:"length_ratio_max"`
}

type sourceRetryConfig struct {
	MaxAttempts int  `json:"max_attempts" yaml:"max_attempts"`
	BackoffMs   int  `json:"backoff_ms"   yaml:"backoff_ms"`
	Jitter      bool `json:"jitter"       yaml:"jitter"`
}
