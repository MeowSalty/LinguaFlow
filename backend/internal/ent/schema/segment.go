package schema

import (
	"context"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	entschema "entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type Segment struct {
	ent.Schema
}

func (Segment) Annotations() []entschema.Annotation {
	return []entschema.Annotation{entsql.Annotation{Checks: map[string]string{"segment_content_version_safe": "content_version >= 1 AND content_version <= 9007199254740991"}}}
}

func (Segment) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Segment) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("content_version").Default(1).Positive().Comment("Monotonic version of content and acceptance state; undo never restores an earlier version"),
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

// Hooks keeps every normal ent write on the same content-version contract,
// including bulk review, issue-only changes and undo. Timestamp-only writes do
// not invalidate candidates. A conditional update that matches no row does not
// increment a version.
func (Segment) Hooks() []ent.Hook {
	return []ent.Hook{func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if !m.Op().Is(ent.OpUpdate | ent.OpUpdateOne) {
				return next.Mutate(ctx, m)
			}
			if _, ok := m.Field("content_version"); ok {
				return nil, fmt.Errorf("segment content_version cannot be replaced")
			}
			if delta, ok := m.AddedField("content_version"); ok && delta.(int64) < 0 {
				return nil, fmt.Errorf("segment content_version cannot decrease")
			}
			changed := false
			for _, name := range []string{"source_text", "target_text", "status", "review_comment", "quality_issues", "meta", "segment_index", "resource_id"} {
				if _, ok := m.Field(name); ok || m.FieldCleared(name) {
					changed = true
					break
				}
			}
			if len(m.AddedIDs("reviewed_by")) > 0 || len(m.RemovedIDs("reviewed_by")) > 0 || m.EdgeCleared("reviewed_by") {
				changed = true
			}
			if changed {
				if err := m.AddField("content_version", int64(1)); err != nil {
					return nil, err
				}
			}
			return next.Mutate(ctx, m)
		})
	}}
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
		// 反向不 Unique → M2M：JobRound.resolved_segments 的轮次断点关联
		//（through job_round_segments，见 job_round_segment.go）。
		edge.From("resolved_in_rounds", JobRound.Type).
			Ref("resolved_segments"),
	}
}

func (Segment) Indexes() []ent.Index {
	return []ent.Index{
		// 段落列表按资源内 segment_index 范围取窗口（游标翻页与双向
		// 邻接探测），无此复合索引时范围条件会退化为全表扫描。
		index.Fields("resource_id", "segment_index"),
	}
}
