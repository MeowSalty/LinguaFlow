package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
)

// LocalAccountPolicy is the authoritative singleton for local credentials.
// The password AuthConnection remains only an internal session source.
type LocalAccountPolicy struct{ ent.Schema }

func (LocalAccountPolicy) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (LocalAccountPolicy) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").Default(1).Min(1).Max(1).Immutable(),
		field.Int64("revision").Default(1).Positive(),
		field.Int64("login_policy_revision").Default(1).Positive(),
		field.Int64("signup_policy_revision").Default(1).Positive(),
		field.Int64("enrollment_policy_revision").Default(1).Positive(),
		field.Bool("password_login_enabled").Default(true),
		field.Bool("username_login_enabled").Default(true),
		field.Bool("email_login_enabled").Default(false),
		field.Bool("password_enrollment_enabled").Default(true),
		field.Bool("signup_enabled").Default(true),
		field.Enum("signup_email_requirement").Values("optional", "required").Default("optional"),
		field.JSON("signup_rules", authrules.RuleSet{}),
	}
}
func (LocalAccountPolicy) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Checks: map[string]string{
		"local_account_singleton":      "id = 1",
		"local_account_revisions_safe": "revision BETWEEN 1 AND 9007199254740991 AND login_policy_revision BETWEEN 1 AND 9007199254740991 AND signup_policy_revision BETWEEN 1 AND 9007199254740991 AND enrollment_policy_revision BETWEEN 1 AND 9007199254740991",
	}}}
}
