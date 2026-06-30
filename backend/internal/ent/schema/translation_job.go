package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type TranslationJob struct {
	ent.Schema
}

func (TranslationJob) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (TranslationJob) Fields() []ent.Field {
	return []ent.Field{
		field.String("status").Default("pending").
			Comment("pending, running, completed, failed, cancelled"),
		field.String("trigger_type").Default("manual").
			Comment("触发类型：manual, file_update, glossary_change, web_edit"),
		field.Int("execution_plan_id").Positive().
			Comment("引用的执行计划模板 ID（必填）"),
		field.JSON("translation_config", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			Comment("翻译配置快照，创建时从项目配置复制并可覆盖"),
		field.Int("resource_count").Default(0).NonNegative().
			Comment("关联的资源文件数"),
		field.Int("completed_resources").Default(0).NonNegative().
			Comment("已完成的资源数"),
		field.Int("failed_resources").Default(0).NonNegative().
			Comment("失败的资源数"),
		field.Int("total_segments").Default(0).NonNegative().
			Comment("总段落数（创建时为选中的 segment 数，ReconcileJob 修正为实际翻译量）"),
		field.Int("stage_total").Default(0).NonNegative().
			Comment("实际需要翻译的段落数（ReconcileJob 从各资源的 stage_total 聚合）"),
		field.Int("completed_segments").Default(0).NonNegative().
			Comment("已完成段落数"),
		field.String("error_message").Optional().Nillable().
			Comment("任务级错误信息"),
		field.Time("started_at").Optional().Nillable().
			Comment("任务开始执行的时间，MarkJobRunning 时写入"),
	}
}

func (TranslationJob) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("project", Project.Type).
			Ref("translation_jobs").
			Unique().
			Required(),
		edge.From("created_by", User.Type).
			Ref("created_translation_jobs").
			Unique(),
		edge.To("job_resources", JobResource.Type),
	}
}
