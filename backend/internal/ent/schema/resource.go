package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type Resource struct {
	ent.Schema
}

func (Resource) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Resource) Fields() []ent.Field {
	return []ent.Field{
		field.String("filename").NotEmpty().
			Comment("原始文件名，如 quest_en.json"),
		field.String("format").NotEmpty().
			Comment("文件格式：srt, vtt, ass, json, md, txt"),
		field.String("storage_path").NotEmpty().
			Comment("文件存储路径"),
		field.Int("total_segments").Default(0).NonNegative().
			Comment("文件解析后的总段落数"),
		field.String("status").Default("ready").
			Comment("资源状态：ready, processing, error"),
		field.String("error_message").Optional().Nillable().
			Comment("解析错误信息"),
		field.Int("project_id").Optional().Nillable().Positive().
			Comment("所属项目 ID"),
	}
}

func (Resource) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("project", Project.Type).
			Ref("resources").
			Field("project_id").
			Unique(),
		edge.To("segments", Segment.Type),
		edge.To("job_resources", JobResource.Type),
	}
}
