package database

import (
	"fmt"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
)

func TestAuthenticationAndMembershipIndexPlans(t *testing.T) {
	for _, name := range []string{"sqlite", "postgres"} {
		t.Run(name, func(t *testing.T) {
			_, c := observedSchemaFixture(t, name)
			ctx := t.Context()
			now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
			users := make([]*ent.User, 64)
			organizations := make([]*ent.Organization, 64)
			for i := range users {
				users[i] = c.User.Create().SetUsername(fmt.Sprintf("plan_user_%02d", i)).SaveX(ctx)
				organizations[i] = c.Organization.Create().SetName(fmt.Sprintf("plan_org_%02d", i)).SetSlug(fmt.Sprintf("plan-org-%02d", i)).SaveX(ctx)
			}
			connections := make([]string, 32)
			for i := range connections {
				connections[i] = fmt.Sprintf("plan_connection_%02d", i)
				c.AuthConnection.Create().SetID(connections[i]).SetKind(authconnection.KindOidc).SetState(authconnection.StateDisabled).SaveX(ctx)
			}
			var identities, bindings, sessions, flows, proofs, memberships [][]any
			for i := 0; i < 2048; i++ {
				id := i + 1
				actor := users[i%64].ID
				connection := connections[i/64]
				expiry := now.Add(24 * time.Hour)
				if i%128 == 0 {
					expiry = now.Add(-time.Second)
				}
				var evidence, projected, revoked any
				if i%8 == 0 || i%173 == 0 {
					evidence = fmt.Sprintf(`{"expires_at":%q}`, expiry.UTC().Format(time.RFC3339Nano))
					projected = expiry
				}
				if i%173 == 0 {
					revoked = now.Add(-time.Second)
				}
				identities = append(identities, []any{id, now, now, actor, "oidc", connection, fmt.Sprintf("fixture-%d", id)})
				bindings = append(bindings, []any{id, now, now, actor, connection, id, evidence, projected})
				sessions = append(sessions, []any{id, now, now, actor, int64(1), connection, id, int64(1), int64(1), evidence, expiry, revoked})
				flows = append(flows, []any{fmt.Sprintf("flow-%d", id), now, now, "login", connection, int64(1), int64(1), int64(1), fmt.Sprintf("state-%d", id), "fixture", "fixture", "fixture", "/auth/complete", expiry})
				proofs = append(proofs, []any{id, now, now, fmt.Sprintf("proof-%d", id), actor, int64(1), "password", "unlink", "fixture", now, expiry})
			}
			for i, organization := range organizations {
				for j, user := range users {
					memberships = append(memberships, []any{i*64 + j + 1, now, now, organization.ID, user.ID})
				}
			}
			insertPlanRows(t, c, "external_identities", []string{"id", "created_at", "updated_at", "user_id", "protocol", "authority", "subject"}, identities)
			insertPlanRows(t, c, "login_bindings", []string{"id", "created_at", "updated_at", "user_id", "connection_id", "external_identity_id", "evidence", "evidence_expires_at"}, bindings)
			insertPlanRows(t, c, "refresh_sessions", []string{"id", "created_at", "updated_at", "user_id", "auth_version", "connection_id", "binding_id", "connection_generation", "login_policy_revision", "evidence", "expires_at", "revoked_at"}, sessions)
			insertPlanRows(t, c, "auth_transactions", []string{"id", "created_at", "updated_at", "purpose", "connection_id", "connection_revision", "connection_generation", "policy_revision", "state_hash", "nonce", "pkce_verifier", "browser_challenge", "completion_url", "expires_at"}, flows)
			insertPlanRows(t, c, "reauthentication_proofs", []string{"id", "created_at", "updated_at", "token_hash", "user_id", "auth_version", "method", "action", "target_id", "authenticated_at", "expires_at"}, proofs)
			insertPlanRows(t, c, "org_memberships", []string{"id", "created_at", "updated_at", "organization_memberships", "user_memberships"}, memberships)
			if _, err := c.ExecContext(ctx, "ANALYZE"); err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				label, query string
				args         []any
			}{
				{"binding_expiry", "SELECT id,connection_id,user_id,evidence_expires_at FROM login_bindings WHERE evidence_expires_at<=$1 ORDER BY evidence_expires_at,id LIMIT 64", []any{now}},
				{"session_expiry", "SELECT id FROM refresh_sessions WHERE evidence IS NOT NULL AND expires_at<=$1 ORDER BY expires_at,id LIMIT 256", []any{now}},
				{"session_revoked", "SELECT id FROM refresh_sessions WHERE evidence IS NOT NULL AND revoked_at IS NOT NULL ORDER BY revoked_at,id LIMIT 256", nil},
				{"flow_expiry", "SELECT id FROM auth_transactions WHERE expires_at<=$1 ORDER BY expires_at,id LIMIT 256", []any{now}},
				{"proof_expiry", "SELECT id FROM reauthentication_proofs WHERE expires_at<=$1 ORDER BY expires_at,id LIMIT 256", []any{now}},
			} {
				t.Logf("fixture=%s auth_rows=2048 case=%s plan=%s", name, test.label, explainPlan(t, c, name, test.query, test.args...))
			}
			query := "SELECT organization_memberships FROM org_memberships WHERE user_memberships=$1"
			t.Logf("fixture=%s case=actor_org generated_source_plan=%s", name, explainPlan(t, c, name, query, users[7].ID))
			if _, err := c.ExecContext(ctx, "DROP INDEX IF EXISTS orgmembership_user_memberships_organization_memberships"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.ExecContext(ctx, "ANALYZE"); err != nil {
				t.Fatal(err)
			}
			before := explainPlan(t, c, name, query, users[7].ID)
			if _, err := c.ExecContext(ctx, "CREATE INDEX plan_membership_user_org ON org_memberships(user_memberships,organization_memberships)"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.ExecContext(ctx, "ANALYZE"); err != nil {
				t.Fatal(err)
			}
			t.Logf("fixture=%s membership_rows=4096 case=actor_org baseline=%s candidate=%s", name, before, explainPlan(t, c, name, query, users[7].ID))
		})
	}
}
