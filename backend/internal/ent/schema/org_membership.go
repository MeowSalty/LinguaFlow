package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type OrgMembership struct {
	ent.Schema
}

func (OrgMembership) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (OrgMembership) Fields() []ent.Field {
	return []ent.Field{
		field.String("role").Default("member"),
	}
}

func (OrgMembership) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("organization", Organization.Type).
			Ref("memberships").
			Unique().
			Required(),
		edge.From("user", User.Type).
			Ref("memberships").
			Unique().
			Required(),
	}
}

func (OrgMembership) Indexes() []ent.Index {
	return []ent.Index{
		index.Edges("organization", "user").Unique(),
	}
}
