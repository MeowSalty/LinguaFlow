package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type ExternalIdentity struct{ ent.Schema }

func (ExternalIdentity) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (ExternalIdentity) Fields() []ent.Field {
	return []ent.Field{
		field.Int("user_id").Positive().Immutable(),
		field.Enum("protocol").Values("oidc", "oauth2", "provider").Immutable(),
		field.String("authority").NotEmpty().Immutable(),
		field.String("subject").NotEmpty().Immutable(),
	}
}
func (ExternalIdentity) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("external_identities").Field("user_id").Unique().Required().Immutable(),
		edge.To("bindings", LoginBinding.Type),
	}
}
func (ExternalIdentity) Indexes() []ent.Index {
	return []ent.Index{index.Fields("protocol", "authority", "subject").Unique()}
}
