package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// WorkRequestInputMember is immutable input frozen before external dispatch.
type WorkRequestInputMember struct{ ent.Schema }

func (WorkRequestInputMember) Fields() []ent.Field {
	return []ent.Field{
		field.Int("request_id").Positive(), field.Int("ordinal").NonNegative(),
		field.String("prompt_id").NotEmpty(), field.Int("segment_id").NonNegative(), field.Int("segment_index").NonNegative(),
		field.String("work_id").NotEmpty(), field.String("candidate_id").NotEmpty(),
		field.Int64("baseline_version").NonNegative(), field.String("baseline_digest"), field.Bool("baseline_target_is_null"), field.String("baseline_status"),
		field.Int64("source_generation").NonNegative(), field.Int("source_revision_id").Optional().Nillable(),
		field.Bytes("payload").Optional(), field.Int64("payload_bytes").NonNegative(), field.String("payload_digest"),
	}
}
func (WorkRequestInputMember) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("request", WorkRequest.Type).Field("request_id").Unique().Required().Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
func (WorkRequestInputMember) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("request_id", "ordinal").Unique(), index.Fields("request_id", "prompt_id").Unique(),
		index.Fields("request_id", "candidate_id").Unique(), index.Fields("request_id", "work_id").Unique(),
	}
}
