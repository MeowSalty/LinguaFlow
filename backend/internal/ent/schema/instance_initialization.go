package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// InstanceInitialization is committed atomically with the initial policy and identity.
// The singleton ID also serializes initialization across independent processes.
type InstanceInitialization struct{ ent.Schema }

func (InstanceInitialization) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (InstanceInitialization) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").Default(1).Min(1).Max(1).Immutable(),
		field.Int("version").Positive().Immutable(),
		field.Enum("mode").Values("serve", "local").Immutable(),
		field.Int("local_user_id").Optional().Nillable().Positive(),
	}
}
