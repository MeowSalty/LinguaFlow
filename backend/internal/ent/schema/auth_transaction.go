package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
)

// AuthTransaction is the single durable state machine for external browser flows.
type AuthTransaction struct{ ent.Schema }

func (AuthTransaction) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (AuthTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Immutable(),
		field.Enum("purpose").Values("login", "link", "reauthenticate", "test").Immutable(),
		field.Enum("status").Values("created", "callback_processing", "callback_verified", "awaiting_confirmation", "completed", "cancelled", "expired", "failed").Default("created"),
		field.String("connection_id").NotEmpty().Immutable(),
		field.Int64("connection_revision").Positive().Immutable(),
		field.Int64("connection_generation").Positive().Immutable(),
		field.Int64("policy_revision").Positive().Immutable(),
		field.Int("user_id").Optional().Nillable().Positive().Immutable(),
		field.Int64("auth_version").Optional().Nillable().Positive().Immutable(),
		field.Int("binding_id").Optional().Nillable().Positive().Immutable(),
		field.String("state_hash").NotEmpty().Unique().Sensitive().Immutable(),
		field.String("nonce").NotEmpty().Sensitive().Immutable(),
		field.String("pkce_verifier").NotEmpty().Sensitive().Immutable(),
		field.String("browser_challenge").NotEmpty().Sensitive().Immutable(),
		field.String("completion_url").NotEmpty().Immutable(),
		field.String("return_to").Default("").Immutable(),
		field.String("exchange_ticket_hash").Optional().Nillable().Unique().Sensitive(),
		field.Time("exchange_ticket_expires_at").Optional().Nillable(),
		field.String("confirmation_token_hash").Optional().Nillable().Unique().Sensitive(),
		field.Time("confirmation_expires_at").Optional().Nillable(),
		field.Enum("identity_protocol").Values("oidc", "oauth2", "provider").Optional(),
		field.String("identity_issuer").Optional(),
		field.String("identity_subject").Optional(),
		field.String("identity_display_name").Optional(),
		field.String("identity_username").Optional(),
		field.String("identity_email").Optional(),
		field.Time("identity_auth_time").Optional().Nillable(),
		field.JSON("evidence", authrules.Evidence{}).Optional().Sensitive(),
		field.String("authorization_connection_id").Optional(),
		field.Int64("authorization_connection_generation").Optional(),
		field.Int64("authorization_policy_revision").Optional(),
		field.String("authorized_action").Optional().Immutable(),
		field.String("authorized_target_id").Optional().Immutable(),
		field.Time("authorization_expires_at").Optional().Nillable().Immutable(),
		field.String("failure_code").Optional(),
		field.Int("actor_session_id").Optional().Nillable().Positive().Immutable(),
		field.String("test_snapshot").Optional().Sensitive().Immutable(),
		field.String("test_protocol_fingerprint").Optional().Immutable(),
		field.String("test_diagnostic_fingerprint").Optional().Immutable(),
		field.String("test_deployment_fingerprint").Optional().Immutable(),
		field.Time("test_validated_at").Optional().Nillable(),
		field.Int64("test_applied_revision").Optional().Nillable().Positive(),
		field.Int64("test_base_revision").Optional().Positive().Immutable(),
		field.Time("expires_at").Immutable(),
		field.Time("finished_at").Optional().Nillable(),
	}
}
func (AuthTransaction) Edges() []ent.Edge {
	return []ent.Edge{edge.From("connection", AuthConnection.Type).Ref("transactions").Field("connection_id").Unique().Required().Immutable()}
}
func (AuthTransaction) Indexes() []ent.Index {
	return []ent.Index{index.Fields("status", "expires_at"), index.Fields("user_id", "status")}
}
