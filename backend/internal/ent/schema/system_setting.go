package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type SystemSetting struct {
	ent.Schema
}

func (SystemSetting) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (SystemSetting) Fields() []ent.Field {
	return []ent.Field{
		field.String("key").NotEmpty().Unique(),
		field.Text("value").Optional(),
		field.String("description").Optional(),
	}
}
