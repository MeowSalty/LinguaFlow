package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

const (
	SegmentRevisionKindReplace = "replace"
	SegmentRevisionKindReverse = "reverse"

	SegmentRevisionStatusPending    = "pending"
	SegmentRevisionStatusTranslated = "translated"
	SegmentRevisionStatusEdited     = "edited"
	SegmentRevisionStatusApproved   = "approved"
	SegmentRevisionStatusRejected   = "rejected"
)

// SegmentRevision stores the before and after snapshots for a segment change.
type SegmentRevision struct {
	ent.Schema
}

func (SegmentRevision) Fields() []ent.Field {
	return []ent.Field{
		field.Int("segment_id").Positive(),
		field.Int("resource_id").Positive(),
		field.String("operation_id").NotEmpty(),
		field.Enum("kind").Values(
			SegmentRevisionKindReplace,
			SegmentRevisionKindReverse,
		),
		field.String("before_target").Optional().Nillable(),
		field.String("after_target").Optional().Nillable(),
		field.Enum("before_status").Values(
			SegmentRevisionStatusPending,
			SegmentRevisionStatusTranslated,
			SegmentRevisionStatusEdited,
			SegmentRevisionStatusApproved,
			SegmentRevisionStatusRejected,
		),
		field.Enum("after_status").Values(
			SegmentRevisionStatusPending,
			SegmentRevisionStatusTranslated,
			SegmentRevisionStatusEdited,
			SegmentRevisionStatusApproved,
			SegmentRevisionStatusRejected,
		),
		field.Int("before_reviewer_id").Optional().Nillable(),
		field.Int("after_reviewer_id").Optional().Nillable(),
		field.JSON("before_issues", []qa.QualityIssue{}).Optional(),
		field.JSON("after_issues", []qa.QualityIssue{}).Optional(),
		field.Int("actor_id").Positive(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (SegmentRevision) Edges() []ent.Edge {
	return []ent.Edge{
		// 段被删除（资源删除/重导入/增量同步/项目删除）时级联清理其撤销历史，
		// 否则 NO ACTION 外键会让这些既有删段流程在产生过 revision 后全部失败。
		edge.To("segment", Segment.Type).
			Field("segment_id").
			Required().
			Unique().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (SegmentRevision) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("segment_id"),
		index.Fields("operation_id"),
		index.Fields("created_at"),
	}
}
