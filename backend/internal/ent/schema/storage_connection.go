package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// StorageConnection 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type StorageConnection struct{ ent.Schema }

func (StorageConnection) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageConnection) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.Enum("driver").Values("local", "s3"),
		field.Enum("owner_kind").Values("site", "user", "org").Default("site"),
		field.Int("owner_id").Default(0).NonNegative(),
		field.String("backend_id").Optional(),
		field.String("endpoint").Default(""),
		field.String("region").Default(""),
		field.Bool("path_style").Default(false),
		field.Enum("auth_source").Values("deployment", "stored").Default("deployment"),
		field.Int64("active_auth_generation").Default(0).NonNegative(),
		field.Int64("management_generation").Default(0).NonNegative(),
		field.Enum("status").Values("enabled", "disabled").Default("enabled"),
		field.String("health").Default("unknown"),
		field.Time("checked_at").Optional().Nillable(),
	}
}
