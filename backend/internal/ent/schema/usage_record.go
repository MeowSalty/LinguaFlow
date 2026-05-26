package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type UsageRecord struct {
	ent.Schema
}

func (UsageRecord) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (UsageRecord) Fields() []ent.Field {
	return []ent.Field{
		field.String("source").Default("job"),
		field.Int("api_calls").Default(0).NonNegative(),
		field.Int("input_tokens").Default(0).NonNegative(),
		field.Int("output_tokens").Default(0).NonNegative(),
		field.Int("segment_count").Default(0).NonNegative(),
		field.String("note").Optional(),
	}
}

func (UsageRecord) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("usage_records").
			Unique(),
		edge.From("organization", Organization.Type).
			Ref("usage_records").
			Unique(),
		edge.From("project", Project.Type).
			Ref("usage_records").
			Unique(),
		edge.From("job", Job.Type).
			Ref("usage_records").
			Unique(),
	}
}
