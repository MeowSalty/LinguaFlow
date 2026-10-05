package schema

import (
	"encoding/json"
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// StorageTask 持久化保存存储相关事实，生命周期独立于业务对象的删除。
type StorageTask struct{ ent.Schema }

func (StorageTask) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageTask) Fields() []ent.Field {
	return []ent.Field{
		field.Int("contract_version").Default(0).NonNegative(),
		field.Int64("input_size").Default(0).NonNegative(),
		field.String("input_sha256").Default(""),
		field.JSON("source_plan", json.RawMessage{}).Optional(),
		field.JSON("legacy_snapshot", json.RawMessage{}).Optional(),
		field.Time("legacy_snapshot_expires_at").Optional().Nillable(),
		field.JSON("result_snapshot", json.RawMessage{}).Optional(),
		field.String("lease_token").Default(""),
		field.Time("lease_until").Optional().Nillable(),
		field.String("operation_id").NotEmpty().Unique().Immutable(),
		field.String("idempotency_key").NotEmpty().Immutable(),
		field.String("request_hash").NotEmpty().Immutable(),
		field.Int("actor_id").Default(0).NonNegative().Immutable(),
		field.Int("project_id").Default(0).NonNegative().Immutable(),
		field.String("kind").NotEmpty().Immutable(),
		field.Enum("status").Values("pending", "running", "waiting_retry", "needs_action", "completed", "failed", "cancelled").Default("pending"),
		field.String("phase").Default("accepted"),
		field.Enum("cleanup_status").Values("cleanup_pending", "running", "blocked", "done").Default("done"),
		field.String("error_code").Default(""),
		field.Int("resource_id").Optional().Nillable(),
		field.Int("source_revision_id").Optional().Nillable(),
		field.Int("target_space_id").Optional().Nillable(),
		field.Int("result_resource_id").Optional().Nillable(),
		field.Int("result_revision_id").Optional().Nillable(),
		field.Int("result_artifact_id").Optional().Nillable(),
		field.Int64("expected_source_generation").Default(0),
		field.Int64("expected_translation_generation").Default(0),
		field.Int64("expected_storage_generation").Default(0),
		field.Int64("expected_location_generation").Default(0),
		field.Int("attempts").Default(0).NonNegative(),
		field.Time("retry_started_at").Optional().Nillable(),
		field.Time("next_retry_at").Optional().Nillable(),
		field.Time("deadline").Optional().Nillable(),
		field.JSON("input", map[string]any{}).Optional(),
	}
}
func (StorageTask) Indexes() []ent.Index {
	return []ent.Index{index.Fields("actor_id", "project_id", "kind", "idempotency_key").Unique(),
		index.Fields("status", "next_retry_at"),
		index.Fields("updated_at", "id"),
		index.Fields("status", "updated_at", "id"),
		index.Fields("project_id", "updated_at", "id")}
}
