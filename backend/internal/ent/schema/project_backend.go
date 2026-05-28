package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type ProjectBackend struct {
	ent.Schema
}

func (ProjectBackend) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (ProjectBackend) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_index").NonNegative(),
		field.Enum("source").Values("user", "org"),
		field.Int("backend_id").Positive(),
	}
}

func (ProjectBackend) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("project", Project.Type).
			Ref("project_backends").
			Unique().
			Required(),
	}
}

func (ProjectBackend) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_index").Edges("project").Unique(),
		index.Fields("source", "backend_id").Edges("project").Unique(),
	}
}
