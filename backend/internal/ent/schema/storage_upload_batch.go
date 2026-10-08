package schema

import (
	"encoding/json"
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type StorageUploadBatch struct{ ent.Schema }

func (StorageUploadBatch) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageUploadBatch) Fields() []ent.Field {
	return []ent.Field{
		field.Int("actor_id").Positive().Immutable(), field.Int("project_id").Positive().Immutable(),
		field.String("idempotency_key").NotEmpty().Immutable(), field.String("operation_id").NotEmpty().Unique().Immutable(),
		field.Int("contract_version").Default(1), field.String("manifest_hash").NotEmpty().Immutable(),
		field.JSON("manifest", json.RawMessage{}).Optional(),
		field.String("status").Default("pending"), field.String("lease_token").Default(""), field.Time("lease_until").Optional().Nillable(),
	}
}
func (StorageUploadBatch) Indexes() []ent.Index {
	return []ent.Index{index.Fields("actor_id", "project_id", "idempotency_key").Unique()}
}

type StorageUploadBatchItem struct{ ent.Schema }

func (StorageUploadBatchItem) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (StorageUploadBatchItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int("batch_id").Positive().Immutable(), field.Int("ordinal").NonNegative().Immutable(),
		field.String("path").Immutable(), field.Int64("size").NonNegative().Immutable(), field.String("sha256").NotEmpty().Immutable(),
		field.Int("task_id").Optional().Nillable(), field.String("status").Default("pending"),
		field.String("error_code").Default(""), field.JSON("response", json.RawMessage{}).Optional(),
	}
}
func (StorageUploadBatchItem) Indexes() []ent.Index {
	return []ent.Index{index.Fields("batch_id", "ordinal").Unique()}
}
