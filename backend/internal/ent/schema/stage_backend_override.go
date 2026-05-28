package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type StageBackendOverride struct {
	ent.Schema
}

func (StageBackendOverride) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (StageBackendOverride) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("stage").Values("translate", "bootstrap"),
		field.Enum("backend_mode").Values("inherit", "prepend", "restrict").Default("inherit"),
		field.Strings("backend_order").Default([]string{}),
	}
}

func (StageBackendOverride) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("project", Project.Type).
			Ref("stage_backend_overrides").
			Unique().
			Required(),
	}
}

func (StageBackendOverride) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("stage").Edges("project").Unique(),
	}
}
