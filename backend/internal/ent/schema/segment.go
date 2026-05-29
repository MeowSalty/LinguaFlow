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
		field.Int("resource_id").Optional().Nillable().Positive().
			Comment("所属资源 ID（新路径，与 sub_job 二选一）"),
	}
}

func (Segment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("sub_job", SubJob.Type).
			Ref("segments").
			Unique(),
		edge.From("resource", Resource.Type).
			Ref("segments").
			Field("resource_id").
			Unique(),
		edge.From("reviewed_by", User.Type).
			Ref("reviewed_segments").
			Unique(),
	}
}
