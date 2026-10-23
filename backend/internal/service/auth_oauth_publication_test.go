package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
)

// B4 freezes the identity contract on first enable, before a binding can be
// created. Rejected candidates must never become current through save or archive.
func TestAuthenticationB4OAuthIdentityCannotReplaceNewBinding(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, admin, protocol := oauthTestServices(t, driver)
			s, c, ctx := svcs[0], clients[0], protocol.adminCtx
			rec := oauthTestConnection(t, s, admin, protocol)
			before := c.AuthConnection.GetX(ctx, rec.ID)
			original := *rec.Configuration.OAuth2
			next := original
			next.ProfileEndpoint = "https://oauth.example.test/another-authority"
			changes := AuthConnectionChanges{OAuth2: &next}
			_, err := s.StartTest(ctx, admin.ID, rec.ID, AuthConnectionTestInput{AuthConnectionCandidateInput: AuthConnectionCandidateInput{ExpectedRevision: rec.AggregateRevision, Changes: &changes}, ExternalBrowserInput: ExternalBrowserInput{BrowserChallenge: browserChallenge(flowTestVerifier), CompletionURL: s.cfg.CompletionURLs[0]}})
			if !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatalf("enabled identity accepted replacement candidate: %v", err)
			}
			_, confirm := oauthTestConfirmation(t, s, protocol, rec.ID, "subject", "old-source-owner")
			if _, err = s.Complete(context.Background(), nil, confirm); err != nil {
				t.Fatal(err)
			}
			if _, err = s.auth.Login(context.Background(), LoginInput{Identifier: confirm.Username, Password: *confirm.InitialPassword}); err != nil {
				t.Fatal(err)
			}
			binding := c.LoginBinding.Query().OnlyX(ctx)
			identity := c.ExternalIdentity.GetX(ctx, binding.ExternalIdentityID)
			_, err = s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, AuthConnectionChanges: changes})
			if !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatalf("new binding identity replaced: %v", err)
			}
			if got := c.AuthConnection.GetX(ctx, rec.ID); got.CurrentRevision != before.CurrentRevision || got.AggregateRevision != before.AggregateRevision || got.IdentityContract != before.IdentityContract {
				t.Fatal("rejected candidate changed current identity or revision")
			}
			// Saving the original contract is a no-op, so rejection is repairable.
			repaired, err := s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, AuthConnectionChanges: AuthConnectionChanges{OAuth2: &original}})
			if err != nil || repaired.AggregateRevision != rec.AggregateRevision {
				t.Fatalf("original identity cannot be retained: %+v, %v", repaired, err)
			}
			archived, err := s.ArchiveConnection(ctx, admin.ID, rec.ID, AuthConnectionActionInput{ExpectedRevision: rec.AggregateRevision})
			if err != nil || !archived.Archived || archived.State != "disabled" || !reflect.DeepEqual(archived.Configuration.OAuth2, &original) {
				t.Fatalf("archive applied rejected identity: %+v, %v", archived, err)
			}
			if got := c.ExternalIdentity.GetX(ctx, identity.ID); got.UserID != identity.UserID || got.Authority != identity.Authority || got.Subject != identity.Subject || c.LoginBinding.GetX(ctx, binding.ID).ExternalIdentityID != identity.ID {
				t.Fatal("archive reassigned the existing binding")
			}
		})
	}
}

func TestAuthenticationB4OAuthFreshDowngradeNeedsMaintenanceFallback(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			f := newB4Fixture(t, driver)
			s, c, ctx := f.services[0], f.clients[0], f.ctx
			rec := f.enabled(t)
			// The downgrade test owns the signup dependency explicitly: only a
			// required local password lets a fresh-only source remain valid when
			// its fresh capability is removed.
			policy := rec.Configuration.Policy
			policy.Signup.PasswordRequirement = "required"
			policyChange := AuthConnectionChanges{Policy: &policy}
			policyProof := f.test(t, rec, &policyChange)
			rec, err := s.UpdateConnection(ctx, f.admin.ID, rec.ID, UpdateAuthConnectionInput{
				ExpectedRevision:      rec.AggregateRevision,
				TestID:                policyProof.TestID,
				Verifier:              b4Verifier,
				AuthConnectionChanges: policyChange,
			})
			if err != nil {
				t.Fatal(err)
			}
			owner := c.User.Create().SetUsername("fresh-only-owner").SaveX(ctx)
			ruleBindingFixture(t, c, owner, rec.ID, s.now(), c.AuthConnection.GetX(ctx, rec.ID).AuthGeneration)
			changes := AuthConnectionChanges{ClientSecret: json.RawMessage(`"non-fresh-new-secret"`)}
			f.protocol.exchange = func(_ context.Context, cfg authoidc.Config, _ authoidc.ExchangeRequest) (authoidc.Identity, error) {
				return authoidc.Identity{Issuer: cfg.Issuer, Subject: "retest"}, nil
			}
			tested := f.test(t, rec, &changes)
			if tested.FreshSupported {
				t.Fatal("missing auth_time advertised fresh capability")
			}
			input := UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, TestID: tested.TestID, Verifier: b4Verifier, AuthConnectionChanges: changes}
			_, err = s.UpdateConnection(ctx, f.admin.ID, rec.ID, input)
			b4Code(t, err, "fallback-required")
			var conflict *AuthConnectionError
			if !errors.As(err, &conflict) || conflict.Impact == nil || conflict.Impact.CanSave || !reflect.DeepEqual(conflict.Impact.BlockedUsers, []int{owner.ID}) {
				t.Fatalf("fresh downgrade omitted maintenance impact: %v", err)
			}
			h, err := hashPassword("password-123")
			if err != nil {
				t.Fatal(err)
			}
			c.User.UpdateOneID(owner.ID).SetPasswordHash(h).ExecX(ctx)
			_, err = s.UpdateConnection(ctx, f.admin.ID, rec.ID, input)
			b4Code(t, err, "fallback-required")
			if _, err = s.auth.Login(context.Background(), LoginInput{Identifier: owner.Username, Password: "password-123"}); err != nil {
				t.Fatal(err)
			}
			saved, err := s.UpdateConnection(ctx, f.admin.ID, rec.ID, input)
			if err != nil || saved.Validation.FreshSupported || saved.Validation.AppliedTestID != tested.TestID || saved.State != "enabled" {
				t.Fatalf("verified fallback did not permit downgrade: %+v, %v", saved, err)
			}
		})
	}
}

func TestAuthenticationB4OAuthFailedFreshRetestPreservesCurrentCapability(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			for _, missing := range []bool{true, false} {
				t.Run(map[bool]string{true: "missing", false: "expired"}[missing], func(t *testing.T) {
					f := newB4Fixture(t, driver)
					s, c, ctx := f.services[0], f.clients[0], f.ctx
					rec := f.enabled(t)
					before := c.AuthConnection.GetX(ctx, rec.ID)
					cfg := c.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ(rec.ID), authconnectionrevision.RevisionEQ(before.CurrentRevision)).OnlyX(ctx)
					f.protocol.exchange = func(_ context.Context, cfg authoidc.Config, _ authoidc.ExchangeRequest) (authoidc.Identity, error) {
						i := authoidc.Identity{Issuer: cfg.Issuer, Subject: "retest"}
						if !missing {
							i.AuthTime = s.now().Add(-time.Hour)
						}
						return i, nil
					}
					// An authentication-only diagnostic is valid in B4. Its failure to
					// prove fresh authentication must not downgrade the saved snapshot.
					tested := f.test(t, rec, nil)
					if tested.FreshSupported {
						t.Fatal("retest without recent auth_time proved fresh authentication")
					}
					after := c.AuthConnectionRevision.GetX(ctx, cfg.ID)
					if !after.FreshSupported || !reflect.DeepEqual(after.TestedAt, cfg.TestedAt) || after.AppliedTestID != cfg.AppliedTestID {
						t.Fatal("diagnostic mutated saved fresh capability")
					}
					if conn := c.AuthConnection.GetX(ctx, rec.ID); conn.AuthGeneration != before.AuthGeneration || conn.CurrentRevision != before.CurrentRevision || conn.AggregateRevision != before.AggregateRevision || conn.State != before.State {
						t.Fatal("retest changed runtime connection")
					}
				})
			}
		})
	}
}
