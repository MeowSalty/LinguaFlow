package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type Segment struct {
	ent.Schema
}

func (Segment) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Segment) Fields() []ent.Field {
	return []ent.Field{
		field.Int("segment_index").NonNegative(),
		field.String("source_text").NotEmpty(),
		field.String("target_text").Optional().Nillable(),
		field.String("status").Default("pending"),
		field.String("review_comment").Optional().Nillable(),
	}
}

func (Segment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("sub_job", SubJob.Type).
			Ref("segments").
			Unique().
			Required(),
		edge.From("reviewed_by", User.Type).
			Ref("reviewed_segments").
			Unique(),
	}
}
