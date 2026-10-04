package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// StorageMigrationItem 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type StorageMigrationItem struct{ ent.Schema }

func (StorageMigrationItem) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageMigrationItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int("task_id").Positive(),
		field.Int("blob_id").Positive(),
		field.Int("source_location_id").Positive(),
		field.Int("target_location_id").Optional().Nillable(),
		field.Int64("expected_location_generation").NonNegative(),
		field.Enum("status").Values("pending", "copied", "verified", "committed").Default("pending"),
	}
}
func (StorageMigrationItem) Edges() []ent.Edge {
	return []ent.Edge{edge.To("task", StorageTask.Type).Field("task_id").Unique().Required(),
		edge.To("blob", Blob.Type).Field("blob_id").Unique().Required(),
		edge.To("source_location", BlobLocation.Type).Field("source_location_id").Unique().Required(),
		edge.To("target_location", BlobLocation.Type).Field("target_location_id").Unique()}
}
func (StorageMigrationItem) Indexes() []ent.Index {
	return []ent.Index{index.Fields("task_id", "blob_id").Unique()}
}
