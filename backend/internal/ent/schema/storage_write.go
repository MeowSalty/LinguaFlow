package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// StorageWrite 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type StorageWrite struct{ ent.Schema }

func (StorageWrite) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageWrite) Fields() []ent.Field {
	return []ent.Field{
		field.Int("task_id").Positive(),
		field.Int("space_id").Positive(),
		field.Int("location_id").Optional().Nillable(),
		field.String("attempt_id").NotEmpty().Unique().Immutable(),
		field.String("object_key").NotEmpty().Immutable(),
		field.String("provider_version").Default(""),
		field.Int64("max_bytes").NonNegative(),
		field.Int64("actual_bytes").Default(0).NonNegative(),
		field.String("sha256").Default(""),
		field.String("phase").Default("accepted"),
		field.Int64("connection_generation").Default(0),
		field.Int64("space_generation").Default(0),
		field.Int64("auth_generation").Default(0),
		field.Bool("outcome_unknown").Default(false),
		field.Time("expires_at").Optional().Nillable(),
	}
}
func (StorageWrite) Edges() []ent.Edge {
	return []ent.Edge{edge.To("task", StorageTask.Type).Field("task_id").Unique().Required(),
		edge.To("space", StorageSpace.Type).Field("space_id").Unique().Required(),
		edge.To("location", BlobLocation.Type).Field("location_id").Unique()}
}
func (StorageWrite) Indexes() []ent.Index {
	return []ent.Index{index.Fields("space_id", "object_key").Unique()}
}
