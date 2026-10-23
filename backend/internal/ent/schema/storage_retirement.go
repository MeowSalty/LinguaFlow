package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// StorageRetirement 是存储领域的跨项目退出编排父任务，按目标归属授权，不携带 project_id。
// 它只保存退出事实与执行租约；项目迁移仍由 StorageTask 执行，精确清理仍由 DeletionEntry 执行。
type StorageRetirement struct{ ent.Schema }

func (StorageRetirement) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageRetirement) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("target_kind").Values("connection", "space").Immutable(),
		field.Int("target_id").Positive().Immutable(),
		field.String("target_identity").NotEmpty().Immutable(),
		field.Enum("owner_kind").Values("site", "user", "org").Default("site").Immutable(),
		field.Int("owner_id").Default(0).NonNegative().Immutable(),
		field.Int("actor_id").Default(0).NonNegative().Immutable(),
		field.JSON("confirmed_scope", json.RawMessage{}).Immutable(),
		field.Int("contract_version").Default(1).Immutable(),
		field.String("idempotency_key").NotEmpty().Immutable(),
		field.String("request_hash").NotEmpty().Immutable(),
		field.Enum("status").Values("pending", "running", "waiting_retry", "needs_action", "completed", "failed", "cancelled").Default("pending"),
		field.Enum("phase").Values("draining", "migrating", "waiting_cleanup", "finalizing", "waiting_configuration", "completed").Default("draining"),
		field.Int64("generation").Default(0).NonNegative(),
		field.String("lease_token").Default(""),
		field.Time("lease_until").Optional().Nillable(),
		field.Time("next_check_at").Optional().Nillable(),
		field.Bool("cancel_requested").Default(false),
		field.JSON("result", json.RawMessage{}).Optional(),
		field.String("error_code").Default(""),
		field.Time("started_at").Optional().Nillable(),
		field.Time("finished_at").Optional().Nillable(),
		field.Int("attempts").Default(0).NonNegative(),
		field.Time("retry_started_at").Optional().Nillable(),
	}
}
func (StorageRetirement) Indexes() []ent.Index {
	return []ent.Index{index.Fields("owner_kind", "owner_id", "target_kind", "target_identity", "idempotency_key").Unique(),
		index.Fields("target_kind", "target_id"),
		index.Fields("updated_at", "id"),
		index.Fields("status", "next_check_at")}
}

// Database checks also cover atomic Add mutations, which skip field validators.
func (StorageRetirement) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Checks: map[string]string{"storage_retirement_generation_safe": "generation >= 0 AND generation <= 9007199254740991"}}}
}
