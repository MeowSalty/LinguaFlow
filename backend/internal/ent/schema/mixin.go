package schema

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
)

type TimeMixin struct {
	mixin.Schema
}

func (TimeMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").Default(timeutil.NowUTC).Immutable(),
		field.Time("updated_at").Default(timeutil.NowUTC).UpdateDefault(timeutil.NowUTC),
	}
}
