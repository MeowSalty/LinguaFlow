package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type JobRound struct {
	ent.Schema
}

func (JobRound) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (JobRound) Fields() []ent.Field {
	return []ent.Field{
		field.Int("job_id").Positive().
			Comment("所属任务 ID"),
		field.Int("job_resource_id").Positive().
			Comment("所属任务资源 ID"),
		field.Int("round_index").NonNegative().
			Comment("执行计划快照中的轮次序号（0 起）"),
		field.String("mode").
			Comment("轮次模式：translate, extract, adjudicate, semantic_qa, revise, correct"),
		field.String("status").Default("pending").
			Comment("pending, running, completed, failed, skipped"),
		field.Int("segment_total").Default(0).NonNegative().
			Comment("本轮实际处理的段落数（首次 StageStart 写入；恢复不重设）"),
		field.Int("segment_completed").Default(0).NonNegative().
			Comment("本轮已完成段落数（≡ 该轮 job_round_segments 关联基数；由 progress.DBReporter 独占写入——绝对值、幂等、单调；终态闭合不改写本列，闭合口径在读侧按状态派生）"),
		field.String("error_message").Optional().Nillable().
			Comment("轮次级错误信息"),
		field.Time("started_at").Optional().Nillable().
			Comment("轮次开始执行的时间"),
		field.Time("finished_at").Optional().Nillable().
			Comment("轮次到达终态的时间"),
	}
}

func (JobRound) Edges() []ent.Edge {
	return []ent.Edge{
		// FK 级联删除注解在拥有边的 To 声明上（见 job.go 的 job_rounds、
		// job_resource.go 的 rounds）：DeleteProject/DeleteResource 手动级联
		// 删除 Job/JobResource 时由 DB 自动清理轮次矩阵行（SQLite/PG 均强制
		// 外键，NoAction 会使创建过任务的资源/项目删除必然报约束失败）。
		edge.From("job", Job.Type).
			Ref("job_rounds").
			Field("job_id").
			Unique().
			Required(),
		edge.From("job_resource", JobResource.Type).
			Ref("rounds").
			Field("job_resource_id").
			Unique().
			Required(),
		// 轮次断点的关系化存储（取代 resolved_segment_ids JSON blob 的
		// 全量重写）：每个已解决段一行纯追加，与 segment_completed 同一
		// flush 事务推进，任意崩溃点「计数 ≡ 集合基数」。FK 级联见
		// job_round_segment.go。
		edge.To("resolved_segments", Segment.Type).
			Through("job_round_segments", JobRoundSegment.Type),
	}
}

func (JobRound) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("job_resource_id", "round_index").Unique(),
		index.Fields("job_id"),
	}
}
