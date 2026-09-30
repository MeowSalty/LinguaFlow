package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Credential owns immutable provider/endpoint/ownership bindings. Rotation only
// advances CurrentVersion; historical versions remain independently usable.
type Credential struct{ ent.Schema }

func (Credential) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (Credential) Fields() []ent.Field {
	return []ent.Field{
		field.String("scope").Immutable(),
		field.Int("owner_id").Positive().Immutable(),
		field.String("provider").Immutable(),
		field.String("endpoint").Immutable(),
		field.Int("current_version").Positive(),
	}
}
func (Credential) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("versions", CredentialVersion.Type),
		edge.To("backends", Backend.Type),
	}
}
