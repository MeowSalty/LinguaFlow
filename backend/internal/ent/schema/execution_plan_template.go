package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// ExecutionRoundConfig 单轮执行配置。
type ExecutionRoundConfig struct {
	Mode      string                `json:"mode"               yaml:"mode"` // "translate" | "extract"
	BackendID int                   `json:"backend_id"         yaml:"backend_id"`
	Translate *TranslateRoundConfig `json:"translate,omitempty" yaml:"translate,omitempty"`
	Extract   *ExtractRoundConfig   `json:"extract,omitempty"   yaml:"extract,omitempty"`
}

// TranslateRoundConfig 翻译轮次配置。
type TranslateRoundConfig struct {
	PromptTemplateID int         `json:"prompt_template_id"  yaml:"prompt_template_id"` // 引用 TranslationPromptTemplate
	ProfileID        int         `json:"profile_id"          yaml:"profile_id"`
	BatchSize        int         `json:"batch_size"          yaml:"batch_size"`
	MaxWordsPerBatch int         `json:"max_words_per_batch" yaml:"max_words_per_batch"`
	Concurrency      int         `json:"concurrency"         yaml:"concurrency"`
	FallbackShrink   float64     `json:"fallback_shrink"     yaml:"fallback_shrink"`
	Retry            RetryConfig `json:"retry"               yaml:"retry"`
}

// ExtractRoundConfig 术语抽取轮次配置。
type ExtractRoundConfig struct {
	BootstrapTemplateID  int         `json:"bootstrap_template_id"   yaml:"bootstrap_template_id"` // 引用 BootstrapPromptTemplate
	BatchSize            int         `json:"batch_size"              yaml:"batch_size"`
	MaxWordsPerBatch     int         `json:"max_words_per_batch"     yaml:"max_words_per_batch"`
	Concurrency          int         `json:"concurrency"             yaml:"concurrency"`
	MaxTermsPer1000Chars float64     `json:"max_terms_per_1000_chars" yaml:"max_terms_per_1000_chars"`
	MinSourceLen         int         `json:"min_source_len"          yaml:"min_source_len"`
	Retry                RetryConfig `json:"retry"                   yaml:"retry"`
}

// ExecutionPlanRubyRetryConfig 注音对齐重试配置。
type ExecutionPlanRubyRetryConfig struct {
	Enabled   bool `json:"enabled"    yaml:"enabled"`    // 是否启用注音对齐重试
	BackendID int  `json:"backend_id" yaml:"backend_id"` // 引用的后端 ID；0 时使用翻译主后端
}

// RetryConfig 重试策略。
type RetryConfig struct {
	MaxAttempts int  `json:"max_attempts" yaml:"max_attempts"`
	BackoffMs   int  `json:"backoff_ms"   yaml:"backoff_ms"`
	Jitter      bool `json:"jitter"       yaml:"jitter"`
}

// ExecutionPlanTemplate 执行计划模板，user/org 级。
type ExecutionPlanTemplate struct {
	ent.Schema
}

func (ExecutionPlanTemplate) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (ExecutionPlanTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("description").Default(""),
		field.String("scope").Default("user").
			Comment("user / org"),
		field.Int("owner_user_id").Optional().Nillable().Positive(),
		field.Int("owner_org_id").Optional().Nillable().Positive(),
		field.JSON("ruby_retry", ExecutionPlanRubyRetryConfig{}).
			Optional().
			Comment("注音对齐重试配置"),
		field.JSON("rounds", []ExecutionRoundConfig{}).
			Comment("轮次配置列表，每轮引用后端+提示词+策略"),
	}
}

func (ExecutionPlanTemplate) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner_user", User.Type).
			Ref("execution_plan_templates").
			Field("owner_user_id").Unique(),
		edge.From("owner_org", Organization.Type).
			Ref("execution_plan_templates").
			Field("owner_org_id").Unique(),
	}
}
