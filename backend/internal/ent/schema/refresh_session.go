package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
)

// RefreshSession is the durable family of one login, including consumed tokens.
type RefreshSession struct{ ent.Schema }

func (RefreshSession) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (RefreshSession) Fields() []ent.Field {
	return []ent.Field{
		field.Int("user_id").Positive().Immutable(),
		field.Int64("auth_version").Positive().Immutable(),
		field.String("connection_id").NotEmpty().Immutable(),
		field.Int("binding_id").Optional().Nillable().Positive().Immutable(),
		field.Int64("connection_generation").Positive().Immutable(),
		field.Int64("login_policy_revision").Positive().Immutable(),
		field.JSON("evidence", authrules.Evidence{}).Optional().Sensitive().Immutable(),
		field.Int64("generation").Default(0).NonNegative(),
		field.Time("expires_at").Immutable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}
func (RefreshSession) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("refresh_sessions").Field("user_id").Unique().Required().Immutable(),
		edge.From("connection", AuthConnection.Type).Ref("sessions").Field("connection_id").Unique().Required().Immutable(),
		edge.From("binding", LoginBinding.Type).Ref("sessions").Field("binding_id").Unique().Immutable(),
		edge.To("tokens", RefreshToken.Type),
	}
}
func (RefreshSession) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id"), index.Fields("expires_at")}
}
