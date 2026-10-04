package schema

import (
	"encoding/json"
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type StorageCheck struct{ ent.Schema }

func (StorageCheck) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageCheck) Fields() []ent.Field {
	return []ent.Field{
		field.Int("connection_id").Positive().Immutable(), field.Int("actor_id").Positive().Immutable(),
		field.String("mode").NotEmpty().Immutable(), field.Int64("management_generation").NonNegative().Immutable(),
		field.String("status").Default("running"), field.String("error_code").Default(""),
		field.Time("completed_at").Optional().Nillable(), field.Bool("authorization_activated").Default(false),
		field.Time("deadline").Optional().Nillable(),
		field.JSON("results", json.RawMessage{}).Optional(),
	}
}
func (StorageCheck) Indexes() []ent.Index { return []ent.Index{index.Fields("connection_id", "id")} }

type StorageCheckWrite struct{ ent.Schema }

func (StorageCheckWrite) Fields() []ent.Field {
	return []ent.Field{field.Int("check_id").Positive(), field.Int("write_id").Positive()}
}
func (StorageCheckWrite) Indexes() []ent.Index {
	return []ent.Index{index.Fields("check_id", "write_id").Unique()}
}
