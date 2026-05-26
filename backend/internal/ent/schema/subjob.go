package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type SubJob struct {
	ent.Schema
}

func (SubJob) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (SubJob) Fields() []ent.Field {
	return []ent.Field{
		field.String("status").Default("pending"),
		field.String("input_filename").Optional(),
		field.String("output_path").Optional(),
		field.String("error_message").Optional().Nillable(),
	}
}

func (SubJob) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("job", Job.Type).
			Ref("sub_jobs").
			Unique().
			Required(),
		edge.To("segments", Segment.Type),
	}
}
