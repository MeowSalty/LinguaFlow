package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SyncTaskItem is an immutable plan position. Segment and resource identities
// deliberately have no foreign keys: deleting a target must preserve its skip.
type SyncTaskItem struct{ ent.Schema }

func (SyncTaskItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int("task_id").Positive().Immutable(),
		field.Int("ordinal").NonNegative().Immutable(),
		field.Int("segment_id").Positive().Immutable(),
		field.Int("resource_id").NonNegative().Immutable().Comment("计划资源快照；历史已删除目标无法恢复归属时为 0"),
	}
}

func (SyncTaskItem) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("task", SyncTask.Type).Field("task_id").Required().Unique().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (SyncTaskItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("task_id", "ordinal").Unique(),
		index.Fields("task_id", "segment_id").Unique(),
	}
}
