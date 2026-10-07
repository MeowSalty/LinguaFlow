package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
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
		field.Int("storage_space_id").Optional().Nillable().Positive(),
		field.Int64("storage_generation").Default(0).NonNegative(),
		field.Int64("output_generation").Default(0).NonNegative(),
		field.String("storage_state").Default("active"),
		field.Int("storage_migration_task_id").Optional().Nillable(),
		field.String("name").NotEmpty(),
		field.Int("owner_user_id").Optional().Nillable().Positive(),
		field.Int("owner_org_id").Optional().Nillable().Positive(),
		field.JSON("config", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }),
		field.Bool("glossary_enabled").Default(false).
			Comment("是否启用术语表"),
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

// Database checks also cover atomic Add mutations, which skip field validators.
func (Project) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Checks: map[string]string{"project_storage_generation_safe": "storage_generation >= 0 AND storage_generation <= 9007199254740991", "project_output_generation_safe": "output_generation >= 0 AND output_generation <= 9007199254740991"}}}
}
