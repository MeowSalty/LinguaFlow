package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type ActivityLog struct {
	ent.Schema
}

func (ActivityLog) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (ActivityLog) Fields() []ent.Field {
	return []ent.Field{
		field.String("action").NotEmpty(),
		field.String("resource_type").NotEmpty(),
		field.Int("resource_id").Optional().Nillable().Positive(),
		field.String("message").Optional(),
		field.JSON("metadata", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }),
	}
}

func (ActivityLog) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("actor", User.Type).
			Ref("activity_logs").
			Unique(),
		edge.From("organization", Organization.Type).
			Ref("activity_logs").
			Unique(),
		edge.From("project", Project.Type).
			Ref("activity_logs").
			Unique(),
		edge.From("job", Job.Type).
			Ref("activity_logs").
			Unique(),
	}
}
