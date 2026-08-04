package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type RefreshToken struct {
	ent.Schema
}

func (RefreshToken) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (RefreshToken) Fields() []ent.Field {
	return []ent.Field{
		field.String("token_hash").NotEmpty().Unique(),
		field.Time("expires_at"),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

func (RefreshToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("refresh_tokens").
			Unique().
			Required(),
	}
}
