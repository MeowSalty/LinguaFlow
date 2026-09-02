package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
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
			Comment("pending, running, completed, failed, cancelled（paused 仅 Job 级；暂停时资源冻结在 running，恢复走 running→pending 重置）"),
		field.JSON("segment_ids", []int{}).
			Default(func() []int { return []int{} }).
			Comment("本任务要处理的 Resource 级 Segment ID 快照；空数组表示按资源 pending 段动态选择"),
		field.Int("segment_count").Default(0).NonNegative().
			Comment("待翻译的段落数"),
		field.Int("completed_segments").Default(0).NonNegative().
			Comment("已完成的段落数"),
		field.Int("skipped_segments").Default(0).NonNegative().
			Comment("被系统跳过的段落数（已翻译、空文本、纯占位符等）"),
		field.Int64("work_weight").Default(0).NonNegative().
			Comment("准入工作配额权重（所选段落 source_text 字节数；动态选择资源首次入线时回填）"),
		field.String("output_path").Optional().
			Comment("输出文件路径"),
		field.String("error_message").Optional().Nillable().
			Comment("翻译错误信息"),
		field.String("warning_message").Optional().Nillable().
			Comment("软警告信息（如 semantic_qa 扫描失败）；资源状态仍为 completed"),
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
		// FK 级联：任务资源删除时由 DB 自动清理其轮次矩阵行
		//（OnDelete 注解须挂在拥有边的 To 声明上）。
		edge.To("rounds", JobRound.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
