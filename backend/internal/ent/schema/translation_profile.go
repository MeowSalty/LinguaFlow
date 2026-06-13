package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// TranslationProfile 翻译配置实体。
type TranslationProfile struct {
	ent.Schema
}

func (TranslationProfile) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (TranslationProfile) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("description").Default(""),
		field.String("scope").Default("user").
			Comment("user / org"),
		field.Int("owner_user_id").Optional().Nillable().Positive(),
		field.Int("owner_org_id").Optional().Nillable().Positive(),
		field.JSON("config", TranslationProfileConfigData{}).
			Default(DefaultProfileConfig()).
			Comment("翻译配置，JSON 内联存储"),
	}
}

func (TranslationProfile) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner_user", User.Type).
			Ref("translation_profiles").
			Field("owner_user_id").Unique(),
		edge.From("owner_org", Organization.Type).
			Ref("translation_profiles").
			Field("owner_org_id").Unique(),
	}
}
