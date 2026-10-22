package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
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
		// 密文与非秘密代次审计分离：退出完成后可清空 nonce/ciphertext，仅保留 generation 等审计事实。
		field.Bytes("nonce").Optional().Sensitive(),
		field.Bytes("ciphertext").Optional().Sensitive(),
		field.Enum("status").Values("candidate", "active", "retired", "revoked").Default("candidate"),
		field.Enum("purpose").Values("business", "retirement").Default("business"),
		field.Int("retirement_id").Optional().Nillable().Positive(),
		field.Time("expires_at").Optional().Nillable(),
		field.Time("purged_at").Optional().Nillable(),
	}
}
func (StorageAuthVersion) Edges() []ent.Edge {
	return []ent.Edge{edge.To("connection", StorageConnection.Type).Field("connection_id").Unique().Required()}
}
func (StorageAuthVersion) Indexes() []ent.Index {
	return []ent.Index{index.Fields("connection_id", "generation").Unique()}
}

// Database checks also cover atomic Add mutations, which skip field validators.
func (StorageAuthVersion) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Checks: map[string]string{"storage_auth_version_generation_safe": "generation >= 0 AND generation <= 9007199254740991", "storage_auth_version_retirement_purpose_safe": "((purpose = 'business' AND retirement_id IS NULL) OR (purpose = 'retirement' AND retirement_id IS NOT NULL))", "storage_auth_version_purge_ciphertext_safe": "((purged_at IS NULL AND nonce IS NOT NULL AND ciphertext IS NOT NULL) OR (purged_at IS NOT NULL AND nonce IS NULL AND ciphertext IS NULL))"}}}
}
