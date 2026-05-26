package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type GlossaryEntry struct {
	ent.Schema
}

func (GlossaryEntry) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (GlossaryEntry) Fields() []ent.Field {
	return []ent.Field{
		field.String("scope_key").NotEmpty(),
		field.String("source_key").NotEmpty(),
		field.String("source").NotEmpty(),
		field.String("target").NotEmpty(),
		field.Bool("case_sensitive").Default(false),
		field.String("notes").Optional(),
		field.Int("project_id").Optional().Nillable().Positive(),
		field.Int("organization_id").Optional().Nillable().Positive(),
	}
}

func (GlossaryEntry) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("project", Project.Type).
			Ref("glossary_entries").
			Field("project_id").
			Unique(),
		edge.From("organization", Organization.Type).
			Ref("glossary_entries").
			Field("organization_id").
			Unique(),
	}
}

func (GlossaryEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("scope_key", "source_key").Unique(),
	}
}
