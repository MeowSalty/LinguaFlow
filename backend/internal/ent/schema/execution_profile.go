package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// ExecutionProfile 执行策略配置实体。
type ExecutionProfile struct {
	ent.Schema
}

func (ExecutionProfile) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (ExecutionProfile) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("description").Default(""),
		field.String("scope").Default("user").
			Comment("user / org"),
		field.Int("owner_user_id").Optional().Nillable().Positive(),
		field.Int("owner_org_id").Optional().Nillable().Positive(),
		field.JSON("config", ExecutionProfileConfigData{}).
			Default(DefaultProfileConfig()).
			Comment("执行策略配置，JSON 内联存储"),
	}
}

func (ExecutionProfile) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner_user", User.Type).
			Ref("execution_profiles").
			Field("owner_user_id").Unique(),
		edge.From("owner_org", Organization.Type).
			Ref("execution_profiles").
			Field("owner_org_id").Unique(),
	}
}
