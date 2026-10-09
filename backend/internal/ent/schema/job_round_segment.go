package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// JobRoundSegment 保存同一轮次内每段的最小完成事实。共享 workstate writer
// 在 Job 锁内去重，与正文、候选终态和派生进度在同一事务确认。
// commit_id 用于丢失提交确认后的幂等查询，不复制候选正文。
//
// 两端 FK 均 Cascade：Job/JobResource/Resource/Segment 的删除链不会
// 因关联行残留而约束失败。级联删除的调用方须在同事务内校准进度。
type JobRoundSegment struct {
	ent.Schema
}

func (JobRoundSegment) Fields() []ent.Field {
	return []ent.Field{
		field.String("commit_id").Optional().Nillable().Unique(),
		field.String("candidate_id").Optional().Nillable(),
		field.String("outcome").Default("legacy"),
		field.Int("job_round_id").Positive().
			Comment("所属轮次行 ID"),
		field.Int("segment_id").Positive().
			Comment("已解决段 ID"),
	}
}

func (JobRoundSegment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("job_round", JobRound.Type).
			Field("job_round_id").
			Required().
			Unique().
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("segment", Segment.Type).
			Field("segment_id").
			Required().
			Unique().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (JobRoundSegment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("job_round_id", "segment_id").Unique(),
	}
}
