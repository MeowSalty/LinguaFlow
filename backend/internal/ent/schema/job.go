package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Job struct {
	ent.Schema
}

func (Job) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Job) Fields() []ent.Field {
	return []ent.Field{
		field.Int("project_id").Positive().
			Comment("所属项目 ID"),
		field.String("status").Default("pending").
			Comment("pending, running, completed, failed, cancelled"),
		field.String("trigger_type").Default("manual").
			Comment("触发类型：manual, file_update, glossary_change, web_edit"),
		field.Int("execution_plan_id").Positive().
			Comment("引用的执行计划模板 ID（必填）"),
		field.JSON("execution_config", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			Comment("执行配置快照，创建时从项目配置复制并可覆盖"),
		field.Int("resource_count").Default(0).NonNegative().
			Comment("关联的资源文件数"),
		field.Int("completed_resources").Default(0).NonNegative().
			Comment("已完成的资源数"),
		field.Int("failed_resources").Default(0).NonNegative().
			Comment("失败的资源数"),
		field.Int("total_segments").Default(0).NonNegative().
			Comment("总段落数（创建时选中的 segment 数）"),
		field.Int("skipped_segments").Default(0).NonNegative().
			Comment("被系统跳过的段落数（聚合自 JobResource）"),
		field.Int("stage_total").Default(0).NonNegative().
			Comment("实际需要处理的段落数（ReconcileJob 从各资源的 stage_total 聚合）"),
		field.Int("completed_segments").Default(0).NonNegative().
			Comment("已完成段落数"),
		field.String("error_message").Optional().Nillable().
			Comment("任务级错误信息"),
		field.Time("started_at").Optional().Nillable().
			Comment("任务开始执行的时间，MarkJobRunning 时写入"),
	}
}

func (Job) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("project", Project.Type).
			Ref("jobs").
			Field("project_id").
			Unique().
			Required(),
		edge.From("created_by", User.Type).
			Ref("created_jobs").
			Unique(),
		edge.To("job_resources", JobResource.Type),
		edge.To("sse_events", SSEEvent.Type),
	}
}

func (Job) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("project_id", "id"),
	}
}
