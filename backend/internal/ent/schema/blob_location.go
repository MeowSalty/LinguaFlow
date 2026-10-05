package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// BlobLocation 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type BlobLocation struct{ ent.Schema }

func (BlobLocation) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (BlobLocation) Fields() []ent.Field {
	return []ent.Field{
		field.Int("blob_id").Optional().Nillable().Positive(),
		field.Int("space_id").Positive(),
		field.String("object_key").NotEmpty().Immutable(),
		field.String("provider_version").Default(""),
		field.Bool("delete_marker").Default(false),
		field.Int64("size").Default(0).NonNegative(),
		field.Enum("status").Values("candidate", "live", "retired", "deleting", "deleted").Default("candidate"),
		field.Enum("integrity").Values("unknown", "available", "missing", "corrupt").Default("unknown"),
		field.Time("verified_at").Optional().Nillable(),
		field.Time("retain_until").Optional().Nillable(),
	}
}
func (BlobLocation) Edges() []ent.Edge {
	return []ent.Edge{edge.To("blob", Blob.Type).Field("blob_id").Unique(),
		edge.To("space", StorageSpace.Type).Field("space_id").Unique().Required()}
}
func (BlobLocation) Indexes() []ent.Index {
	return []ent.Index{index.Fields("space_id", "object_key", "provider_version").Unique()}
}
