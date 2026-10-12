package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ReauthenticationProof is a short-lived, single-use authorization for one action.
type ReauthenticationProof struct{ ent.Schema }

func (ReauthenticationProof) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (ReauthenticationProof) Fields() []ent.Field {
	return []ent.Field{
		field.String("token_hash").NotEmpty().Unique().Sensitive().Immutable(),
		field.Int("user_id").Positive().Immutable(),
		field.Int64("auth_version").Positive().Immutable(),
		field.Enum("method").Values("password", "oidc").Immutable(),
		field.String("connection_id").Optional().Immutable(),
		field.Int64("connection_generation").Optional().Immutable(),
		field.Int64("login_policy_revision").Optional().Immutable(),
		field.Enum("action").Values("link", "password_set", "password_delete", "unlink").Immutable(),
		field.String("target_id").NotEmpty().Immutable(),
		field.Time("authenticated_at").Immutable(),
		field.Time("expires_at").Immutable(),
		field.Time("consumed_at").Optional().Nillable(),
	}
}
func (ReauthenticationProof) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "expires_at")}
}
