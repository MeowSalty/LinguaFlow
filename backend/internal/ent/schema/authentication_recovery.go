package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AuthenticationRecovery is a deployment-only single-use authorization.
// Its token is never an HTTP session or a reauthentication credential.
type AuthenticationRecovery struct{ ent.Schema }

func (AuthenticationRecovery) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (AuthenticationRecovery) Fields() []ent.Field {
	return []ent.Field{
		field.String("token_hash").NotEmpty().Unique().Sensitive().Immutable(),
		field.Int("user_id").Positive().Immutable(),
		field.Int64("auth_version").Positive().Immutable(),
		field.Int64("local_policy_revision").Positive().Immutable(),
		field.Enum("action").Values("restore_local_login").Immutable(),
		field.Time("expires_at").Immutable(),
		field.Time("consumed_at").Optional().Nillable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}
func (AuthenticationRecovery) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "expires_at")}
}
