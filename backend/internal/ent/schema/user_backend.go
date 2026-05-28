package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type UserBackend struct {
	ent.Schema
}

func (UserBackend) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (UserBackend) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.Enum("backend_type").Values("openai", "anthropic", "google"),
		field.Int("priority").Default(0),
		field.JSON("options", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }),
	}
}

func (UserBackend) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("user_backends").
			Unique().
			Required(),
	}
}

func (UserBackend) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Edges("user").Unique(),
	}
}
