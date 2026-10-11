package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// WorkRequest is one application-owned external invocation and usage identity.
type WorkRequest struct{ ent.Schema }

func (WorkRequest) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (WorkRequest) Fields() []ent.Field {
	return []ent.Field{
		field.String("identity").NotEmpty().Unique(), field.Int("job_id").Positive(),
		field.Int("resource_id").Positive(), field.Int("job_round_id").Positive(),
		field.Int64("retry_epoch").NonNegative(), field.JSON("segment_ids", []int{}),
		field.String("stage").NotEmpty(), field.Int("backend_id").NonNegative(),
		field.String("candidate_id").Default(""), field.Int("logical_attempt").Default(0).NonNegative(),
		field.JSON("members", json.RawMessage{}).Optional().Comment("Versioned per-member candidate identity, version, pool and invocation cursor; absent for legacy single-candidate requests"),
		field.JSON("debits", json.RawMessage{}).Optional().Comment("Per-member attempt counters before and after reservation; only proven pre-dispatch aborts may restore them"),
		field.JSON("glossary_receipt", json.RawMessage{}).Optional().Comment("Idempotent inline glossary absorption result, committed with the glossary entries"),
		field.Int("usage_record_id").Optional().Nillable().Positive(),
		field.String("budget_model").NotEmpty(), field.String("input_digest").NotEmpty(),
		field.String("state").Default("reserved"), field.Bool("usage_known").Default(false),
		field.Int64("input_tokens").Default(0).NonNegative(), field.Int64("output_tokens").Default(0).NonNegative(),
		field.Int64("duration_ms").Default(0).NonNegative(), field.String("last_error").Default(""),
	}
}
func (WorkRequest) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("job", Job.Type).Field("job_id").Unique().Required().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("resource", Resource.Type).Field("resource_id").Unique().Required().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("round", JobRound.Type).Field("job_round_id").Unique().Required().Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
func (WorkRequest) Indexes() []ent.Index {
	return []ent.Index{index.Fields("job_id", "state", "id")}
}
