package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// BackupPin 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type BackupPin struct{ ent.Schema }

func (BackupPin) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (BackupPin) Fields() []ent.Field {
	return []ent.Field{
		field.Int("location_id").Positive(),
		field.String("backup_id").NotEmpty(),
		field.Time("expires_at"),
	}
}
func (BackupPin) Edges() []ent.Edge {
	return []ent.Edge{edge.To("location", BlobLocation.Type).Field("location_id").Unique().Required()}
}
func (BackupPin) Indexes() []ent.Index {
	return []ent.Index{index.Fields("backup_id", "location_id").Unique()}
}
