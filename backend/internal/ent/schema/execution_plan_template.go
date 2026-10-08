package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

type ExecutionRoundConfig = execution.ExecutionRoundConfig
type TranslateSegmentFilterConfig = execution.TranslateSegmentFilterConfig
type TranslateRoundConfig = execution.TranslateRoundConfig
type ExtractRoundConfig = execution.ExtractRoundConfig
type AdjudicateRoundConfig = execution.AdjudicateRoundConfig
type SemanticQARoundConfig = execution.SemanticQARoundConfig
type ReviseRoundConfig = execution.ReviseRoundConfig
type CorrectRoundConfig = execution.CorrectRoundConfig
type CorrectRuleConfig = execution.CorrectRuleConfig
type ExecutionPlanRubyRetryConfig = execution.ExecutionPlanRubyRetryConfig
type RetryConfig = execution.RetryConfig

// ExecutionPlanTemplate 执行计划模板，user/org 级。
type ExecutionPlanTemplate struct {
	ent.Schema
}

func (ExecutionPlanTemplate) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (ExecutionPlanTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.Int("schema_version").Default(1).Positive(),
		field.String("name").NotEmpty(),
		field.String("description").Default(""),
		field.String("scope").Default("user").
			Comment("user / org"),
		field.Int("owner_user_id").Optional().Nillable().Positive(),
		field.Int("owner_org_id").Optional().Nillable().Positive(),
		// -1 = 内置默认策略（templates.BuiltinExecutionProfileID；schema 包不可
		// import templates，故字面量）。带 Default 使 SQLite 对存量行执行
		// ALTER TABLE ADD COLUMN NOT NULL 时回填到合法的内置策略而非迁移失败。
		field.Int("profile_id").Default(-1).
			Comment("计划级策略引用（ExecutionProfile），为全管道供七项行为预设"),
		field.JSON("ruby_retry", ExecutionPlanRubyRetryConfig{}).
			Optional().
			Comment("注音对齐重试配置"),
		field.JSON("rounds", []ExecutionRoundConfig{}).
			Comment("轮次配置列表，每轮引用后端+提示词"),
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
