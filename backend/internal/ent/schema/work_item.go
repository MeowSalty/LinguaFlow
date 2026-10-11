package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// WorkItem is the durable member and retry cursor of a sealed round manifest.
// It outlives a rejected candidate's payload.
type WorkItem struct{ ent.Schema }

func (WorkItem) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (WorkItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int("job_id").Positive(), field.Int("resource_id").Positive(),
		field.Int("job_round_id").Positive(), field.Int("segment_id").Positive(),
		field.Int64("retry_epoch").Default(0).NonNegative(),
		field.String("state").Default("pending"),
		field.String("candidate_id").Default(""),
		field.Int("pool_index").Default(0).NonNegative(),
		field.Int("main_attempts").Default(0).NonNegative(),
		field.Int("alignment_attempts").Default(0).NonNegative(),
		field.Int("network_attempts").Default(0).NonNegative(),
		field.Int("main_network_attempts").Default(0).NonNegative(),
		field.Int("alignment_network_attempts").Default(0).NonNegative(),
		field.String("prompt_phase").Default("initial"),
		field.Time("next_attempt_at").Optional().Nillable(),
		field.String("last_error").Default(""),
		field.JSON("cursor", json.RawMessage{}).Optional(),
	}
}
func (WorkItem) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("job", Job.Type).Field("job_id").Unique().Required().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("resource", Resource.Type).Field("resource_id").Unique().Required().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("round", JobRound.Type).Field("job_round_id").Unique().Required().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("segment", Segment.Type).Field("segment_id").Unique().Required().Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
func (WorkItem) Indexes() []ent.Index {
	return []ent.Index{index.Fields("job_round_id", "segment_id").Unique(), index.Fields("job_id", "state", "id"), index.Fields("resource_id")}
}
