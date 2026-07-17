package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type User struct {
	ent.Schema
}

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("username").NotEmpty().Unique(),
		field.String("password_hash").NotEmpty().Sensitive(),
		field.String("email").NotEmpty().Unique(),
		field.String("display_name").Optional(),
		field.String("role").Default("user"),
		field.Bool("active").Default(true),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("created_jobs", Job.Type),
		edge.To("reviewed_segments", Segment.Type),
		edge.To("refresh_tokens", RefreshToken.Type),
		edge.To("memberships", OrgMembership.Type),
		edge.To("backends", Backend.Type),
		edge.To("owned_projects", Project.Type),
		edge.To("activity_logs", ActivityLog.Type),
		edge.To("usage_records", UsageRecord.Type),
		edge.To("translation_prompt_templates", TranslationPromptTemplate.Type),
		edge.To("bootstrap_prompt_templates", BootstrapPromptTemplate.Type),
		edge.To("execution_profiles", ExecutionProfile.Type),
		edge.To("execution_plan_templates", ExecutionPlanTemplate.Type),
		edge.To("sync_tasks", SyncTask.Type),
	}
}
