package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SyncTask holds the schema definition for the sync_task entity.
// It stores information about async glossary sync tasks.
type SyncTask struct {
	ent.Schema
}

// Mixin of the SyncTask.
func (SyncTask) Mixin() []ent.Mixin {
	return []ent.Mixin{
		TimeMixin{},
	}
}

// Fields of the SyncTask.
func (SyncTask) Fields() []ent.Field {
	return []ent.Field{
		field.Int("project_id").
			Positive().
			Comment("所属项目 ID"),
		field.Int("entry_id").
			Positive().
			Comment("术语条目 ID"),
		field.Int("actor_user_id").
			Positive().
			Comment("操作用户 ID"),
		field.String("old_target").
			NotEmpty().
			Comment("修改前的旧 target 值"),
		field.String("new_target").
			NotEmpty().
			Comment("修改后的新 target 值"),
		field.Int("total_segments").
			NonNegative().
			Comment("待处理的段落总数"),
		field.Int("processed_segments").
			Default(0).
			NonNegative().
			Comment("已处理的段落数"),
		field.String("status").
			NotEmpty().
			Default("pending").
			Comment("任务状态: pending, running, completed, failed, cancelled"),
		field.Text("segment_ids").
			Comment("JSON 序列化的段落 ID 列表"),
		field.Text("resource_ids").
			Comment("JSON 序列化的资源 ID 列表"),
		field.Text("result").
			Optional().
			Comment("JSON 序列化的结果摘要"),
		field.String("error").
			Optional().
			Comment("错误信息"),
		field.Time("cancelled_at").
			Optional().
			Nillable().
			Comment("取消时间"),
	}
}

// Edges of the SyncTask.
func (SyncTask) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("project", Project.Type).
			Ref("sync_tasks").
			Field("project_id").
			Unique().
			Required(),
		edge.From("entry", GlossaryEntry.Type).
			Ref("sync_tasks").
			Field("entry_id").
			Unique().
			Required(),
		edge.From("actor", User.Type).
			Ref("sync_tasks").
			Field("actor_user_id").
			Unique().
			Required(),
	}
}

// Indexes of the SyncTask.
func (SyncTask) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("project_id", "status"),
		index.Fields("status", "created_at"),
	}
}
