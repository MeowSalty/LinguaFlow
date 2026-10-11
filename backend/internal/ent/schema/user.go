package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/MeowSalty/LinguaFlow/backend/internal/accountidentity"
)

type User struct {
	ent.Schema
}

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{TimeMixin{}}
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("username").NotEmpty().Unique().Immutable().Validate(accountidentity.ValidateUsername),
		field.String("password_hash").NotEmpty().Optional().Sensitive(),
		field.String("email").NotEmpty().Optional(),
		field.Time("email_verified_at").Optional().Nillable(),
		field.String("email_verification_source").NotEmpty().Optional(),
		field.String("verified_email_key").NotEmpty().Optional().Unique(),
		field.Int64("auth_version").Default(1).Positive(),
		field.String("display_name").Optional(),
		field.String("role").Default("user"),
		field.Bool("active").Default(true),
	}
}

func (User) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Checks: map[string]string{
		"user_auth_version_safe":             "auth_version >= 1 AND auth_version <= 9007199254740991",
		"user_email_verification_consistent": "((verified_email_key IS NULL AND email_verified_at IS NULL AND email_verification_source IS NULL) OR (email IS NOT NULL AND verified_email_key IS NOT NULL AND verified_email_key = email AND email_verified_at IS NOT NULL AND email_verification_source IS NOT NULL AND email_verification_source <> ''))",
	}}}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("created_jobs", Job.Type),
		edge.To("reviewed_segments", Segment.Type),
		edge.To("refresh_tokens", RefreshToken.Type),
		edge.To("refresh_sessions", RefreshSession.Type),
		edge.To("external_identities", ExternalIdentity.Type),
		edge.To("login_bindings", LoginBinding.Type),
		edge.To("memberships", OrgMembership.Type),
		edge.To("backends", Backend.Type),
		edge.To("owned_projects", Project.Type),
		edge.To("activity_logs", ActivityLog.Type),
		edge.To("usage_records", UsageRecord.Type),
		edge.To("translation_prompt_templates", TranslationPromptTemplate.Type),
		edge.To("bootstrap_prompt_templates", BootstrapPromptTemplate.Type),
		edge.To("prune_prompt_templates", PrunePromptTemplate.Type),
		edge.To("execution_profiles", ExecutionProfile.Type),
		edge.To("execution_plan_templates", ExecutionPlanTemplate.Type),
		edge.To("sync_tasks", SyncTask.Type),
	}
}
