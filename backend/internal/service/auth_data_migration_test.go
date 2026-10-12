package service

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
)

func TestAuthenticationB1MigrationPreservesStageA(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			_, clients := concurrentAuthClients(t, driver)
			c, ctx := clients[0], context.Background()
			account := createPasswordTestUser(t, c, "existing-owner", "password-123")
			project := c.Project.Create().SetName("Existing project").SetOwnerUserID(account.ID).SaveX(ctx)
			before, err := readPasswordPolicy(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			c.InstanceInitialization.UpdateOneID(1).SetDataVersion(2).ExecX(ctx)
			for range 2 {
				if err := MigrateData(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			if err := ValidateDataVersion(ctx, c); err != nil {
				t.Fatal(err)
			}
			after, err := readPasswordPolicy(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			current, owned := c.User.GetX(ctx, account.ID), c.Project.GetX(ctx, project.ID)
			if current.Username != account.Username || current.PasswordHash != account.PasswordHash || current.AuthVersion != account.AuthVersion || owned.OwnerUserID == nil || *owned.OwnerUserID != account.ID {
				t.Fatal("upgrade changed existing identity or business ownership")
			}
			if before.Connection.AuthGeneration != after.Connection.AuthGeneration || before.Policy.Revision != after.Policy.Revision {
				t.Fatal("upgrade reset password policy")
			}
			if c.AuthConnection.Query().CountX(ctx) != 1 || c.ExternalIdentity.Query().CountX(ctx) != 0 {
				t.Fatal("upgrade invented third-party identities")
			}
		})
	}
}

func TestAuthenticationB2MigrationPreservesB1AndRejectsDamagedRules(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			_, clients := concurrentAuthClients(t, driver)
			c, ctx := clients[0], context.Background()
			owner := createPasswordTestUser(t, c, "b1-owner", "password-123")
			project := c.Project.Create().SetName("B1 project").SetOwnerUserID(owner.ID).SaveX(ctx)
			chain := c.RefreshSession.Create().SetUserID(owner.ID).SetAuthVersion(owner.AuthVersion).SetConnectionID(PasswordConnectionID).SetConnectionGeneration(1).SetLoginPolicyRevision(1).SetExpiresAt(time.Now().Add(time.Hour)).SaveX(ctx)
			seedExternalFlowConnection(t, c)
			pending := c.AuthTransaction.Create().SetID("b1-pending").SetPurpose(authtransaction.PurposeLogin).SetConnectionID("flow-oidc").SetConnectionRevision(1).SetConnectionGeneration(1).SetPolicyRevision(1).SetStateHash("b1-state").SetNonce("old-encrypted-nonce").SetPkceVerifier("old-encrypted-verifier").SetBrowserChallenge("b1-browser").SetCompletionURL("https://app.example.test/auth/complete").SetExchangeTicketHash("old-ticket").SetExpiresAt(time.Now().Add(time.Minute)).SaveX(ctx)
			c.ReauthenticationProof.Create().SetTokenHash("b1-proof").SetUserID(owner.ID).SetAuthVersion(owner.AuthVersion).SetMethod("password").SetAction("link").SetTargetID("flow-oidc").SetAuthenticatedAt(time.Now()).SetExpiresAt(time.Now().Add(time.Minute)).ExecX(ctx)
			// Reproduce the B1 policy storage, then run the real schema addition.
			for _, column := range []string{"login_rules", "signup_rules", "link_rules"} {
				if _, err := c.ExecContext(ctx, "ALTER TABLE auth_channel_policies DROP COLUMN "+column); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Schema.Create(ctx); err != nil {
				t.Fatal(err)
			}
			c.InstanceInitialization.UpdateOneID(1).SetDataVersion(3).ExecX(ctx)
			for range 2 {
				if err := MigrateData(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			policy, err := readPasswordPolicy(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			if policy.Policy.LoginRules.Mode != "unrestricted" || c.User.GetX(ctx, owner.ID).AuthVersion != owner.AuthVersion || *c.Project.GetX(ctx, project.ID).OwnerUserID != owner.ID || c.RefreshSession.GetX(ctx, chain.ID).RevokedAt != nil {
				t.Fatal("B2 migration changed identity, ownership or unrestricted sessions")
			}
			old := c.AuthTransaction.GetX(ctx, pending.ID)
			if old.Status != authtransaction.StatusCancelled || old.ExchangeTicketHash != nil || c.ReauthenticationProof.Query().CountX(ctx) != 0 || len(c.RefreshSession.GetX(ctx, chain.ID).Evidence.Attributes) != 0 {
				t.Fatal("migration retained short-term credentials or fabricated attribute evidence")
			}
			// Missing typed policy in an already-upgraded DB must fail closed.
			if _, err = c.ExecContext(ctx, "UPDATE auth_channel_policies SET login_rules=NULL"); err != nil {
				t.Fatal(err)
			}
			if _, err = readPasswordPolicy(ctx, c); err == nil {
				t.Fatal("damaged current policy fell back to legacy unrestricted")
			}
		})
	}
}
