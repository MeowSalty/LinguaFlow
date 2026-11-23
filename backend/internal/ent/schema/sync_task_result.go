package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SyncTaskResult holds one resource's accumulated counts and immutable lock
// membership. Zero-count rows are created with the plan and hidden from results.
type SyncTaskResult struct{ ent.Schema }

func (SyncTaskResult) Fields() []ent.Field {
	return []ent.Field{
		field.Int("task_id").Positive().Immutable(),
		field.Int("resource_id").Positive().Immutable(),
		field.String("resource_path").Default(""),
		field.Int("updated_count").Default(0).NonNegative(),
		field.Int("skipped_count").Default(0).NonNegative(),
	}
}

func (SyncTaskResult) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("task", SyncTask.Type).Field("task_id").Required().Unique().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (SyncTaskResult) Indexes() []ent.Index {
	return []ent.Index{index.Fields("task_id", "resource_id").Unique()}
}
