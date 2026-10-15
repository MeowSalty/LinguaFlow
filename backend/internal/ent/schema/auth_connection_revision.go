package schema

import (
	"context"
	"fmt"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
)

// Revisions are immutable snapshots. The connection's current_revision points to
// the single saved runtime configuration; each snapshot records whether it was
// browser-validated at save time.
type AuthConnectionRevision struct{ ent.Schema }

func (AuthConnectionRevision) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }
func (AuthConnectionRevision) Fields() []ent.Field {
	return []ent.Field{
		field.String("connection_id").NotEmpty().Immutable(),
		field.Int64("revision").Positive().Immutable(),
		field.String("display_name").NotEmpty().Immutable(),
		field.String("description").Default("").Immutable(),
		field.Int("display_order").Default(0).Immutable(),
		field.Int("config_version").Default(1).Positive().Immutable(),
		field.JSON("attribute_mappings", []authrules.Field{}).Optional().Immutable(),
		field.Int("evidence_max_age_seconds").Default(3600).Positive().Immutable(),
		field.String("oauth2_authorization_endpoint").Optional().Immutable(),
		field.String("oauth2_token_endpoint").Optional().Immutable(),
		field.String("oauth2_profile_endpoint").Optional().Immutable(),
		field.String("oauth2_client_id").Optional().Immutable(),
		field.String("oauth2_subject_pointer").Optional().Immutable(),
		field.Enum("oauth2_client_auth_method").Values("client_secret_basic", "client_secret_post").Optional().Immutable(),
		field.Enum("oauth2_subject_type").Values("string", "integer").Optional().Immutable(),
		field.Strings("oauth2_scopes").Optional().Immutable(),
		field.String("oidc_issuer").Optional().Immutable(),
		field.String("oidc_client_id").Optional().Immutable(),
		field.Enum("oidc_client_auth_method").Values("client_secret_basic", "client_secret_post").Optional().Immutable(),
		field.Strings("oidc_scopes").Optional().Immutable(),
		field.Bool("oidc_userinfo_enabled").Default(false).Immutable(),
		field.Int64("secret_version").Optional().Nillable().Positive().Immutable(),
		field.Bytes("logo_data").Optional().Sensitive().Immutable(),
		field.String("logo_media_type").Optional().Immutable(),
		field.Bool("username_login_enabled").Default(false).Immutable(),
		field.Bool("email_login_enabled").Default(false).Immutable(),
		field.Bool("fresh_supported").Default(false).Immutable(),
		field.Time("tested_at").Optional().Nillable().Immutable(),
		field.String("applied_test_id").Optional().Immutable(),
	}
}
func (AuthConnectionRevision) Edges() []ent.Edge {
	return []ent.Edge{edge.From("connection", AuthConnection.Type).Ref("revisions").Field("connection_id").Unique().Required().Immutable()}
}
func (AuthConnectionRevision) Indexes() []ent.Index {
	return []ent.Index{index.Fields("connection_id", "revision").Unique()}
}
func (AuthConnectionRevision) Hooks() []ent.Hook { return immutableAuthSnapshotHooks() }

func immutableAuthSnapshotHooks() []ent.Hook {
	return []ent.Hook{func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if m.Op().Is(ent.OpUpdate | ent.OpUpdateOne) {
				return nil, fmt.Errorf("authentication snapshots are immutable; create a new revision")
			}
			return next.Mutate(ctx, m)
		})
	}}
}
