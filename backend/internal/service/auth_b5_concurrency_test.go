package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authenticationrecovery"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/loginbinding"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

func TestAuthenticationB5RegistrationPublicationRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
			s, c, ctx := svcs[0], clients[0], context.Background()
			_, input := flowTestConfirm(t, s, protocol, "flow-oidc")
			input.Username = "registration-race-b5"
			start, done := make(chan struct{}), make(chan error, 1)
			go func() {
				<-start
				done <- withOrganizationTransaction(ctx, clients[1], func(tx *ent.Client) error {
					if err := lockAuthenticationConfiguration(ctx, tx); err != nil {
						return err
					}
					current, err := readExternalConnection(ctx, tx, "flow-oidc", 0)
					if err != nil {
						return err
					}
					cfg, policy := *current.Config, policyInput(current.Policy)
					policy.Signup.EmailRequirement = "required"
					policy.SignupRules = b5DomainRule("example.com")
					next := current.Connection.CurrentRevision + 1
					if err := saveExternalSnapshot(ctx, tx, "flow-oidc", next, current.Policy.LoginPolicyRevision, &cfg, policy); err != nil {
						return err
					}
					return tx.AuthConnection.UpdateOneID("flow-oidc").SetCurrentRevision(next).SetAggregateRevision(next).Exec(ctx)
				})
			}()
			close(start)
			result, err := s.Complete(ctx, nil, input)
			if saveErr := <-done; saveErr != nil {
				t.Fatal(saveErr)
			}
			if err != nil && !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatal(err)
			}
			created := c.User.Query().Where(user.UsernameEQ(input.Username)).CountX(ctx)
			if (created == 1) != (err == nil) {
				t.Fatal("registration/policy race left partial user")
			}
			if err == nil {
				if _, err := s.auth.Refresh(ctx, result.Session.RefreshToken); err != nil {
					t.Fatal("signup-only publication revoked winning login", err)
				}
			} else if c.LoginBinding.Query().CountX(ctx) != 0 || c.ExternalIdentity.Query().CountX(ctx) != 0 {
				t.Fatal("losing confirmation left a binding")
			}
		})
	}
}

func TestAuthenticationB5ConfirmationCancellationRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
			s, c, ctx := svcs[0], clients[0], context.Background()
			flow, input := flowTestConfirm(t, s, protocol, "flow-oidc")
			input.Username = "cancel-race-b5"
			start, done := make(chan struct{}), make(chan error, 1)
			go func() {
				<-start
				done <- svcs[1].Cancel(ctx, nil, ExternalCancelInput{TransactionID: flow.TransactionID, Verifier: flowTestVerifier})
			}()
			close(start)
			_, err := s.Complete(ctx, nil, input)
			cancelErr := <-done
			if err != nil && !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatal(err)
			}
			if cancelErr != nil && !errors.Is(cancelErr, ErrExternalFlowInvalid) {
				t.Fatal(cancelErr)
			}
			if (err == nil) == (cancelErr == nil) {
				t.Fatal("cancel and completion must have exactly one winner")
			}
			status := c.AuthTransaction.GetX(ctx, flow.TransactionID).Status
			if err == nil && status != authtransaction.StatusCompleted || cancelErr == nil && status != authtransaction.StatusCancelled {
				t.Fatal("terminal confirmation state did not match the committed winner")
			}
			created := c.User.Query().Where(user.UsernameEQ(input.Username)).CountX(ctx)
			if (created == 1) != (err == nil) {
				t.Fatal("cancel/complete race left partial user")
			}
			if created == 0 && (c.LoginBinding.Query().CountX(ctx) != 0 || c.ExternalIdentity.Query().CountX(ctx) != 0) {
				t.Fatal("cancelled confirmation left identity")
			}
		})
	}
}

func TestAuthenticationB5PasswordDisableAndRestore(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			f := newB4Fixture(t, driver)
			s, c, ctx := f.services[0], f.clients[0], f.ctx
			external := f.enabled(t)
			initial, err := s.auth.Login(context.Background(), LoginInput{Identifier: f.admin.Username, Password: "password-123"})
			if err != nil {
				t.Fatal(err)
			}
			local, err := s.GetLocalAccount(ctx, f.admin.ID)
			if err != nil {
				t.Fatal(err)
			}
			off := local.Policy
			off.PasswordLoginEnabled, off.UsernameLoginEnabled, off.PasswordEnrollmentEnabled, off.Signup.Enabled = false, false, false, false
			impact, err := s.LocalAccountImpact(ctx, f.admin.ID, LocalAccountCandidateInput{ExpectedRevision: local.Revision, Policy: &off})
			if err != nil || impact.CanSave || len(impact.BlockedUsers) != 1 {
				t.Fatal("password disabled without verified fallback", err)
			}
			p, err := readExternalConnection(ctx, c, external.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			identity := c.ExternalIdentity.Create().SetUserID(f.admin.ID).SetProtocol("oidc").SetAuthority(p.Config.OidcIssuer).SetSubject("b5-admin-subject").SaveX(ctx)
			c.LoginBinding.Create().SetUserID(f.admin.ID).SetConnectionID(external.ID).SetExternalIdentityID(identity.ID).SetLastVerifiedAt(s.now()).SetVerifiedAuthGeneration(p.Connection.AuthGeneration).SetVerifiedLoginPolicyRevision(p.Policy.LoginPolicyRevision).SetEvidence(authrules.Evidence{}).ExecX(ctx)
			closed, err := s.UpdateLocalAccount(ctx, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: local.Revision, Policy: off})
			if err != nil || closed.Capabilities.CanLogin || closed.Capabilities.CanEnrollPassword || closed.Capabilities.CanSignup {
				t.Fatal("valid pure external deployment could not disable password", err)
			}
			if _, err := s.auth.Login(context.Background(), LoginInput{Identifier: f.admin.Username, Password: "password-123"}); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatal("disabled password login accepted", err)
			}
			if _, err := s.auth.Refresh(context.Background(), initial.RefreshToken); !errors.Is(err, ErrRefreshTokenRevoked) {
				t.Fatal("disabled password refresh accepted", err)
			}
			if _, _, err := s.Reauthenticate(ctx, f.admin.ID, ReauthenticationInput{Method: "password", Password: "password-123", Action: "password_set", TargetID: PasswordConnectionID}); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatal("disabled password proof accepted", err)
			}
			recovery := NewDeploymentRecoveryService(c)
			issued, err := recovery.Issue(context.Background(), DeploymentRecoveryIssueInput{Username: f.admin.Username})
			if err != nil {
				t.Fatal(err)
			}
			if err := recovery.RestoreLocalLogin(context.Background(), DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "restored-password-123"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.auth.Refresh(context.Background(), initial.RefreshToken); !errors.Is(err, ErrRefreshTokenRevoked) {
				t.Fatal("recovery revived old password session", err)
			}
			if _, err := s.auth.Login(context.Background(), LoginInput{Identifier: f.admin.Username, Password: "restored-password-123"}); err != nil {
				t.Fatal("recovery did not restore password login", err)
			}
			policy, err := readPasswordPolicy(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if !policy.canLogin() || policy.Local.SignupEnabled || policy.Local.PasswordEnrollmentEnabled {
				t.Fatal("recovery changed unrelated local policy")
			}
		})
	}
}

func TestAuthenticationB5SignupLocalDependencyAndMaintenance(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
			s, c, ctx := svcs[0], clients[0], context.Background()
			b5Publish(t, c, func(cfg *ent.AuthConnectionRevision, p *AuthConnectionPolicyInput) {
				p.Signup.PasswordRequirement = "optional"
			})
			_, in := flowTestConfirm(t, s, protocol, "flow-oidc")
			in.Username = "local-dependency-b5"
			if err := withOrganizationTransaction(ctx, c, func(tx *ent.Client) error {
				if err := lockAuthenticationConfiguration(ctx, tx); err != nil {
					return err
				}
				local, err := readPasswordPolicy(ctx, tx)
				if err != nil {
					return err
				}
				candidate := localAccountInput(local.Local)
				candidate.PasswordEnrollmentEnabled = false
				return writeLocalAccountPolicy(ctx, tx, local, candidate, s.now())
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Complete(ctx, nil, in); !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatal("changed enrollment capability admitted confirmation", err)
			}
			if c.User.Query().Where(user.UsernameEQ(in.Username)).ExistX(ctx) {
				t.Fatal("dependency conflict left user")
			}
			b5Publish(t, c, func(cfg *ent.AuthConnectionRevision, p *AuthConnectionPolicyInput) {
				cfg.FreshSupported = false
				p.Signup.PasswordRequirement = "none"
			})
			protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
				return authoidc.Identity{Issuer: "https://issuer.example.test", Subject: "no-maintenance"}, nil
			}
			flowTestStart(t, s, "flow-oidc")
			cb, err := s.HandleCallback(ctx, "flow-oidc", protocol.state, "authorization-code-secret", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Exchange(ctx, nil, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier}); !errors.Is(err, ErrRegistrationClosed) {
				t.Fatal("missing independent maintenance accepted signup", err)
			}
			if c.AuthTransaction.Query().Where(authtransaction.StatusEQ(authtransaction.StatusCompleted)).ExistX(ctx) {
				t.Fatal("rejected signup consumed credential")
			}
		})
	}
}

func TestAuthenticationB5LocalDisableAndUnlinkRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			f := newB4Fixture(t, driver)
			s, c, ctx := f.services[0], f.clients[0], context.Background()
			rec := f.enabled(t)
			p, err := readExternalConnection(ctx, c, rec.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			binding := ruleBindingFixture(t, c, f.admin, rec.ID, s.now(), p.Connection.AuthGeneration)
			proof, _, err := s.Reauthenticate(ctx, f.admin.ID, ReauthenticationInput{Method: "password", Password: "password-123", Action: "unlink", TargetID: strconv.Itoa(binding.ID)})
			if err != nil {
				t.Fatal(err)
			}
			local, err := s.GetLocalAccount(ctx, f.admin.ID)
			if err != nil {
				t.Fatal(err)
			}
			off := local.Policy
			off.PasswordLoginEnabled, off.UsernameLoginEnabled, off.PasswordEnrollmentEnabled, off.Signup.Enabled = false, false, false, false
			results := raceExternalCalls(
				func() error {
					_, e := s.UpdateLocalAccount(ctx, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: local.Revision, Policy: off})
					return e
				},
				func() error { return f.services[1].Unlink(ctx, f.admin.ID, binding.ID, proof.Proof) },
			)
			expectOneExternalWinner(t, results, ErrAuthConnectionConflict, ErrReauthenticationRequired)
			final, err := readPasswordPolicy(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			if !final.canLogin() && !c.LoginBinding.Query().Where(loginbinding.IDEQ(binding.ID)).ExistX(ctx) {
				t.Fatal("local disable and removal committed without a login path")
			}
			if c.User.GetX(ctx, f.admin.ID).PasswordHash == "" {
				t.Fatal("unlink changed password")
			}
		})
	}
}

func TestAuthenticationB5LoginPolicyAndPasswordDeleteRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			f := newB4Fixture(t, driver)
			s, c, ctx := f.services[0], f.clients[0], context.Background()
			rec := b4RulesFixture(t, f, true)
			p, err := readExternalConnection(ctx, c, rec.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			binding := ruleBindingFixture(t, c, f.admin, rec.ID, s.now(), p.Connection.AuthGeneration)
			attrs, err := authrules.ExtractForPurpose("oidc", p.Config.AttributeMappings, "login", map[string]json.RawMessage{"oidc_id_token": json.RawMessage(`{"department":"engineering"}`)}, nil, s.now())
			if err != nil {
				t.Fatal(err)
			}
			identity := c.ExternalIdentity.GetX(ctx, binding.ExternalIdentityID)
			evidence := authrules.Evidence{Issuer: identity.Authority, Subject: identity.Subject, SourceFingerprint: sourceTrustFingerprint(p.Config), LoginPolicyRevision: p.Policy.LoginPolicyRevision, ObservedAt: s.now(), ExpiresAt: s.now().Add(time.Minute), Attributes: attrs}
			c.LoginBinding.UpdateOneID(binding.ID).SetEvidence(evidence).SetVerifiedLoginPolicyRevision(p.Policy.LoginPolicyRevision).ExecX(ctx)
			proof, err := s.issueReauthenticationProof(ctx, c, f.admin, "oidc", rec.ID, "password_delete", PasswordConnectionID, s.now(), evidence)
			if err != nil {
				t.Fatal(err)
			}
			candidate := policyInput(p.Policy)
			candidate.LoginRules.Conditions[0].Value = json.RawMessage(`"finance"`)
			results := raceExternalCalls(
				func() error {
					_, e := s.UpdateConnection(ctx, f.admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, AuthConnectionChanges: AuthConnectionChanges{Policy: &candidate}})
					return e
				},
				func() error { return f.services[1].DeletePassword(ctx, f.admin.ID, proof.Proof) },
			)
			expectOneExternalWinner(t, results, ErrAuthConnectionConflict, ErrReauthenticationRequired, ErrLoginMethodConflict)
			current, err := readExternalConnection(ctx, c, rec.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			account := c.User.GetX(ctx, f.admin.ID)
			if account.PasswordHash == "" {
				result, err := bindingRuleResult(ctx, c, c.LoginBinding.GetX(ctx, binding.ID), current, s.now(), false)
				if err != nil || result.Status != "pass" {
					t.Fatal("password removed without passing external path", err)
				}
			}
		})
	}
}

func TestAuthenticationB5RecoveryAndPolicySaveRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			f := newB4Fixture(t, driver)
			ctx, c := context.Background(), f.clients[0]
			if err := withOrganizationTransaction(ctx, c, func(tx *ent.Client) error {
				if err := lockAuthenticationConfiguration(ctx, tx); err != nil {
					return err
				}
				p, err := readPasswordPolicy(ctx, tx)
				if err != nil {
					return err
				}
				off := localAccountInput(p.Local)
				off.PasswordLoginEnabled, off.UsernameLoginEnabled, off.PasswordEnrollmentEnabled, off.Signup.Enabled = false, false, false, false
				return writeLocalAccountPolicy(ctx, tx, p, off, f.services[0].now())
			}); err != nil {
				t.Fatal(err)
			}
			local, err := f.services[0].GetLocalAccount(ctx, f.admin.ID)
			if err != nil {
				t.Fatal(err)
			}
			recovery := NewDeploymentRecoveryService(f.clients[1])
			issued, err := recovery.Issue(ctx, DeploymentRecoveryIssueInput{Username: f.admin.Username})
			if err != nil {
				t.Fatal(err)
			}
			candidate := local.Policy
			candidate.Signup.EmailRequirement = "required"
			results := raceExternalCalls(
				func() error {
					_, e := f.services[0].UpdateLocalAccount(ctx, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: local.Revision, Policy: candidate})
					return e
				},
				func() error {
					return recovery.RestoreLocalLogin(ctx, DeploymentRecoveryRestoreInput{Token: issued.Token, Password: "recovery-race-123"})
				},
			)
			expectOneExternalWinner(t, results, ErrAuthConnectionConflict, ErrAuthenticationRecoveryConflict)
			p, err := readPasswordPolicy(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			target := c.User.GetX(ctx, f.admin.ID)
			consumed := c.AuthenticationRecovery.Query().Where(authenticationrecovery.UserIDEQ(f.admin.ID), authenticationrecovery.ConsumedAtNotNil()).ExistX(ctx)
			if p.canLogin() != consumed || (target.AuthVersion == f.admin.AuthVersion+1) != consumed {
				t.Fatal("recovery race left partial authorization or policy")
			}
			if !consumed && target.PasswordHash != f.admin.PasswordHash {
				t.Fatal("failed recovery changed password")
			}
		})
	}
}
