package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type Job struct {
	ent.Schema
}

func (Job) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Job) Fields() []ent.Field {
	return []ent.Field{
		field.String("status").Default("pending"),
		field.String("input_path").Optional(),
		field.String("output_path").Optional(),
		field.String("error_message").Optional().Nillable(),
	}
}

func (Job) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("project", Project.Type).
			Ref("jobs").
			Unique().
			Required(),
		edge.From("created_by", User.Type).
			Ref("jobs").
			Unique(),
		edge.To("sub_jobs", SubJob.Type),
	}
}
