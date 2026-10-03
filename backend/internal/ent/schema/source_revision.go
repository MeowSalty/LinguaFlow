package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SourceRevision 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type SourceRevision struct{ ent.Schema }

func (SourceRevision) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (SourceRevision) Fields() []ent.Field {
	return []ent.Field{
		field.Int("resource_id").Positive().Immutable(),
		field.Int("project_id").Positive().Immutable(),
		field.Int("source_blob_id").Positive().Immutable(),
		field.String("format").NotEmpty().Immutable(),
		field.String("parser_version").NotEmpty().Immutable(),
		field.Enum("verification_state").Values("verified", "legacy_unverified").Default("verified"),
		field.Int64("size").Optional().Nillable().NonNegative(),
		field.String("sha256").Optional().Nillable(),
		field.Time("retain_until").Optional().Nillable(),
		field.Bool("current").Default(true),
		field.Bool("deleted").Default(false),
	}
}
func (SourceRevision) Edges() []ent.Edge {
	return []ent.Edge{edge.To("blob", Blob.Type).Field("source_blob_id").Unique().Required().Immutable()}
}
func (SourceRevision) Indexes() []ent.Index {
	return []ent.Index{index.Fields("resource_id", "current")}
}
