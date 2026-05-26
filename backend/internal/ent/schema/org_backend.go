package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type OrgBackend struct {
	ent.Schema
}

func (OrgBackend) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (OrgBackend) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.Enum("backend_type").Values("openai", "anthropic", "google"),
		field.Int("priority").Default(0),
		field.JSON("options", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }),
	}
}

func (OrgBackend) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("organization", Organization.Type).
			Ref("org_backends").
			Unique().
			Required(),
	}
}

func (OrgBackend) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Edges("organization").Unique(),
	}
}
