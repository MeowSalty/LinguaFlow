package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// BootstrapPromptTemplate 术语抽取提示词模板实体。
type BootstrapPromptTemplate struct {
	ent.Schema
}

func (BootstrapPromptTemplate) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (BootstrapPromptTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("description").Default(""),
		field.String("scope").Default("user").
			Comment("user / org"),
		field.Int("owner_user_id").Optional().Nillable().Positive(),
		field.Int("owner_org_id").Optional().Nillable().Positive(),
		field.Text("content").Default("").
			Comment("术语抽取提示词内容"),
	}
}

func (BootstrapPromptTemplate) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner_user", User.Type).
			Ref("bootstrap_prompt_templates").
			Field("owner_user_id").Unique(),
		edge.From("owner_org", Organization.Type).
			Ref("bootstrap_prompt_templates").
			Field("owner_org_id").Unique(),
	}
}
