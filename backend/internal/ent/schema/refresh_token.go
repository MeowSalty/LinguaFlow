package schema

import (
	"context"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	entmodel "github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

type RefreshToken struct {
	ent.Schema
}

func (RefreshToken) Hooks() []ent.Hook {
	return []ent.Hook{func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if mutation.Op().Is(ent.OpCreate) {
				m := mutation.(*entmodel.RefreshTokenMutation)
				ownerID, _ := m.UserID()
				sessionID, _ := m.SessionID()
				chain, err := m.Client().RefreshSession.Get(ctx, sessionID)
				if err != nil {
					return nil, err
				}
				if chain.UserID != ownerID {
					return nil, fmt.Errorf("refresh token owner must match session")
				}
			}
			return next.Mutate(ctx, mutation)
		})
	}}
}

func (RefreshToken) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (RefreshToken) Fields() []ent.Field {
	return []ent.Field{
		field.String("token_hash").NotEmpty().Unique().Immutable().Sensitive(),
		field.Int("session_id").Positive().Immutable(),
		field.Int64("generation").NonNegative().Immutable(),
		field.Time("expires_at").Immutable(),
		field.Time("consumed_at").Optional().Nillable(),
		field.Time("revoked_at").Optional().Nillable(),
	}
}

func (RefreshToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("session", RefreshSession.Type).Ref("tokens").Field("session_id").Unique().Required().Immutable(),
		edge.From("user", User.Type).
			Ref("refresh_tokens").
			Unique().
			Required().Immutable(),
	}
}

func (RefreshToken) Indexes() []ent.Index {
	return []ent.Index{index.Fields("session_id", "generation").Unique()}
}
