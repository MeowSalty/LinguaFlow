package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type SSEEvent struct {
	ent.Schema
}

func (SSEEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Int("job_id").Positive(),
		field.Int64("seq").Positive(),
		field.String("type"),
		field.String("level"),
		field.String("stage").Optional(),
		field.String("message"),
		field.JSON("metadata", map[string]any{}).Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (SSEEvent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("job", Job.Type).
			Ref("sse_events").
			Field("job_id").
			Unique().
			Required(),
	}
}

func (SSEEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("job_id", "seq"),
	}
}
