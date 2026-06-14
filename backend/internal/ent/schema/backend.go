package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Backend struct {
	ent.Schema
}

func (Backend) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Backend) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("scope").Default("user").
			Comment("user / org"),
		field.Int("owner_user_id").Optional().Nillable().Positive(),
		field.Int("owner_org_id").Optional().Nillable().Positive(),
		field.Enum("backend_type").Values("openai", "anthropic", "google"),
		field.Int("priority").Default(0),
		field.JSON("options", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }),
	}
}

func (Backend) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner_user", User.Type).
			Ref("backends").
			Field("owner_user_id").Unique(),
		edge.From("owner_org", Organization.Type).
			Ref("backends").
			Field("owner_org_id").Unique(),
	}
}

func (Backend) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name", "owner_user_id").
			Unique().
			Annotations(entsql.IndexWhere("scope = 'user' AND owner_user_id IS NOT NULL")),
		index.Fields("name", "owner_org_id").
			Unique().
			Annotations(entsql.IndexWhere("scope = 'org' AND owner_org_id IS NOT NULL")),
	}
}
