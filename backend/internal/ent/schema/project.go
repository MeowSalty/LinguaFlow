package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type Project struct {
	ent.Schema
}

func (Project) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Project) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.Int("owner_user_id").Optional().Nillable().Positive(),
		field.Int("owner_org_id").Optional().Nillable().Positive(),
		field.JSON("config", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }),
		field.JSON("default_translation_config", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			Comment("默认翻译配置，创建翻译任务时作为任务配置基底"),
		field.Bool("glossary_enabled").Default(false).
			Comment("翻译过程中是否启用术语表"),
		field.String("source_lang").Default("auto"),
		field.String("target_lang").Default("zh"),
	}
}

func (Project) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner_user", User.Type).
			Ref("owned_projects").
			Field("owner_user_id").
			Unique(),
		edge.From("owner_org", Organization.Type).
			Ref("projects").
			Field("owner_org_id").
			Unique(),
		edge.To("glossary_entries", GlossaryEntry.Type),
		edge.To("tm_entries", TMEntry.Type),
		edge.To("jobs", Job.Type),
		edge.To("activity_logs", ActivityLog.Type),
		edge.To("usage_records", UsageRecord.Type),
		edge.To("resources", Resource.Type),
		edge.To("sync_tasks", SyncTask.Type),
	}
}
