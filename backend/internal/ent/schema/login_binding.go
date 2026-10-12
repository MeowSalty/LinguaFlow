package schema

import (
	"context"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	entmodel "github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

type LoginBinding struct{ ent.Schema }

func (LoginBinding) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (LoginBinding) Fields() []ent.Field {
	return []ent.Field{
		field.Int("user_id").Positive().Immutable(),
		field.String("connection_id").NotEmpty().Immutable(),
		field.Int("external_identity_id").Positive().Immutable(),
		field.Time("last_used_at").Optional().Nillable(),
		field.Time("last_verified_at").Optional().Nillable(),
		field.Int64("verified_auth_generation").Default(0).NonNegative(),
		field.Int64("verified_login_policy_revision").Default(0).NonNegative(),
		field.JSON("evidence", authrules.Evidence{}).Optional().Sensitive(),
	}
}
func (LoginBinding) Hooks() []ent.Hook {
	return []ent.Hook{func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if mutation.Op().Is(ent.OpCreate) {
				m := mutation.(*entmodel.LoginBindingMutation)
				ownerID, _ := m.UserID()
				identityID, _ := m.ExternalIdentityID()
				connectionID, _ := m.ConnectionID()
				identity, err := m.Client().ExternalIdentity.Get(ctx, identityID)
				if err != nil {
					return nil, err
				}
				if identity.UserID != ownerID || connectionID == "password" {
					return nil, fmt.Errorf("binding must belong to its external identity owner and an external connection")
				}
			}
			return next.Mutate(ctx, mutation)
		})
	}}
}
func (LoginBinding) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("login_bindings").Field("user_id").Unique().Required().Immutable(),
		edge.From("connection", AuthConnection.Type).Ref("bindings").Field("connection_id").Unique().Required().Immutable(),
		edge.From("external_identity", ExternalIdentity.Type).Ref("bindings").Field("external_identity_id").Unique().Required().Immutable(),
		edge.To("sessions", RefreshSession.Type),
	}
}
func (LoginBinding) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "connection_id").Unique()}
}
