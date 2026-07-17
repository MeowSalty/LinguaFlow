package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type TMEntry struct {
	ent.Schema
}

func (TMEntry) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (TMEntry) Fields() []ent.Field {
	return []ent.Field{
		field.String("scope_key").NotEmpty(),
		field.String("source_hash").NotEmpty(),
		field.String("source_text").NotEmpty(),
		field.String("target_text").NotEmpty(),
		field.String("source_lang").NotEmpty(),
		field.String("target_lang").NotEmpty(),
		field.Int("usage_count").Default(0).NonNegative(),
		field.Int("project_id").Optional().Nillable().Positive(),
	}
}

func (TMEntry) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("project", Project.Type).
			Ref("tm_entries").
			Field("project_id").
			Unique(),
	}
}

func (TMEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("scope_key", "source_hash", "source_lang", "target_lang").Unique(),
	}
}
