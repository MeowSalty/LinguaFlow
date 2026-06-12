package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// TranslationTemplate 翻译模板实体。
// 所有配置（Prompt/Pipeline/Glossary）统一内联为主表字段，单表零子表 edge。
type TranslationTemplate struct {
	ent.Schema
}

func (TranslationTemplate) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (TranslationTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("description").Default(""),
		field.String("scope").Default("user").
			Comment("user / org / system"),
		field.Int("owner_user_id").Optional().Nillable().Positive(),
		field.Int("owner_org_id").Optional().Nillable().Positive(),

		// Prompt 配置（内联，风格/受众等翻译要求直接写在提示词文本中）
		field.Text("system_prompt_content").Default("").
			Comment("内联提示词内容，空则使用内置默认模板"),

		// 管线和术语表配置（内联 JSON）
		field.JSON("pipeline_config", TemplatePipelineConfigData{}).
			Default(DefaultPipelineConfig()).
			Comment("管线配置，JSON 内联存储"),
		field.JSON("glossary_config", TemplateGlossaryConfigData{}).
			Default(DefaultGlossaryConfig()).
			Comment("术语表配置，JSON 内联存储"),
	}
}

func (TranslationTemplate) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner_user", User.Type).
			Ref("translation_templates").
			Field("owner_user_id").Unique(),
		edge.From("owner_org", Organization.Type).
			Ref("translation_templates").
			Field("owner_org_id").Unique(),
	}
}
