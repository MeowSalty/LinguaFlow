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
		field.Int("manifest_version").Default(0).NonNegative(),
		field.Bool("manifest_sealed").Default(false),
		field.Int("pool_index").Default(0).NonNegative(),
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
			Comment("封口清单成员数；恢复不按剩余批次重设，删除成员时同事务校准"),
		field.Int("segment_completed").Default(0).NonNegative().
			Comment("本轮 job_round_segments 关联基数；共享 workstate writer 在 Job 锁内维护，终态有效进度另按状态派生"),
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
		// 每个完成段保留一个最小事实，与正式提交/计数同事务确认。
		// FK 级联删除后由共享 writer 在原事务内校准基数。
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
