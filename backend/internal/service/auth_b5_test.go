package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/reauthenticationproof"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
)

func b5DomainRule(domain string) authrules.RuleSet {
	value, _ := json.Marshal(domain)
	return authrules.RuleSet{Mode: "conditions", Match: "all", Conditions: []authrules.Condition{{FieldID: "registration.email_domain", Operator: "eq", Value: value}}}
}

func b5Publish(t *testing.T, c *ent.Client, change func(*ent.AuthConnectionRevision, *AuthConnectionPolicyInput)) {
	t.Helper()
	ctx := context.Background()
	if err := withOrganizationTransaction(ctx, c, func(tx *ent.Client) error {
		if err := lockAuthenticationConfiguration(ctx, tx); err != nil {
			return err
		}
		current, err := readExternalConnection(ctx, tx, "flow-oidc", 0)
		if err != nil {
			return err
		}
		cfg, policy := *current.Config, policyInput(current.Policy)
		change(&cfg, &policy)
		next := current.Connection.CurrentRevision + 1
		if err := saveExternalSnapshot(ctx, tx, "flow-oidc", next, current.Policy.LoginPolicyRevision, &cfg, policy); err != nil {
			return err
		}
		return tx.AuthConnection.UpdateOneID("flow-oidc").SetCurrentRevision(next).SetAggregateRevision(next).Exec(ctx)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticationB5LocalPolicyAndSignup(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			f := newB4Fixture(t, driver)
			s, c := f.services[0], f.clients[0]
			current, err := s.GetLocalAccount(f.ctx, f.admin.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !current.Policy.Signup.Enabled || !current.Capabilities.CanLogin || !current.Capabilities.CanEnrollPassword || current.Policy.Signup.EmailRequirement != "optional" {
				t.Fatal("incorrect local defaults")
			}
			if _, err := s.GetLocalAccount(context.Background(), createTestUser(t, c, "plain-user").ID); !errors.Is(err, ErrForbidden) {
				t.Fatalf("unprivileged local policy: %v", err)
			}
			noChange, err := s.UpdateLocalAccount(f.ctx, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: current.Revision, Policy: current.Policy})
			if err != nil || noChange.Revision != current.Revision {
				t.Fatal("no-op changed revision", err)
			}
			if _, err := s.UpdateLocalAccount(f.ctx, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: current.Revision + 1, Policy: current.Policy}); !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatalf("stale local revision: %v", err)
			}
			policy := current.Policy
			policy.EmailLoginEnabled = true
			if _, err := s.UpdateLocalAccount(f.ctx, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: current.Revision, Policy: policy}); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("unsupported email login: %v", err)
			}
			policy = current.Policy
			policy.PasswordEnrollmentEnabled = false
			saved, err := s.UpdateLocalAccount(f.ctx, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: current.Revision, Policy: policy})
			if err != nil || saved.Capabilities.CanEnrollPassword {
				t.Fatal("password enrollment independence", err)
			}
			registered, err := s.auth.Register(context.Background(), RegisterInput{Username: "local-without-enrollment", Password: "password-123"})
			if err != nil {
				t.Fatalf("enrollment disabled blocked fixed local registration: %v", err)
			}
			policy = saved.Policy
			policy.Signup.Enabled = false
			saved, err = s.UpdateLocalAccount(f.ctx, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: saved.Revision, Policy: policy})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.auth.Login(context.Background(), LoginInput{Identifier: registered.User.Username, Password: "password-123"}); err != nil {
				t.Fatal("signup change blocked password login", err)
			}
			if _, err := s.auth.Refresh(context.Background(), registered.RefreshToken); err != nil {
				t.Fatal("signup change revoked established session", err)
			}
			if _, err := s.auth.Register(context.Background(), RegisterInput{Username: "closed-local", Password: "password-123"}); !errors.Is(err, ErrRegistrationClosed) {
				t.Fatal("closed local signup accepted", err)
			}
			policy.Signup.Enabled, policy.Signup.EmailRequirement = true, "required"
			policy.Signup.Rules = b5DomainRule("example.com")
			saved, err = s.UpdateLocalAccount(f.ctx, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: saved.Revision, Policy: policy})
			if err != nil {
				t.Fatal(err)
			}
			for i, tc := range []struct {
				email string
				want  error
			}{{"", ErrInvalidInput}, {"invalid", ErrInvalidInput}, {"a@[127.0.0.1]", ErrInvalidInput}, {"a@other.com", ErrAuthenticationRuleDenied}, {"a@example.com.evil.org", ErrAuthenticationRuleDenied}, {"A@EXAMPLE.COM", nil}} {
				_, err := s.auth.Register(context.Background(), RegisterInput{Username: fmt.Sprintf("local-matrix-%d", i), Password: "password-123", Email: tc.email})
				if !errors.Is(err, tc.want) {
					t.Fatalf("email %q: got %v want %v", tc.email, err, tc.want)
				}
			}
			policy.Signup.EmailRequirement = "optional"
			if _, err := s.UpdateLocalAccount(f.ctx, f.admin.ID, LocalAccountPatchInput{ExpectedRevision: saved.Revision, Policy: policy}); !errors.Is(err, ErrInvalidInput) {
				t.Fatal("optional domain input accepted", err)
			}
		})
	}
}

func TestAuthenticationB5ExternalSignupMatrix(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			for _, tc := range []struct {
				policy   string
				password *string
				want     error
			}{{"none", nil, nil}, {"none", ptrB5("password-123"), ErrInvalidInput}, {"optional", nil, nil}, {"optional", ptrB5(""), ErrInvalidInput}, {"optional", ptrB5("password-123"), nil}, {"required", nil, ErrInvalidInput}, {"required", ptrB5("short"), ErrInvalidInput}, {"required", ptrB5("password-123"), nil}} {
				t.Run(tc.policy+fmt.Sprint(tc.password != nil), func(t *testing.T) {
					svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
					s, c, ctx := svcs[0], clients[0], context.Background()
					b5Publish(t, c, func(cfg *ent.AuthConnectionRevision, p *AuthConnectionPolicyInput) {
						p.Signup = SignupPolicyRequirements{EmailRequirement: "required", PasswordRequirement: tc.policy}
					})
					protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
						return authoidc.Identity{Issuer: "https://issuer.example.test", Subject: "b5-signup-subject", PreferredUsername: "provider-name", Email: "suggestion@provider.com"}, nil
					}
					flowTestStart(t, s, "flow-oidc")
					cb, err := s.HandleCallback(ctx, "flow-oidc", protocol.state, "authorization-code-secret", "")
					if err != nil {
						t.Fatal(err)
					}
					x, err := s.Exchange(ctx, nil, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier})
					if err != nil || x.Confirmation == nil {
						t.Fatal("signup did not reach confirmation", err)
					}
					if x.Confirmation.Signup == nil || x.Confirmation.Signup.PasswordRequirement != tc.policy || x.Confirmation.UsernameCandidate != "provider-name" || x.Confirmation.SignupRequirements[0] != "email" {
						t.Fatal("confirmation requirements or candidate missing")
					}
					before := c.User.Query().CountX(ctx)
					out, err := s.Complete(ctx, nil, ExternalCompleteInput{ConfirmationToken: x.Confirmation.ConfirmationToken, Verifier: flowTestVerifier, Username: "b5-created", Email: ptrB5("final@example.com"), InitialPassword: tc.password})
					if !errors.Is(err, tc.want) {
						t.Fatalf("policy %s: got %v want %v", tc.policy, err, tc.want)
					}
					if err != nil {
						if c.User.Query().CountX(ctx) != before || c.ExternalIdentity.Query().CountX(ctx) != 0 {
							t.Fatal("failed signup left partial state")
						}
						return
					}
					account := c.User.GetX(ctx, out.Session.User.ID)
					if account.Email != "final@example.com" || account.VerifiedEmailKey != "" || account.Role != SystemRoleUser || (account.PasswordHash != "") != (tc.password != nil) {
						t.Fatal("registration mutated credential ownership or ignored explicit password requirement")
					}
				})
			}
		})
	}
}

func ptrB5(value string) *string { return &value }

func TestAuthenticationB5InvalidSignupDataPrecedesAdmission(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
			s, c, ctx := svcs[0], clients[0], context.Background()
			b5Publish(t, c, func(cfg *ent.AuthConnectionRevision, p *AuthConnectionPolicyInput) {
				p.Signup.EmailRequirement = "required"
				p.SignupRules = b5DomainRule("example.com")
			})
			flow, input := flowTestConfirm(t, s, protocol, "flow-oidc")
			users := c.User.Query().CountX(ctx)
			for _, tc := range []struct {
				username, email string
				want            error
			}{
				{"", "owner@other.com", ErrInvalidInput},
				{"xx", "owner@other.com", ErrInvalidInput},
				{"bad!name", "owner@other.com", ErrInvalidInput},
				{"valid-b5-data", "", ErrInvalidInput},
				{"valid-b5-data", "invalid", ErrInvalidInput},
				{"valid-b5-data", "owner@other.com", ErrAuthenticationRuleDenied},
			} {
				input.Username, input.Email = tc.username, ptrB5(tc.email)
				if _, err := s.Complete(ctx, nil, input); !errors.Is(err, tc.want) {
					t.Fatalf("username=%q email=%q: %v, want %v", tc.username, tc.email, err, tc.want)
				}
				if c.User.Query().CountX(ctx) != users || c.ExternalIdentity.Query().CountX(ctx) != 0 || c.LoginBinding.Query().CountX(ctx) != 0 || c.RefreshToken.Query().CountX(ctx) != 0 || c.AuthTransaction.GetX(ctx, flow.TransactionID).Status != authtransaction.StatusAwaitingConfirmation {
					t.Fatal("invalid or denied input consumed confirmation or created account state")
				}
			}
			input.Username, input.Email = "valid-b5-data", ptrB5("owner@example.com")
			if _, err := s.Complete(ctx, nil, input); err != nil {
				t.Fatal("rejected data prevented corrected registration", err)
			}
		})
	}
}

func TestAuthenticationB5SignupRevisionIsolation(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
			s, c, ctx := svcs[0], clients[0], context.Background()
			_, in := flowTestConfirm(t, s, protocol, "flow-oidc")
			in.Username = "existing-b5"
			created, err := s.Complete(ctx, nil, in)
			if err != nil {
				t.Fatal(err)
			}
			flowTestStart(t, s, "flow-oidc")
			b5Publish(t, c, func(cfg *ent.AuthConnectionRevision, p *AuthConnectionPolicyInput) {
				p.Signup.EmailRequirement = "required"
				p.SignupRules = b5DomainRule("example.com")
			})
			cb, err := s.HandleCallback(ctx, "flow-oidc", protocol.state, "authorization-code-secret", "")
			if err != nil {
				t.Fatal(err)
			}
			login, err := s.Exchange(ctx, nil, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier})
			if err != nil || login.Kind != "session" {
				t.Fatal("signup-only publication interrupted bound login", err)
			}
			if _, err := s.auth.Refresh(ctx, created.Session.RefreshToken); err != nil {
				t.Fatal("signup-only publication revoked refresh", err)
			}
			protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
				return authoidc.Identity{Issuer: "https://issuer.example.test", Subject: "new-b5"}, nil
			}
			_, pending := flowTestConfirm(t, s, protocol, "flow-oidc")
			pending.Username = "stale-b5"
			pending.Email = ptrB5("final@example.com")
			b5Publish(t, c, func(cfg *ent.AuthConnectionRevision, p *AuthConnectionPolicyInput) {
				p.Signup.EmailRequirement = "optional"
				p.SignupRules = authrules.Unrestricted()
			})
			if _, err := s.Complete(ctx, nil, pending); !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatal("changed signup policy admitted old confirmation", err)
			}
			_, pending = flowTestConfirm(t, s, protocol, "flow-oidc")
			pending.Username = "global-stale"
			admin := createPasswordTestUser(t, c, "global-admin", "password-123")
			c.User.UpdateOneID(admin.ID).SetRole(SystemRoleAdmin).ExecX(ctx)
			settings := NewSettingsService(c)
			c.SystemSetting.Create().SetKey(SettingTaskRetention).SetValue(`{"enabled":false,"retention_days":30,"revision":1}`).ExecX(ctx)
			off, on := false, true
			if _, err := settings.Patch(ctx, admin.ID, SettingsPatch{RegistrationEnabled: &off}); err != nil {
				t.Fatal(err)
			}
			if _, err := settings.Patch(ctx, admin.ID, SettingsPatch{RegistrationEnabled: &on}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Complete(ctx, nil, pending); !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatal("global off/on revived confirmation", err)
			}
			if c.User.Query().Where(user.UsernameIn("stale-b5", "global-stale")).ExistX(ctx) {
				t.Fatal("stale confirmation left user")
			}
		})
	}
}

func TestAuthenticationB5SignupAuditRollback(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
			s, c, ctx := svcs[0], clients[0], context.Background()
			_, in := flowTestConfirm(t, s, protocol, "flow-oidc")
			in.Username = "rollback-b5"
			failure := errors.New("b5 audit failure")
			c.ActivityLog.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) { return nil, failure })
			})
			before := c.User.Query().CountX(ctx)
			if _, err := s.Complete(ctx, nil, in); !errors.Is(err, failure) {
				t.Fatal("audit failure ignored", err)
			}
			if c.User.Query().CountX(ctx) != before || c.ExternalIdentity.Query().CountX(ctx) != 0 || c.LoginBinding.Query().CountX(ctx) != 0 || c.RefreshToken.Query().Where(refreshtoken.ConsumedAtIsNil()).CountX(ctx) != 0 {
				t.Fatal("audit failure left partial signup")
			}
			if c.AuthTransaction.Query().Where(authtransaction.StatusEQ(authtransaction.StatusAwaitingConfirmation)).CountX(ctx) != 1 {
				t.Fatal("failed transaction consumed confirmation")
			}
		})
	}
}

func TestAuthenticationB5CredentialConsumptionRollback(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		for _, stage := range []string{"consume", "refresh_token", "local_audit"} {
			t.Run(driver+"/"+stage, func(t *testing.T) {
				svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
				s, c, ctx := svcs[0], clients[0], context.Background()
				failure := errors.New("injected B5 " + stage + " failure")
				hook := func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) { return nil, failure })
				}
				before := c.User.Query().CountX(ctx)
				if stage == "local_audit" {
					c.ActivityLog.Use(hook)
					if _, err := s.auth.Register(ctx, RegisterInput{Username: "local-rollback-b5", Password: "password-123"}); !errors.Is(err, failure) {
						t.Fatal("local audit failure ignored", err)
					}
				} else {
					b5Publish(t, c, func(cfg *ent.AuthConnectionRevision, p *AuthConnectionPolicyInput) {
						p.Signup.PasswordRequirement = "required"
					})
					flow, in := flowTestConfirm(t, s, protocol, "flow-oidc")
					in.Username, in.InitialPassword = "credential-rollback-b5", ptrB5("password-123")
					if stage == "consume" {
						c.AuthTransaction.Use(hook)
					} else {
						c.RefreshToken.Use(hook)
					}
					if _, err := s.Complete(ctx, nil, in); !errors.Is(err, failure) {
						t.Fatal("failed credential write ignored", err)
					}
					if c.AuthTransaction.GetX(ctx, flow.TransactionID).Status != authtransaction.StatusAwaitingConfirmation {
						t.Fatal("failed completion consumed confirmation")
					}
				}
				if c.User.Query().CountX(ctx) != before || c.LoginBinding.Query().CountX(ctx) != 0 || c.ExternalIdentity.Query().CountX(ctx) != 0 || c.RefreshSession.Query().CountX(ctx) != 0 || c.RefreshToken.Query().CountX(ctx) != 0 {
					t.Fatal("failed credential transaction left partial state")
				}
			})
		}
	}
}

func TestAuthenticationB5LinkProofAtomicCreationAndInitializationFields(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, protocol := externalFlowServices(t, driver, time.Now().UTC())
			s, c, ctx := svcs[0], clients[0], context.Background()
			owner := createPasswordTestUser(t, c, "link-init-b5", "password-123")
			proof, _, err := s.Reauthenticate(ctx, owner.ID, ReauthenticationInput{Method: "password", Password: "password-123", Action: "link", TargetID: "flow-oidc"})
			if err != nil {
				t.Fatal(err)
			}
			input := ExternalLinkStartInput{ExternalBrowserInput: ExternalBrowserInput{BrowserChallenge: browserChallenge(flowTestVerifier), CompletionURL: "https://app.example.test/auth/complete"}, ReauthenticationProof: proof.Proof}
			failure, fail := errors.New("injected link transaction creation failure"), true
			c.AuthTransaction.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
					if fail && m.Op().Is(ent.OpCreate) {
						return nil, failure
					}
					return next.Mutate(ctx, m)
				})
			})
			if _, err := s.StartLink(ctx, owner.ID, "flow-oidc", input); !errors.Is(err, failure) {
				t.Fatal("flow creation failure ignored", err)
			}
			stored := c.ReauthenticationProof.Query().Where(reauthenticationproof.TokenHashEQ(hash.Full(proof.Proof))).OnlyX(ctx)
			if stored.ConsumedAt != nil || c.AuthTransaction.Query().CountX(ctx) != 0 {
				t.Fatal("failed link consumed proof")
			}
			fail = false
			if _, err := s.StartLink(ctx, owner.ID, "flow-oidc", input); err != nil {
				t.Fatal("creation rollback prevented retry", err)
			}
			cb, err := s.HandleCallback(ctx, "flow-oidc", protocol.state, "authorization-code-secret", "")
			if err != nil {
				t.Fatal(err)
			}
			x, err := s.Exchange(ctx, &owner.ID, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier})
			if err != nil || x.Confirmation == nil || x.Kind != "link_confirmation" || x.Confirmation.Signup != nil || len(x.Confirmation.SignupRequirements) != 0 {
				t.Fatal("link exposed registration initialization", err)
			}
			base := ExternalCompleteInput{ConfirmationToken: x.Confirmation.ConfirmationToken, Verifier: flowTestVerifier}
			for _, change := range []func(*ExternalCompleteInput){
				func(in *ExternalCompleteInput) { in.UsernamePresent = true },
				func(in *ExternalCompleteInput) { in.DisplayNamePresent = true },
				func(in *ExternalCompleteInput) { in.EmailPresent = true },
				func(in *ExternalCompleteInput) { in.InitialPassword = ptrB5("") },
			} {
				in := base
				change(&in)
				if _, err := s.Complete(ctx, &owner.ID, in); !errors.Is(err, ErrInvalidInput) {
					t.Fatal("link accepted signup field presence", err)
				}
				if c.LoginBinding.Query().CountX(ctx) != 0 {
					t.Fatal("invalid initialization created binding")
				}
			}
			if out, err := s.Complete(ctx, &owner.ID, base); err != nil || out.Kind != "linked" {
				t.Fatal("link without signup fields failed", err)
			}
		})
	}
}
