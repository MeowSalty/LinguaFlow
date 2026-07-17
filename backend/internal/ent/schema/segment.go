package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type Segment struct {
	ent.Schema
}

func (Segment) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Segment) Fields() []ent.Field {
	return []ent.Field{
		field.Int("segment_index").NonNegative(),
		field.String("source_text").NotEmpty(),
		field.String("target_text").Optional().Nillable(),
		field.Enum("status").
			Values("pending", "translated", "edited", "approved", "rejected").
			Default("pending"),
		field.String("review_comment").Optional().Nillable(),
		field.Int("resource_id").Optional().Nillable().Positive().
			Comment("所属资源 ID"),
		field.Text("meta").Optional().Nillable().
			Comment("parser 注入的格式元数据（JSON 序列化），用于按需渲染时还原格式"),
		field.JSON("quality_issues", []qa.QualityIssue{}).Optional().
			Comment("QA 检测到的质量问题列表"),
	}
}

func (Segment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("resource", Resource.Type).
			Ref("segments").
			Field("resource_id").
			Unique(),
		edge.From("reviewed_by", User.Type).
			Ref("reviewed_segments").
			Unique(),
	}
}
