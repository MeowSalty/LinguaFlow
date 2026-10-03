package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// StorageSpace 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type StorageSpace struct{ ent.Schema }

func (StorageSpace) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageSpace) Fields() []ent.Field {
	return []ent.Field{
		field.Int("connection_id").Positive(),
		field.String("name").NotEmpty(),
		field.String("identity").NotEmpty().Unique().Immutable(),
		field.String("marker_nonce").NotEmpty().Immutable(),
		field.String("bucket").Default("").Immutable(),
		field.String("prefix").Default("").Immutable(),
		field.Enum("owner_kind").Values("site", "user", "org").Default("site"),
		field.Int("owner_id").Default(0).NonNegative(),
		field.Int64("management_generation").Default(0).NonNegative(),
		field.Enum("status").Values("active", "read_only", "disabled").Default("active"),
		field.Bool("verified").Default(false),
		field.Bool("versioned").Default(false),
		field.Int64("capacity_bytes").Default(107374182400).NonNegative(),
		field.Int64("reserved_bytes").Default(0).NonNegative(),
		field.Int64("candidate_bytes").Default(0).NonNegative(),
		field.Int64("live_bytes").Default(0).NonNegative(),
		field.Int64("pending_delete_bytes").Default(0).NonNegative(),
	}
}
func (StorageSpace) Edges() []ent.Edge {
	return []ent.Edge{edge.To("connection", StorageConnection.Type).Field("connection_id").Unique().Required()}
}
