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
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authenticationrecovery"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/reauthenticationproof"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
)

func TestAuthenticationB5RecoveryWriteRollback(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		for _, stage := range []string{"issue_audit", "consume", "restore_audit", "revoke_audit"} {
			t.Run(driver+"/"+stage, func(t *testing.T) {
				clients, services, admin := newAuthenticationRecoveryConcurrencyFixture(t, driver)
				c, svc, ctx := clients[0], services[0], context.Background()
				now := time.Now().UTC()
				c.AuthConnection.Create().SetID("recovery-session-b5").SetKind(authconnection.KindOidc).SetState(authconnection.StateConfiguring).ExecX(ctx)
				chain := c.RefreshSession.Create().SetUserID(admin.ID).SetAuthVersion(admin.AuthVersion).
					SetConnectionID("recovery-session-b5").SetConnectionGeneration(1).SetLoginPolicyRevision(1).
					SetEvidence(authrules.Evidence{ObservedAt: now, ExpiresAt: now.Add(time.Hour)}).SetExpiresAt(now.Add(time.Hour)).SaveX(ctx)
				token := c.RefreshToken.Create().SetUserID(admin.ID).SetSessionID(chain.ID).SetGeneration(0).
					SetTokenHash("recovery-session-token-b5").SetExpiresAt(now.Add(time.Hour)).SaveX(ctx)
				var issued *DeploymentRecoveryIssued
				if stage != "issue_audit" {
					var err error
					issued, err = svc.Issue(ctx, DeploymentRecoveryIssueInput{Username: admin.Username})
					if err != nil {
						t.Fatal(err)
					}
				}
				before := c.User.GetX(ctx, admin.ID)
				policyBefore, err := readPasswordPolicy(ctx, c)
				if err != nil {
					t.Fatal(err)
				}
				audits, authorizations := c.ActivityLog.Query().CountX(ctx), c.AuthenticationRecovery.Query().CountX(ctx)
				failure, fail := errors.New("injected B5 recovery "+stage+" failure"), true
				hook := func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
						if fail {
							return nil, failure
						}
						return next.Mutate(ctx, m)
					})
				}
				if stage == "consume" {
					c.AuthenticationRecovery.Use(hook)
				} else {
					c.ActivityLog.Use(hook)
				}
				retry := func() error {
					switch stage {
					case "issue_audit":
						var err error
						issued, err = svc.Issue(ctx, DeploymentRecoveryIssueInput{Username: admin.Username})
						return err
					case "revoke_audit":
						return svc.Revoke(ctx, issued.Token)
					default:
						return svc.RestoreLocalLogin(ctx, DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "new-password-123"})
					}
				}
				if err := retry(); !errors.Is(err, failure) {
					t.Fatal("recovery write failure ignored", err)
				}
				after := c.User.GetX(ctx, admin.ID)
				policyAfter, err := readPasswordPolicy(ctx, c)
				if err != nil {
					t.Fatal(err)
				}
				if after.AuthVersion != before.AuthVersion || after.PasswordHash != before.PasswordHash || after.PasswordVerifiedHash != before.PasswordVerifiedHash || !reflect.DeepEqual(after.PasswordVerifiedAt, before.PasswordVerifiedAt) || after.Role != before.Role || after.Active != before.Active {
					t.Fatal("failed recovery changed target account")
				}
				if policyAfter.Local.Revision != policyBefore.Local.Revision || policyAfter.Connection.AuthGeneration != policyBefore.Connection.AuthGeneration || !reflect.DeepEqual(localAccountInput(policyAfter.Local), localAccountInput(policyBefore.Local)) {
					t.Fatal("failed recovery changed local policy or source generation")
				}
				chainAfter, tokenAfter := c.RefreshSession.GetX(ctx, chain.ID), c.RefreshToken.GetX(ctx, token.ID)
				if chainAfter.RevokedAt != nil || !chainAfter.Evidence.ObservedAt.Equal(chain.Evidence.ObservedAt) || tokenAfter.RevokedAt != nil || tokenAfter.ConsumedAt != nil {
					t.Fatal("failed recovery revoked existing credentials or erased evidence")
				}
				if c.ActivityLog.Query().CountX(ctx) != audits || c.AuthenticationRecovery.Query().CountX(ctx) != authorizations {
					t.Fatal("failed recovery left partial authorization or audit")
				}
				if issued != nil {
					row := c.AuthenticationRecovery.Query().Where(authenticationrecovery.TokenHashEQ(hash.Full(deploymentRecoveryTokenString(issued.Token)))).OnlyX(ctx)
					if row.ConsumedAt != nil || row.RevokedAt != nil {
						t.Fatal("failed recovery consumed or revoked authorization")
					}
				}
				fail = false
				if err := retry(); err != nil {
					t.Fatal("rollback prevented a valid retry", err)
				}
			})
		}
	}
}

func TestAuthenticationB5PasswordReenableKeepsRevocation(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			f := newB4Fixture(t, driver)
			s, c, ctx := f.services[0], f.clients[0], context.Background()
			external := f.enabled(t)
			initial, err := s.auth.Login(ctx, LoginInput{Identifier: f.admin.Username, Password: "password-123"})
			if err != nil {
				t.Fatal(err)
			}
			proof, _, err := s.Reauthenticate(f.ctx, f.admin.ID, ReauthenticationInput{Method: "password", Password: "password-123", Action: "password_set", TargetID: PasswordConnectionID})
			if err != nil {
				t.Fatal(err)
			}
			p, err := readExternalConnection(ctx, c, external.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			identity := c.ExternalIdentity.Create().SetUserID(f.admin.ID).SetProtocol("oidc").SetAuthority(p.Config.OidcIssuer).SetSubject("reenable-admin-b5").SaveX(ctx)
			c.LoginBinding.Create().SetUserID(f.admin.ID).SetConnectionID(external.ID).SetExternalIdentityID(identity.ID).
				SetLastVerifiedAt(s.now()).SetVerifiedAuthGeneration(p.Connection.AuthGeneration).
				SetVerifiedLoginPolicyRevision(p.Policy.LoginPolicyRevision).SetEvidence(authrules.Evidence{}).ExecX(ctx)
			local, err := s.GetLocalAccount(f.ctx, f.admin.ID)
			if err != nil {
				t.Fatal(err)
			}
			generation := c.AuthConnection.GetX(ctx, PasswordConnectionID).AuthGeneration
			off := local.Policy
			off.PasswordLoginEnabled, off.UsernameLoginEnabled, off.PasswordEnrollmentEnabled, off.Signup.Enabled = false, false, false, false
			closed, err := s.UpdateLocalAccount(f.ctx, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: local.Revision, Policy: off})
			if err != nil {
				t.Fatal(err)
			}
			on := closed.Policy
			on.PasswordLoginEnabled, on.UsernameLoginEnabled = true, true
			actor := WithExternalActor(ctx, f.admin.ID, f.admin.AuthVersion)
			if _, err := s.UpdateLocalAccount(actor, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: closed.Revision, Policy: on}); err != nil {
				t.Fatal(err)
			}
			if c.AuthConnection.GetX(ctx, PasswordConnectionID).AuthGeneration != generation+2 || c.User.GetX(ctx, f.admin.ID).AuthVersion != f.admin.AuthVersion {
				t.Fatal("re-enable must preserve the user version and advance the source generation")
			}
			if _, err := s.auth.Refresh(ctx, initial.RefreshToken); !errors.Is(err, ErrRefreshTokenRevoked) {
				t.Fatal("re-enable revived an old refresh credential", err)
			}
			stored := c.ReauthenticationProof.Query().Where(reauthenticationproof.TokenHashEQ(hash.Full(proof.Proof))).OnlyX(ctx)
			if stored.ConsumedAt == nil {
				t.Fatal("re-enable revived an old password authorization")
			}
			if _, err := s.auth.Login(ctx, LoginInput{Identifier: f.admin.Username, Password: "password-123"}); err != nil {
				t.Fatal("new password login failed after re-enable", err)
			}
		})
	}
}
