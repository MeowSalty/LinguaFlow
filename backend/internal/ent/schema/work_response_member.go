package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// WorkResponseMember owns one model output and optional immutable correction.
type WorkResponseMember struct{ ent.Schema }

func (WorkResponseMember) Fields() []ent.Field {
	return []ent.Field{
		field.Int("request_id").Positive(), field.Int("ordinal").NonNegative(),
		field.Bytes("payload").Optional(), field.Int64("payload_bytes").NonNegative(), field.String("payload_digest"),
		field.Bytes("corrected_payload").Optional(), field.Int64("corrected_bytes").Default(0).NonNegative(), field.String("corrected_digest").Default(""),
		field.String("state").Default("pending"), field.String("failure").Default(""),
		field.Int("outcome_kind").Default(0).NonNegative(), field.Int64("outcome_limit").Default(0).NonNegative(), field.Int64("outcome_observed").Default(0).NonNegative(),
		field.String("candidate_digest").Default(""), field.Int64("candidate_version").Default(0).NonNegative(),
	}
}
func (WorkResponseMember) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("request", WorkRequest.Type).Field("request_id").Unique().Required().Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
func (WorkResponseMember) Indexes() []ent.Index {
	return []ent.Index{index.Fields("request_id", "ordinal").Unique(), index.Fields("request_id", "state", "ordinal")}
}
