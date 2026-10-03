package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// DeletionEntry 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type DeletionEntry struct{ ent.Schema }

func (DeletionEntry) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (DeletionEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int("location_id").Positive().Unique(),
		field.Int("project_id").Default(0).NonNegative(),
		field.String("owner_kind").Default("site"),
		field.Int("owner_id").Default(0).NonNegative(),
		field.Enum("status").Values("pending", "running", "blocked", "done").Default("pending"),
		field.Int("attempts").Default(0).NonNegative(),
		field.String("error_code").Default(""),
		field.Time("not_before"),
		field.Time("next_retry_at").Optional().Nillable(),
	}
}
func (DeletionEntry) Edges() []ent.Edge {
	return []ent.Edge{edge.To("location", BlobLocation.Type).Field("location_id").Unique().Required()}
}
