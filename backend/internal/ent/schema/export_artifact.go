package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// ExportArtifact 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type ExportArtifact struct{ ent.Schema }

func (ExportArtifact) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (ExportArtifact) Fields() []ent.Field {
	return []ent.Field{
		field.Int("project_id").Positive().Immutable(),
		field.Int("resource_id").Positive().Immutable(),
		field.Int("source_revision_id").Positive().Immutable(),
		field.Int("snapshot_blob_id").Optional().Nillable(),
		field.Int("output_blob_id").Optional().Nillable(),
		field.String("renderer_version").NotEmpty(),
		field.Int64("source_generation").Default(0),
		field.Int64("translation_generation").Default(0),
		field.Int64("output_generation").Default(0),
		field.Enum("status").Values("pending", "ready", "failed", "deleted").Default("pending"),
		field.Bool("rebuildable").Default(true),
		field.String("filename").NotEmpty(),
		field.JSON("options", map[string]any{}).Optional(),
	}
}
func (ExportArtifact) Edges() []ent.Edge {
	return []ent.Edge{edge.To("source_revision", SourceRevision.Type).Field("source_revision_id").Unique().Required().Immutable(),
		edge.To("snapshot_blob", Blob.Type).Field("snapshot_blob_id").Unique(),
		edge.To("output_blob", Blob.Type).Field("output_blob_id").Unique()}
}
