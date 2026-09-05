package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
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
			Comment("pending, running, paused, completed, failed, cancelled"),
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
		field.Int64("progress_total").Default(0).NonNegative().
			Comment("已知工作量总数（单位=段落×轮：JobRound pending→running 时累加 segment_total；矩阵重算时全量刷新）"),
		field.Int64("progress_completed").Default(0).NonNegative().
			Comment("已完成工作量（单位=段落×轮：各轮 segment_completed 之和；矩阵重算时全量刷新）"),
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
		// FK 级联：任务删除时由 DB 自动清理轮次矩阵行（entsql.OnDelete
		// 注解须挂在拥有边的 To 声明上，子侧 From 注解不传导）。
		edge.To("job_rounds", JobRound.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("sse_events", SSEEvent.Type),
	}
}

func (Job) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("project_id", "id"),
	}
}
