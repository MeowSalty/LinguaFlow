package schema

import (
	"context"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// AuthConnection identifies one login route, not an external subject namespace.
type AuthConnection struct{ ent.Schema }

func (AuthConnection) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (AuthConnection) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Immutable(),
		field.Enum("kind").Values("password", "oidc", "generic_oauth2", "provider").Immutable(),
		field.String("identity_authority_id").Optional().Nillable().Unique().Immutable(),
		field.Int64("aggregate_revision").Default(1).Positive(),
		field.Int64("draft_revision").Optional().Nillable().Positive(),
		field.Int64("published_revision").Optional().Nillable().Positive(),
		field.Int64("auth_generation").Default(1).Positive(),
		field.Time("archived_at").Optional().Nillable(),
	}
}
func (AuthConnection) Hooks() []ent.Hook {
	return []ent.Hook{func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if m.Op().Is(ent.OpDelete | ent.OpDeleteOne) {
				ids, err := m.(interface {
					IDs(context.Context) ([]string, error)
				}).IDs(ctx)
				if err != nil {
					return nil, err
				}
				for _, id := range ids {
					if id == "password" {
						return nil, fmt.Errorf("the built-in password connection cannot be deleted")
					}
				}
			}
			return next.Mutate(ctx, m)
		})
	}}
}
func (AuthConnection) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Checks: map[string]string{
		"auth_connection_reserved_password": "((id = 'password' AND kind = 'password' AND archived_at IS NULL) OR (id <> 'password' AND kind <> 'password'))",
		"auth_connection_generation_safe":   "auth_generation >= 1 AND auth_generation <= 9007199254740991",
	}}}
}
func (AuthConnection) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("revisions", AuthConnectionRevision.Type),
		edge.To("policies", AuthChannelPolicy.Type),
		edge.To("bindings", LoginBinding.Type),
		edge.To("sessions", RefreshSession.Type),
		edge.To("secrets", AuthConnectionSecret.Type),
		edge.To("validations", AuthConnectionValidation.Type),
		edge.To("transactions", AuthTransaction.Type),
	}
}
