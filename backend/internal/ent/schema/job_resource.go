package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type JobResource struct {
	ent.Schema
}

func (JobResource) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (JobResource) Fields() []ent.Field {
	return []ent.Field{
		field.String("status").Default("pending").
			Comment("pending, running, completed, failed, cancelled"),
		field.JSON("segment_ids", []int{}).
			Default(func() []int { return []int{} }).
			Comment("本任务要处理的 Resource 级 Segment ID 快照；空数组表示按资源 pending 段动态选择"),
		field.Int("segment_count").Default(0).NonNegative().
			Comment("待翻译的段落数"),
		field.Int("completed_segments").Default(0).NonNegative().
			Comment("已完成的段落数"),
		field.Int("skipped_segments").Default(0).NonNegative().
			Comment("被系统跳过的段落数（已翻译、空文本、纯占位符等）"),
		field.String("output_path").Optional().
			Comment("输出文件路径"),
		field.String("error_message").Optional().Nillable().
			Comment("翻译错误信息"),
		field.String("current_stage").Optional().Default("").
			Comment("当前执行阶段名称：translate, bootstrap 等"),
		field.Int("stage_total").Default(0).NonNegative().
			Comment("当前阶段的总段落数（StageStart 时写入）"),
		field.Int("stage_completed").Default(0).NonNegative().
			Comment("当前阶段已完成的段落数（SegmentDone 时递增）"),
		field.Time("started_at").Optional().Nillable().
			Comment("资源开始执行的时间"),
	}
}

func (JobResource) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("job", Job.Type).
			Ref("job_resources").
			Unique().
			Required(),
		edge.From("resource", Resource.Type).
			Ref("job_resources").
			Unique().
			Required(),
	}
}
