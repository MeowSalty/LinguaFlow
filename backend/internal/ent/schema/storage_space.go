package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
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
		field.Int64("capacity_bytes").Optional().Nillable().Positive(),
		field.Int64("reserved_bytes").Default(0).NonNegative(),
		field.Int64("candidate_bytes").Default(0).NonNegative(),
		field.Int64("live_bytes").Default(0).NonNegative(),
		field.Int64("pending_delete_bytes").Default(0).NonNegative(),
	}
}
func (StorageSpace) Edges() []ent.Edge {
	return []ent.Edge{edge.To("connection", StorageConnection.Type).Field("connection_id").Unique().Required()}
}

// Database checks also cover atomic Add mutations, which skip field validators.
func (StorageSpace) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Checks: map[string]string{"storage_space_management_generation_safe": "management_generation >= 0 AND management_generation <= 9007199254740991", "storage_space_reserved_bytes_safe": "reserved_bytes >= 0 AND reserved_bytes <= 9007199254740991", "storage_space_candidate_bytes_safe": "candidate_bytes >= 0 AND candidate_bytes <= 9007199254740991", "storage_space_live_bytes_safe": "live_bytes >= 0 AND live_bytes <= 9007199254740991", "storage_space_pending_delete_bytes_safe": "pending_delete_bytes >= 0 AND pending_delete_bytes <= 9007199254740991", "storage_space_total_safe": "reserved_bytes + candidate_bytes + live_bytes + pending_delete_bytes <= 9007199254740991", "storage_space_capacity_safe": "capacity_bytes IS NULL OR (capacity_bytes >= 1 AND capacity_bytes <= 9007199254740991)"}}}
}
