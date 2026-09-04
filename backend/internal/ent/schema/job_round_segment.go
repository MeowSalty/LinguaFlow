package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// JobRoundSegment 是 JobRound ↔ Segment 的多对多关联表：轮次断点的
// 关系化存储，每个 (轮次行, 已解决段) 一行，纯追加、无更新。
//
// 取代早期设计中的 resolved_segment_ids JSON blob——blob 每次集合变化
// 需全量重写（几十 KB/批），关联行只 INSERT 新增段，写放大低两个数量级；
// 且与 segment_completed 在同一 flush 事务推进，保证任意崩溃点
// 「计数 ≡ 集合基数」（checkpoint 不变式）。
//
// 写入方（DBReporter）在 flush 事务内预查已存在段后 CreateBulk 缺失行
// （幂等去重；ent 未启用 upsert feature flag，故不用 OnConflict），
// 唯一索引兜底并发/重试边界的重复插入；读取方（断点恢复 loadResolved）
// 按资源聚合各轮行求并集。
//
// 两端 FK 均 Cascade：Job/JobResource/Resource/Segment 的删除链不会
// 因关联行残留而约束失败（同 job_rounds 的级联理由）。
type JobRoundSegment struct {
	ent.Schema
}

func (JobRoundSegment) Fields() []ent.Field {
	return []ent.Field{
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
