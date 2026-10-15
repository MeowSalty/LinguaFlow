package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
)

// AuthChannelPolicy belongs to an immutable configuration revision.
type AuthChannelPolicy struct{ ent.Schema }

func (AuthChannelPolicy) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (AuthChannelPolicy) Fields() []ent.Field {
	return []ent.Field{
		field.String("connection_id").NotEmpty().Immutable(),
		field.Int64("revision").Positive().Immutable(),
		field.Int64("login_policy_revision").Positive().Immutable(),
		field.Bool("enabled").Default(false).Immutable(),
		field.Bool("login_enabled").Default(false).Immutable(),
		field.Bool("signup_enabled").Default(false).Immutable(),
		field.Bool("link_enabled").Default(false).Immutable(),
		field.JSON("login_rules", authrules.RuleSet{}).Optional().Immutable(),
		field.JSON("signup_rules", authrules.RuleSet{}).Optional().Immutable(),
		field.JSON("link_rules", authrules.RuleSet{}).Optional().Immutable(),
	}
}
func (AuthChannelPolicy) Edges() []ent.Edge {
	return []ent.Edge{edge.From("connection", AuthConnection.Type).Ref("policies").Field("connection_id").Unique().Required().Immutable()}
}
func (AuthChannelPolicy) Indexes() []ent.Index {
	return []ent.Index{index.Fields("connection_id", "revision").Unique()}
}
func (AuthChannelPolicy) Hooks() []ent.Hook { return immutableAuthSnapshotHooks() }
