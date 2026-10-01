package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type CredentialVersion struct{ ent.Schema }

func (CredentialVersion) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (CredentialVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("credential_id").Positive().Immutable(),
		field.Int("version").Positive().Immutable(),
		field.Int("encryption_version").Default(1).Positive(),
		field.String("key_id").NotEmpty(),
		field.Bytes("nonce").Sensitive(),
		field.Bytes("ciphertext").Sensitive(),
		field.Bool("revoked").Default(false),
	}
}
func (CredentialVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("credential", Credential.Type).Ref("versions").Field("credential_id").Unique().Required().Immutable(),
		edge.To("job_references", CredentialJobReference.Type),
	}
}
func (CredentialVersion) Indexes() []ent.Index {
	return []ent.Index{index.Fields("credential_id", "version").Unique()}
}
