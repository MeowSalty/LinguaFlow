package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// StorageAuthVersion 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type StorageAuthVersion struct{ ent.Schema }

func (StorageAuthVersion) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageAuthVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("connection_id").Positive(),
		field.Int64("generation").NonNegative(),
		field.Int("payload_version").Default(1),
		field.Int("aad_version").Default(1),
		field.String("key_id").NotEmpty(),
		field.Bytes("nonce").Sensitive(),
		field.Bytes("ciphertext").Sensitive(),
		field.Enum("status").Values("candidate", "active", "retired", "revoked").Default("candidate"),
		field.Time("expires_at").Optional().Nillable(),
	}
}
func (StorageAuthVersion) Edges() []ent.Edge {
	return []ent.Edge{edge.To("connection", StorageConnection.Type).Field("connection_id").Unique().Required()}
}
func (StorageAuthVersion) Indexes() []ent.Index {
	return []ent.Index{index.Fields("connection_id", "generation").Unique()}
}
