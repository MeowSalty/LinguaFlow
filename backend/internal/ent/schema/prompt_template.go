package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// PromptTemplate 提示词模板实体。
type PromptTemplate struct {
	ent.Schema
}

func (PromptTemplate) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (PromptTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("description").Default(""),
		field.String("scope").Default("user").
			Comment("user / org"),
		field.Int("owner_user_id").Optional().Nillable().Positive(),
		field.Int("owner_org_id").Optional().Nillable().Positive(),
		field.Text("system_prompt_content").Default("").
			Comment("翻译提示词内容"),
		field.Text("bootstrap_prompt_content").Default("").
			Comment("Bootstrap 术语抽取提示词内容"),
	}
}

func (PromptTemplate) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner_user", User.Type).
			Ref("prompt_templates").
			Field("owner_user_id").Unique(),
		edge.From("owner_org", Organization.Type).
			Ref("prompt_templates").
			Field("owner_org_id").Unique(),
	}
}
