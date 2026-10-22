package service

import (
	"context"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
)

// adminSessionContext creates a live administrator refresh session and binds it
// to the context exactly the way production bearer tokens do, for administrators
// created directly instead of through f.login.
func adminSessionContext(t *testing.T, client *ent.Client, admin *ent.User) context.Context {
	t.Helper()
	session := client.RefreshSession.Create().SetUserID(admin.ID).SetAuthVersion(admin.AuthVersion).
		SetConnectionID(PasswordConnectionID).SetConnectionGeneration(1).SetLoginPolicyRevision(1).
		SetExpiresAt(time.Now().Add(time.Hour)).SaveX(context.Background())
	return WithExternalActorSession(context.Background(), admin.ID, admin.AuthVersion, session.ID)
}

// seedB4Connection creates an enabled external connection whose immutable
// snapshot already carries a fresh browser validation, for tests that seed the
// final model directly instead of exercising the management proof flow.
func seedB4Connection(t *testing.T, c *ent.Client, id, issuer string) *ent.AuthConnection {
	t.Helper()
	ctx := context.Background()
	row := c.AuthConnection.Create().SetID(id).SetKind(authconnection.KindOidc).
		SetState(authconnection.StateEnabled).SetCurrentRevision(1).SaveX(ctx)
	c.AuthConnectionRevision.Create().SetConnectionID(id).SetRevision(1).SetDisplayName(id).
		SetOidcIssuer(issuer).SetOidcClientID(id + "-client").
		SetOidcClientAuthMethod(authconnectionrevision.OidcClientAuthMethodClientSecretBasic).
		SetOidcScopes([]string{"openid"}).
		SetFreshSupported(true).SetTestedAt(time.Now().UTC()).ExecX(ctx)
	c.AuthChannelPolicy.Create().SetConnectionID(id).SetRevision(1).SetLoginPolicyRevision(1).
		SetEnabled(true).SetLoginEnabled(true).SetSignupEnabled(true).SetLinkEnabled(true).
		SetLoginRules(authrules.Unrestricted()).SetSignupRules(authrules.Unrestricted()).
		SetLinkRules(authrules.Unrestricted()).ExecX(ctx)
	return row
}

// connectionTestServices adapts the shared B4 fixture for scopes that still
// expect the (services, clients, admin) tuple. It already carries a session-
// bound admin context internally; use newB4Fixture directly for f.test/enable.
func connectionTestServices(t *testing.T, driver string) ([2]*ExternalAuthService, [2]*ent.Client, *ent.User) {
	t.Helper()
	f := newB4Fixture(t, driver)
	return f.services, f.clients, f.admin
}
