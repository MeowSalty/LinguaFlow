package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// JobEvent holds the schema definition for the JobEvent entity.
type JobEvent struct {
	ent.Schema
}

// Mixin of the JobEvent.
func (JobEvent) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

// Fields of the JobEvent.
func (JobEvent) Fields() []ent.Field {
	return []ent.Field{
		field.String("level").Default("info").
			Comment("info, warn, error"),
		field.String("stage").Optional().Default("").
			Comment("关联的阶段名称"),
		field.String("message").NotEmpty().
			Comment("事件描述"),
		field.JSON("metadata", map[string]any{}).Optional().
			Comment("附加元数据，如 segment_index, backend_name 等"),
	}
}

// Edges of the JobEvent.
func (JobEvent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("job", TranslationJob.Type).
			Ref("job_events").Unique().Required(),
	}
}
