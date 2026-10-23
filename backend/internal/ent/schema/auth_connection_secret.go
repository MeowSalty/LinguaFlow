package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AuthConnectionSecret is a write-only encrypted client secret version.
type AuthConnectionSecret struct{ ent.Schema }

func (AuthConnectionSecret) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (AuthConnectionSecret) Fields() []ent.Field {
	return []ent.Field{
		field.String("connection_id").NotEmpty().Immutable(),
		field.Int64("version").Positive().Immutable(),
		field.Int("cipher_version").Default(1).Positive().Immutable(),
		field.String("key_id").NotEmpty().Immutable(),
		field.Bytes("nonce").NotEmpty().Sensitive().Immutable(),
		field.Bytes("ciphertext").NotEmpty().Sensitive().Immutable(),
		field.Int("actor_user_id").Positive().Immutable(),
	}
}
func (AuthConnectionSecret) Edges() []ent.Edge {
	return []ent.Edge{edge.From("connection", AuthConnection.Type).Ref("secrets").Field("connection_id").Unique().Required().Immutable()}
}
func (AuthConnectionSecret) Indexes() []ent.Index {
	return []ent.Index{index.Fields("connection_id", "version").Unique()}
}
