package schema

import (
	"context"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Revisions are immutable snapshots. Only the connection's published pointer
// selects runtime configuration; a saved candidate never changes authentication.
type AuthConnectionRevision struct{ ent.Schema }

func (AuthConnectionRevision) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (AuthConnectionRevision) Fields() []ent.Field {
	return []ent.Field{
		field.String("connection_id").NotEmpty().Immutable(),
		field.Int64("revision").Positive().Immutable(),
		field.String("display_name").NotEmpty().Immutable(),
		field.Int("display_order").Default(0).Immutable(),
		field.Int("config_version").Default(1).Positive().Immutable(),
		field.Bool("username_login_enabled").Default(false).Immutable(),
		field.Bool("email_login_enabled").Default(false).Immutable(),
	}
}
func (AuthConnectionRevision) Edges() []ent.Edge {
	return []ent.Edge{edge.From("connection", AuthConnection.Type).Ref("revisions").Field("connection_id").Unique().Required().Immutable()}
}
func (AuthConnectionRevision) Indexes() []ent.Index {
	return []ent.Index{index.Fields("connection_id", "revision").Unique()}
}
func (AuthConnectionRevision) Hooks() []ent.Hook { return immutableAuthSnapshotHooks() }

func immutableAuthSnapshotHooks() []ent.Hook {
	return []ent.Hook{func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if m.Op().Is(ent.OpUpdate | ent.OpUpdateOne) {
				return nil, fmt.Errorf("authentication snapshots are immutable; create a new revision")
			}
			return next.Mutate(ctx, m)
		})
	}}
}
