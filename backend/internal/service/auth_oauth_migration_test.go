package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
)

func seedOAuthMigrationTransaction(t *testing.T, c *ent.Client, id string, status authtransaction.Status) *ent.AuthTransaction {
	t.Helper()
	now := time.Now().UTC()
	return c.AuthTransaction.Create().SetID(id).SetPurpose(authtransaction.PurposeLogin).SetStatus(status).SetConnectionID("flow-oidc").SetConnectionRevision(1).SetConnectionGeneration(1).SetPolicyRevision(1).SetStateHash(id + "-state").SetNonce("old-encrypted-nonce").SetPkceVerifier("old-encrypted-pkce").SetBrowserChallenge("old-challenge").SetCompletionURL("https://app.example.test/auth/complete").SetIdentityIssuer("https://issuer.example.test").SetIdentitySubject("old-subject").SetEvidence(authrules.Evidence{Issuer: "https://issuer.example.test", Subject: "old-subject", ObservedAt: now, ExpiresAt: now.Add(time.Minute)}).SetExchangeTicketHash(id + "-ticket").SetExchangeTicketExpiresAt(now.Add(time.Minute)).SetConfirmationTokenHash(id + "-confirmation").SetConfirmationExpiresAt(now.Add(time.Minute)).SetExpiresAt(now.Add(time.Minute)).SaveX(context.Background())
}

func TestAuthenticationB3OAuthMigrationPreservesB2(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			_, clients := concurrentAuthClients(t, driver)
			c, ctx, now := clients[0], context.Background(), time.Now().UTC()
			owner := createPasswordTestUser(t, c, "b2-owner", "password-123")
			project := c.Project.Create().SetName("B2 project").SetOwnerUserID(owner.ID).SaveX(ctx)
			seedExternalFlowConnection(t, c)
			c.AuthConnectionValidation.Delete().ExecX(ctx)
			identity := c.ExternalIdentity.Create().SetUserID(owner.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://issuer.example.test").SetSubject("existing-subject").SaveX(ctx)
			binding := c.LoginBinding.Create().SetUserID(owner.ID).SetConnectionID("flow-oidc").SetExternalIdentityID(identity.ID).SetVerifiedAuthGeneration(1).SetVerifiedLoginPolicyRevision(1).SetLastVerifiedAt(now).SaveX(ctx)
			session := c.RefreshSession.Create().SetUserID(owner.ID).SetAuthVersion(owner.AuthVersion).SetConnectionID("flow-oidc").SetConnectionGeneration(1).SetLoginPolicyRevision(1).SetBindingID(binding.ID).SetExpiresAt(now.Add(time.Hour)).SaveX(ctx)
			cfg := c.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ("flow-oidc")).OnlyX(ctx)
			validation := c.AuthConnectionValidation.Create().SetConnectionID("flow-oidc").SetFingerprint(connectionFingerprint(cfg)).SetRevision(1).SetFreshSupported(true).SetTestedAt(now).SetActorUserID(owner.ID).SaveX(ctx)
			// Compare persisted baselines: PostgreSQL stores timestamps at
			// microsecond precision, while creation returns the input time.
			session = c.RefreshSession.GetX(ctx, session.ID)
			validation = c.AuthConnectionValidation.GetX(ctx, validation.ID)
			c.ReauthenticationProof.Create().SetTokenHash("old-proof").SetUserID(owner.ID).SetAuthVersion(owner.AuthVersion).SetMethod("password").SetAction("link").SetTargetID("flow-oidc").SetAuthenticatedAt(now).SetExpiresAt(now.Add(time.Minute)).ExecX(ctx)
			var pending []*ent.AuthTransaction
			for _, status := range []authtransaction.Status{authtransaction.StatusCreated, authtransaction.StatusCallbackProcessing, authtransaction.StatusCallbackVerified, authtransaction.StatusAwaitingConfirmation} {
				pending = append(pending, seedOAuthMigrationTransaction(t, c, string(status), status))
			}
			completed := seedOAuthMigrationTransaction(t, c, "completed", authtransaction.StatusCompleted)
			// B2 transactions had no protocol column. Exercise the actual schema
			// addition before the version-5 data backfill and cancellation.
			if _, err := c.ExecContext(ctx, "ALTER TABLE auth_transactions DROP COLUMN identity_protocol"); err != nil {
				t.Fatal(err)
			}
			if err := c.Schema.Create(ctx); err != nil {
				t.Fatal(err)
			}
			c.InstanceInitialization.UpdateOneID(1).SetDataVersion(4).ExecX(ctx)
			if err := MigrateData(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := ValidateDataVersion(ctx, c); err != nil {
				t.Fatal(err)
			}
			if c.InstanceInitialization.GetX(ctx, 1).DataVersion != 5 {
				t.Fatal("migration did not advance 4 to 5")
			}
			after := c.User.GetX(ctx, owner.ID)
			if after.Username != owner.Username || after.PasswordHash != owner.PasswordHash || after.AuthVersion != owner.AuthVersion || *c.Project.GetX(ctx, project.ID).OwnerUserID != owner.ID {
				t.Fatal("migration changed account or business ownership")
			}
			currentIdentity := c.ExternalIdentity.GetX(ctx, identity.ID)
			currentBinding := c.LoginBinding.GetX(ctx, binding.ID)
			if currentIdentity.UserID != identity.UserID || currentIdentity.Protocol != identity.Protocol || currentIdentity.Authority != identity.Authority || currentIdentity.Subject != identity.Subject || currentBinding.ExternalIdentityID != binding.ExternalIdentityID || currentBinding.ConnectionID != binding.ConnectionID {
				t.Fatal("migration reassigned existing external identity")
			}
			currentSession := c.RefreshSession.GetX(ctx, session.ID)
			if currentSession.RevokedAt != nil || currentSession.AuthVersion != session.AuthVersion || currentSession.ConnectionGeneration != session.ConnectionGeneration || !currentSession.ExpiresAt.Equal(session.ExpiresAt) {
				t.Fatal("migration invalidated existing session")
			}
			currentValidation := c.AuthConnectionValidation.GetX(ctx, validation.ID)
			if currentValidation.Fingerprint != validation.Fingerprint || !currentValidation.FreshSupported || !currentValidation.TestedAt.Equal(validation.TestedAt) {
				t.Fatal("migration lost tested OIDC fresh capability")
			}
			for _, old := range pending {
				row := c.AuthTransaction.GetX(ctx, old.ID)
				if row.Status != authtransaction.StatusCancelled || row.FinishedAt == nil || row.IdentityProtocol != authtransaction.IdentityProtocolOidc || row.ExchangeTicketHash != nil || row.ExchangeTicketExpiresAt != nil || row.ConfirmationTokenHash != nil || row.ConfirmationExpiresAt != nil || !row.Evidence.ObservedAt.IsZero() {
					t.Fatalf("migration retained usable short-term credentials: %+v", row)
				}
			}
			if c.ReauthenticationProof.Query().CountX(ctx) != 0 || c.AuthTransaction.GetX(ctx, completed.ID).Status != authtransaction.StatusCompleted {
				t.Fatal("migration mishandled proof or terminal state")
			}
			beforeRepeat := c.AuthTransaction.GetX(ctx, pending[0].ID)
			if err := MigrateData(ctx, c); err != nil {
				t.Fatal(err)
			}
			afterRepeat := c.AuthTransaction.GetX(ctx, pending[0].ID)
			if !reflect.DeepEqual(beforeRepeat.FinishedAt, afterRepeat.FinishedAt) || beforeRepeat.Status != afterRepeat.Status || c.AuthConnectionValidation.Query().CountX(ctx) != 1 {
				t.Fatal("repeated migration changed stable data")
			}
		})
	}
}

func TestAuthenticationB3OAuthMigrationRollback(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			_, clients := concurrentAuthClients(t, driver)
			c, ctx := clients[0], context.Background()
			owner := createPasswordTestUser(t, c, "migration-owner", "password-123")
			seedExternalFlowConnection(t, c)
			pending := seedOAuthMigrationTransaction(t, c, "rollback-pending", authtransaction.StatusAwaitingConfirmation)
			c.ReauthenticationProof.Create().SetTokenHash("rollback-proof").SetUserID(owner.ID).SetAuthVersion(owner.AuthVersion).SetMethod("password").SetAction("link").SetTargetID("flow-oidc").SetAuthenticatedAt(time.Now()).SetExpiresAt(time.Now().Add(time.Minute)).ExecX(ctx)
			c.InstanceInitialization.UpdateOneID(1).SetDataVersion(4).ExecX(ctx)
			injected, fail := errors.New("injected final marker failure"), true
			c.InstanceInitialization.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
					if fail {
						return nil, injected
					}
					return next.Mutate(ctx, m)
				})
			})
			if err := MigrateData(ctx, c); !errors.Is(err, injected) {
				t.Fatalf("migration failure=%v", err)
			}
			row := c.AuthTransaction.GetX(ctx, pending.ID)
			if c.InstanceInitialization.GetX(ctx, 1).DataVersion != 4 || row.Status != pending.Status || row.FinishedAt != nil || row.IdentityProtocol != "" || row.Evidence.ObservedAt.IsZero() || row.ExchangeTicketHash == nil || row.ConfirmationTokenHash == nil || c.ReauthenticationProof.Query().CountX(ctx) != 1 {
				t.Fatal("failed migration committed cancellation or proof deletion")
			}
			fail = false
			if err := MigrateData(ctx, c); err != nil {
				t.Fatal(err)
			}
			if c.InstanceInitialization.GetX(ctx, 1).DataVersion != 5 || c.AuthTransaction.GetX(ctx, pending.ID).Status != authtransaction.StatusCancelled || c.ReauthenticationProof.Query().CountX(ctx) != 0 {
				t.Fatal("migration could not resume after rollback")
			}
		})
	}
}
