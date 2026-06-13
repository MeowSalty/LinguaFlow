package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type Organization struct {
	ent.Schema
}

func (Organization) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (Organization) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().Unique(),
		field.String("slug").NotEmpty().Unique(),
		field.String("display_name").Optional(),
		field.String("description").Optional(),
	}
}

func (Organization) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("projects", Project.Type),
		edge.To("memberships", OrgMembership.Type),
		edge.To("org_backends", OrgBackend.Type),
		edge.To("glossary_entries", GlossaryEntry.Type),
		edge.To("tm_entries", TMEntry.Type),
		edge.To("activity_logs", ActivityLog.Type),
		edge.To("usage_records", UsageRecord.Type),
		edge.To("prompt_templates", PromptTemplate.Type),
		edge.To("translation_profiles", TranslationProfile.Type),
	}
}
