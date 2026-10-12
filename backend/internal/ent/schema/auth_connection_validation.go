package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AuthConnectionValidation records evidence for one exact immutable snapshot.
type AuthConnectionValidation struct{ ent.Schema }

func (AuthConnectionValidation) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (AuthConnectionValidation) Fields() []ent.Field {
	return []ent.Field{
		field.String("connection_id").NotEmpty().Immutable(),
		field.String("fingerprint").NotEmpty().Immutable(),
		field.Int64("revision").Positive().Immutable(),
		field.Bool("fresh_supported").Default(false),
		field.Time("tested_at"),
		field.Int("actor_user_id").Positive(),
	}
}
func (AuthConnectionValidation) Edges() []ent.Edge {
	return []ent.Edge{edge.From("connection", AuthConnection.Type).Ref("validations").Field("connection_id").Unique().Required().Immutable()}
}
func (AuthConnectionValidation) Indexes() []ent.Index {
	return []ent.Index{index.Fields("connection_id", "fingerprint").Unique()}
}
