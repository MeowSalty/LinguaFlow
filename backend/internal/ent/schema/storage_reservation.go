package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// StorageReservation 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type StorageReservation struct{ ent.Schema }

func (StorageReservation) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageReservation) Fields() []ent.Field {
	return []ent.Field{
		field.Int("write_id").Positive().Unique(),
		field.Int("space_id").Positive(),
		field.Int64("bytes").NonNegative(),
		field.Enum("state").Values("reserved", "candidate", "live", "pending_delete", "freed").Default("reserved"),
	}
}
func (StorageReservation) Edges() []ent.Edge {
	return []ent.Edge{edge.To("write", StorageWrite.Type).Field("write_id").Unique().Required(),
		edge.To("space", StorageSpace.Type).Field("space_id").Unique().Required()}
}
