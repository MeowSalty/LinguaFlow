package execution

import "github.com/MeowSalty/LinguaFlow/backend/internal/credential"

type JobExecutionSnapshot struct {
	SchemaVersion         int           `json:"schema_version"`
	DefaultsVersion       int           `json:"defaults_version"`
	Sources               []AssetSource `json:"sources"`
	RubyTemplates         RubyTemplates `json:"ruby_templates"`
	RetryReminderTemplate string        `json:"retry_reminder_template"`
	ExecutionPlanID       int           `json:"execution_plan_id"`
	ExecutionPlanName     string        `json:"execution_plan_name"`
	// Strategy 计划级策略快照：来自计划引用的 ExecutionProfile（profile_id），
	// 为全管道（所有改写型轮次与引擎级行为）供 protect/ruby 等七项行为预设。
	Strategy                 StrategySnapshot                `json:"strategy"`
	Rounds                   []JobRoundSnapshot              `json:"rounds"`
	SourceLang               string                          `json:"source_lang"`
	TargetLang               string                          `json:"target_lang"`
	GlossaryEnabled          bool                            `json:"glossary_enabled"`
	TMEnabled                bool                            `json:"tm_enabled,omitempty"`
	AutoApprove              bool                            `json:"auto_approve,omitempty"`
	ExplicitSegmentSelection bool                            `json:"explicit_segment_selection,omitempty"`
	RubyRetry                *ExecutionPlanRubyRetrySnapshot `json:"ruby_retry,omitempty"`
}

type ExecutionPlanRubyRetrySnapshot struct {
	Enabled     bool            `json:"enabled"`
	Backend     BackendSnapshot `json:"backend"`
	MaxAttempts int             `json:"max_attempts,omitempty"`
}

type JobRoundSnapshot struct {
	Mode       string                      `json:"mode"` // "translate" | "extract" | "adjudicate" | "semantic_qa" | "revise" | "correct"
	Backend    BackendSnapshot             `json:"backend"`
	Translate  *JobTranslateRoundSnapshot  `json:"translate,omitempty"`
	Extract    *JobExtractRoundSnapshot    `json:"extract,omitempty"`
	Adjudicate *JobAdjudicateRoundSnapshot `json:"adjudicate,omitempty"`
	SemanticQA *JobSemanticQARoundSnapshot `json:"semantic_qa,omitempty"`
	Revise     *JobReviseRoundSnapshot     `json:"revise,omitempty"`
	Correct    *JobCorrectRoundSnapshot    `json:"correct,omitempty"`
}

type JobTranslateRoundSnapshot struct {
	Prompt           PromptSnapshot         `json:"prompt"`
	BatchSize        int                    `json:"batch_size"`
	MaxWordsPerBatch int                    `json:"max_words_per_batch"`
	Concurrency      int                    `json:"concurrency"`
	FallbackShrink   float64                `json:"fallback_shrink"`
	SegmentFilter    *SegmentFilterSnapshot `json:"segment_filter,omitempty"`
	Retry            RetryConfig            `json:"retry"`
}

type JobExtractRoundSnapshot struct {
	TemplateContent      string      `json:"template_content"` // 从 BootstrapPromptTemplate.Content 快照
	BatchSize            int         `json:"batch_size"`
	MaxWordsPerBatch     int         `json:"max_words_per_batch"`
	Concurrency          int         `json:"concurrency"`
	MaxTermsPer1000Chars float64     `json:"max_terms_per_1000_chars"`
	MinSourceLen         int         `json:"min_source_len"`
	Retry                RetryConfig `json:"retry"`
}

type JobAdjudicateRoundSnapshot struct {
	TemplateContent  string      `json:"template_content"`
	BatchSize        int         `json:"batch_size"`
	MaxWordsPerBatch int         `json:"max_words_per_batch"`
	Concurrency      int         `json:"concurrency"`
	AdjudicateCodes  []string    `json:"adjudicate_codes"`
	Retry            RetryConfig `json:"retry"`
}

type JobSemanticQARoundSnapshot struct {
	TemplateContent  string      `json:"template_content"`
	BatchSize        int         `json:"batch_size"`
	MaxWordsPerBatch int         `json:"max_words_per_batch"`
	Concurrency      int         `json:"concurrency"`
	SegmentScope     string      `json:"segment_scope"` // 物化后的 scope（空 → "all"）
	IssueCodes       []string    `json:"issue_codes"`   // 仅 with_issue_codes 有效
	Retry            RetryConfig `json:"retry"`
}

type JobReviseRoundSnapshot struct {
	TemplateContent  string      `json:"template_content"`
	BatchSize        int         `json:"batch_size"`
	MaxWordsPerBatch int         `json:"max_words_per_batch"`
	Concurrency      int         `json:"concurrency"`
	SegmentScope     string      `json:"segment_scope"` // 物化后的 scope（空 → "with_issues"）
	IssueCodes       []string    `json:"issue_codes"`   // 已冻结的语义问题集合；显式空数组不执行修订
	Retry            RetryConfig `json:"retry"`
}

type JobCorrectRoundSnapshot struct {
	Rules       []JobCorrectRuleSnapshot `json:"rules,omitempty"`
	Concurrency int                      `json:"concurrency"`
}

type JobCorrectRuleSnapshot struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type SegmentFilterSnapshot struct {
	StatusFilter string `json:"status_filter"`        // "pending_only" | "skip_approved" | "all"
	Overridden   bool   `json:"overridden,omitempty"` // true 表示由任务创建时显式覆盖
}

type BackendSnapshot struct {
	Credential         credential.Binding `json:"credential"`
	ID                 int                `json:"id"`
	Scope              string             `json:"scope"`
	Name               string             `json:"name"`
	Type               string             `json:"type"`
	Options            map[string]any     `json:"options"`
	RateLimitPerMinute int                `json:"rate_limit_per_minute"`
}

type PromptSnapshot struct {
	TemplateID   *int   `json:"template_id,omitempty"`
	TemplateName string `json:"template_name"`
	Content      string `json:"content"`
}

type BootstrapPromptSnapshot struct {
	TemplateID   *int   `json:"template_id,omitempty"`
	TemplateName string `json:"template_name"`
	Content      string `json:"content"`
}

type StrategySnapshot struct {
	ProfileID   *int                     `json:"profile_id,omitempty"`
	ProfileName string                   `json:"profile_name"`
	Protect     ProfileProtectConfig     `json:"protect"`
	Postprocess ProfilePostprocessConfig `json:"postprocess"`
	Repair      ProfileRepairConfig      `json:"repair"`
	Glossary    ProfileGlossaryConfig    `json:"glossary"`
	Context     ProfileContextConfig     `json:"context"`
	Ruby        ProfileRubyConfig        `json:"ruby"`
	QA          ProfileQAConfig          `json:"qa"`
}

type ResolvedExecutionSpec = JobExecutionSnapshot

type RubyTemplates struct {
	JSON string `json:"json"`
	Text string `json:"text"`
}

type AssetSource struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// Bindings lists the credential versions retained by this execution.
func (s *JobExecutionSnapshot) Bindings() []credential.Binding {
	seen := map[credential.Binding]bool{}
	var out []credential.Binding
	add := func(b credential.Binding) {
		if b.Valid() && !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	for _, round := range s.Rounds {
		if round.Mode != "correct" {
			add(round.Backend.Credential)
		}
	}
	if s.RubyRetry != nil && s.RubyRetry.Enabled {
		add(s.RubyRetry.Backend.Credential)
	}
	return out
}
