package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// StorageBackup 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type StorageBackup struct{ ent.Schema }

func (StorageBackup) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageBackup) Fields() []ent.Field {
	return []ent.Field{
		field.String("identity").NotEmpty().Unique(),
		field.Enum("status").Values("metadata_only", "incomplete", "complete").Default("metadata_only"),
		field.Time("expires_at"),
		field.JSON("manifest", map[string]any{}).Optional(),
	}
}
