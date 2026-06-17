package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Resource struct {
	ent.Schema
}

func (Resource) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Resource) Fields() []ent.Field {
	return []ent.Field{
		field.String("path").NotEmpty().
			Comment("项目内规范化资源相对路径，如 ui/common.json"),
		field.String("format").NotEmpty().
			Comment("文件格式：srt, vtt, ass, json, md, txt"),
		field.String("storage_path").NotEmpty().
			Comment("文件存储路径"),
		field.Int("total_segments").Default(0).NonNegative().
			Comment("文件解析后的总段落数"),
		field.Int("project_id").Optional().Nillable().Positive().
			Comment("所属项目 ID"),
	}
}

func (Resource) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("project_id", "path").Unique(),
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
