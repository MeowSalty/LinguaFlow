package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Blob 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type Blob struct{ ent.Schema }

func (Blob) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (Blob) Fields() []ent.Field {
	return []ent.Field{
		field.String("identity").NotEmpty().Unique().Immutable(),
		field.Int("project_id").Positive().Immutable(),
		field.Enum("owner_kind").Values("site", "user", "org").Default("user"),
		field.Int("owner_id").Default(0).NonNegative(),
		field.Enum("purpose").Values("source", "export", "snapshot"),
		field.Int64("size").Optional().Nillable().NonNegative(),
		field.String("sha256").Optional().Nillable(),
		field.Enum("status").Values("pending", "ready", "delete_pending", "deleted").Default("pending"),
		field.Int("active_location_id").Optional().Nillable().Positive(),
		field.Int64("location_generation").Default(0).NonNegative(),
	}
}
func (Blob) Edges() []ent.Edge {
	return []ent.Edge{edge.To("active_location", BlobLocation.Type).Field("active_location_id").Unique()}
}

// Database checks also cover atomic Add mutations, which skip field validators.
func (Blob) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Checks: map[string]string{"blob_location_generation_safe": "location_generation >= 0 AND location_generation <= 9007199254740991"}}}
}
