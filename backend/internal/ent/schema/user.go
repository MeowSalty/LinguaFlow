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
		field.String("email").NotEmpty().Unique(),
		field.String("display_name").Optional(),
		field.String("status").Default("active"),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("jobs", Job.Type),
		edge.To("reviewed_segments", Segment.Type),
	}
}
