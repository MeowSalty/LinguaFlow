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
		edge.To("jobs", Job.Type),
		edge.To("created_translation_jobs", TranslationJob.Type),
		edge.To("reviewed_segments", Segment.Type),
		edge.To("refresh_tokens", RefreshToken.Type),
		edge.To("memberships", OrgMembership.Type),
		edge.To("user_backends", UserBackend.Type),
		edge.To("owned_projects", Project.Type),
		edge.To("activity_logs", ActivityLog.Type),
		edge.To("usage_records", UsageRecord.Type),
	}
}
