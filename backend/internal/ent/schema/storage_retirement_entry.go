package schema

import (
	"encoding/json"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// StorageRetirementEntry 保存退出任务逐项事实：涉及空间、项目子迁移与需精确核验的对象位置。
// 条目不保存任何凭据或密文，受限授权只存在于 storage_auth_versions。
type StorageRetirementEntry struct{ ent.Schema }

func (StorageRetirementEntry) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageRetirementEntry) Fields() []ent.Field {
	return []ent.Field{
		field.Int("retirement_id").Positive().Immutable(),
		field.String("entry_key").NotEmpty().Immutable(),
		field.String("kind").NotEmpty().Immutable(),
		field.Int("space_id").Optional().Nillable().Positive(),
		field.Int("project_id").Default(0).NonNegative(),
		field.Int("migration_task_id").Optional().Nillable().Positive(),
		field.Int("target_space_id").Optional().Nillable().Positive(),
		field.Int64("expected_storage_generation").Default(0).NonNegative(),
		field.Int64("expected_target_management_generation").Default(0).NonNegative(),
		field.Int64("expected_target_execution_generation").Default(0).NonNegative(),
		field.Time("accepted_at").Optional().Nillable(),
		field.Int("accepted_by").Optional().Nillable().Positive(),
		field.String("object_key").Default(""),
		field.String("provider_version").Default(""),
		// 受限授权恢复的幂等身份：仅在该条目确实提交过受限恢复时写入。
		field.String("idempotency_key").Optional().Nillable().NotEmpty().Immutable(),
		field.String("request_hash").Optional().Nillable().NotEmpty().Immutable(),
		field.Time("next_check_at").Optional().Nillable(),
		field.Enum("status").Values("pending", "running", "waiting_retry", "needs_action", "completed", "failed", "cancelled").Default("pending"),
		field.JSON("result", json.RawMessage{}).Optional(),
		field.String("error_code").Default(""),
	}
}
func (StorageRetirementEntry) Edges() []ent.Edge {
	return []ent.Edge{edge.To("retirement", StorageRetirement.Type).Field("retirement_id").Unique().Required().Immutable(),
		edge.To("space", StorageSpace.Type).Field("space_id").Unique(),
		edge.To("target_space", StorageSpace.Type).Field("target_space_id").Unique(),
		edge.To("migration_task", StorageTask.Type).Field("migration_task_id").Unique()}
}
func (StorageRetirementEntry) Indexes() []ent.Index {
	return []ent.Index{index.Fields("retirement_id", "entry_key").Unique(),
		index.Fields("retirement_id", "kind", "idempotency_key").Unique(),
		index.Fields("retirement_id", "status"),
		index.Fields("status", "next_check_at"),
		index.Fields("space_id")}
}

func (StorageRetirementEntry) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Checks: map[string]string{
		"storage_retirement_entry_idempotency_pair": "((idempotency_key IS NULL AND request_hash IS NULL) OR (idempotency_key IS NOT NULL AND request_hash IS NOT NULL))",
	}}}
}
