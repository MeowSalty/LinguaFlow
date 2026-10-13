package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
)

func TestAuthenticationB3OAuthDraftIdentityCannotReplaceNewBinding(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, admin, protocol := oauthTestServices(t, driver)
			s, c, ctx := svcs[0], clients[0], context.Background()
			rec := oauthTestConnection(t, s, admin, protocol)
			original := *rec.Published.OAuth2
			next := original
			next.ProfileEndpoint = "https://oauth.example.test/another-authority"
			draft, err := s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, OAuth2: &next})
			if err != nil {
				t.Fatal(err)
			}
			cfg := c.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ(rec.ID), authconnectionrevision.RevisionEQ(*draft.DraftRevision)).OnlyX(ctx)
			c.AuthConnectionValidation.Create().SetConnectionID(rec.ID).SetFingerprint(connectionFingerprint(cfg)).SetRevision(cfg.Revision).SetFreshSupported(false).SetActorUserID(admin.ID).SetTestedAt(time.Now()).ExecX(ctx)
			_, confirm := oauthTestConfirmation(t, s, protocol, rec.ID, "subject", "old-source-owner")
			if _, err = s.Complete(ctx, nil, confirm); err != nil {
				t.Fatal(err)
			}
			if _, err = s.auth.Login(ctx, LoginInput{Identifier: confirm.Username, Password: *confirm.InitialPassword}); err != nil {
				t.Fatal(err)
			}
			if _, err = s.PublishConnection(ctx, admin.ID, rec.ID, draft.AggregateRevision); !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatalf("new binding did not freeze publication identity: %v", err)
			}
			if _, err = s.ArchiveConnection(ctx, admin.ID, rec.ID, draft.AggregateRevision); !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatalf("archive published changed identity: %v", err)
			}
			if got := c.AuthConnection.GetX(ctx, rec.ID); *got.PublishedRevision != *rec.PublishedRevision {
				t.Fatal("identity conflict changed published revision")
			}
			// A rejected draft can be repaired back to the bound identity contract.
			if _, err = s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: draft.AggregateRevision, OAuth2: &original}); err != nil {
				t.Fatalf("cannot repair stale draft: %v", err)
			}
		})
	}
}

func TestAuthenticationB3OAuthFreshDowngradeNeedsMaintenanceFallback(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, admin := connectionTestServices(t, driver)
			s, c, ctx := svcs[0], clients[0], context.Background()
			rec := publishedRuleFixture(t, s, c, admin, false)
			owner := c.User.Create().SetUsername("fresh-only-owner").SaveX(ctx)
			ruleBindingFixture(t, c, owner, rec.ID, time.Now())
			draft, err := s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, ClientSecret: json.RawMessage(`"non-fresh-new-secret"`)})
			if err != nil {
				t.Fatal(err)
			}
			cfg := c.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ(rec.ID), authconnectionrevision.RevisionEQ(*draft.DraftRevision)).OnlyX(ctx)
			c.AuthConnectionValidation.Create().SetConnectionID(rec.ID).SetFingerprint(connectionFingerprint(cfg)).SetRevision(cfg.Revision).SetFreshSupported(false).SetActorUserID(admin.ID).SetTestedAt(time.Now()).ExecX(ctx)
			impact, err := s.ImpactConnection(ctx, admin.ID, rec.ID, draft.AggregateRevision)
			if err != nil || impact.CanPublish {
				t.Fatalf("fresh downgrade impact=%+v err=%v", impact, err)
			}
			if _, err = s.PublishConnection(ctx, admin.ID, rec.ID, draft.AggregateRevision); !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatalf("sole maintenance source downgraded: %v", err)
			}
			h, err := hashPassword("password-123")
			if err != nil {
				t.Fatal(err)
			}
			c.User.UpdateOneID(owner.ID).SetPasswordHash(h).ExecX(ctx)
			if _, err = s.auth.Login(ctx, LoginInput{Identifier: owner.Username, Password: "password-123"}); err != nil {
				t.Fatal(err)
			}
			if _, err = s.PublishConnection(ctx, admin.ID, rec.ID, draft.AggregateRevision); err != nil {
				t.Fatalf("verified fallback did not permit downgrade: %v", err)
			}
		})
	}
}

func TestAuthenticationB3OAuthFailedFreshRetestPreservesPublishedCapability(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "expired"}[missing], func(t *testing.T) {
			now := time.Now().UTC()
			svcs, clients, protocol := externalFlowServices(t, dialect.SQLite, now)
			s, c, ctx := svcs[0], clients[0], context.Background()
			admin := createPasswordTestUser(t, c, "retest-admin", "password-123")
			admin = c.User.UpdateOneID(admin.ID).SetRole(SystemRoleAdmin).SaveX(ctx)
			before := c.AuthConnectionValidation.Query().OnlyX(ctx)
			c.AuthConnection.UpdateOneID("flow-oidc").SetDraftRevision(1).ExecX(ctx)
			protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
				i := authoidc.Identity{Issuer: "https://issuer.example.test", Subject: "retest"}
				if !missing {
					i.AuthTime = now.Add(-time.Hour)
				}
				return i, nil
			}
			if _, err := s.StartTest(ctx, admin.ID, "flow-oidc", 1, ExternalBrowserInput{BrowserChallenge: browserChallenge(flowTestVerifier), CompletionURL: "https://app.example.test/auth/complete"}); err != nil {
				t.Fatal(err)
			}
			cb, err := s.HandleCallback(ctx, "flow-oidc", protocol.state, "authorization-code-secret", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Exchange(ctx, &admin.ID, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier}); !errors.Is(err, ErrReauthenticationRequired) {
				t.Fatalf("failed fresh retest was successful: %v", err)
			}
			after := c.AuthConnectionValidation.GetX(ctx, before.ID)
			if !after.FreshSupported || !after.TestedAt.Equal(before.TestedAt) {
				t.Fatal("failed test mutated published capability")
			}
			if conn := c.AuthConnection.GetX(ctx, "flow-oidc"); conn.AuthGeneration != 1 || *conn.PublishedRevision != 1 {
				t.Fatal("failed test changed runtime connection")
			}
		})
	}
}
