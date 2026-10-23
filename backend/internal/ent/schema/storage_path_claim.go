package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// StoragePathClaim 记录空间对 bucket/prefix 的活跃路径占用：一个活跃空间至多持有一条占用。
// 释放时删除占用行并保留 storage_spaces 历史身份；同 bucket 的重叠判定由后续 service 在同一占用上进行。
type StoragePathClaim struct{ ent.Schema }

func (StoragePathClaim) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StoragePathClaim) Fields() []ent.Field {
	return []ent.Field{
		field.Int("space_id").Positive().Unique().Immutable(),
		field.String("identity").NotEmpty().Immutable(),
		field.String("bucket").Default("").Immutable(),
		field.String("prefix").Default("").Immutable(),
		field.Int64("execution_generation").Default(0).NonNegative(),
	}
}
func (StoragePathClaim) Edges() []ent.Edge {
	return []ent.Edge{edge.To("space", StorageSpace.Type).Field("space_id").Unique().Required().Immutable()}
}
func (StoragePathClaim) Indexes() []ent.Index {
	return []ent.Index{index.Fields("bucket", "prefix").Unique()}
}

// Database checks also cover atomic Add mutations, which skip field validators.
func (StoragePathClaim) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Checks: map[string]string{"storage_path_claim_execution_generation_safe": "execution_generation >= 0 AND execution_generation <= 9007199254740991"}}}
}
