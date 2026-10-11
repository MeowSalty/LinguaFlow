package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// WorkCandidate keeps private output separate from the accepted Segment.
type WorkCandidate struct{ ent.Schema }

func (WorkCandidate) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (WorkCandidate) Fields() []ent.Field {
	return []ent.Field{
		field.String("identity").NotEmpty().Unique(), field.Int64("version").Default(1).Positive(),
		field.String("parent_request_id").Default(""),
		field.Int("work_item_id").Positive(), field.Int("dto_version").Positive(),
		field.String("snapshot_digest").NotEmpty(), field.String("mode").NotEmpty(),
		field.Int64("source_generation").NonNegative(), field.Int("source_revision_id").Optional().Nillable(),
		field.Int64("baseline_version").Positive(), field.String("baseline_target").Optional().Nillable(),
		field.String("baseline_status"), field.String("state").Default("pending_alignment"),
		field.Int64("payload_bytes").Default(0).NonNegative(),
		field.JSON("payload", json.RawMessage{}).Optional(),
	}
}
func (WorkCandidate) Edges() []ent.Edge {
	return []ent.Edge{edge.To("work_item", WorkItem.Type).Field("work_item_id").Unique().Required().Annotations(entsql.OnDelete(entsql.Cascade))}
}
func (WorkCandidate) Indexes() []ent.Index {
	return []ent.Index{index.Fields("work_item_id", "state"), index.Fields("state", "id")}
}
