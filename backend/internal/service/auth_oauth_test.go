package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authoauth2"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authprotocol"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/loginbinding"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
)

type oauthFlowProtocol struct {
	adminCtx      context.Context
	clock         *atomic.Int64
	state         string
	authorization authprotocol.AuthorizationRequest
	exchange      func(context.Context, authoauth2.Config, authprotocol.ExchangeRequest) (authprotocol.Identity, error)
}

func (*oauthFlowProtocol) Check(context.Context, authoauth2.Config) (authprotocol.ProviderMetadata, error) {
	return authprotocol.ProviderMetadata{SupportsPKCES256: true}, nil
}
func (p *oauthFlowProtocol) BuildAuthorization(_ context.Context, _ authoauth2.Config, in authprotocol.AuthorizationRequest) (authprotocol.Authorization, error) {
	p.state, p.authorization = in.State, in
	return authprotocol.Authorization{URL: "https://oauth.example.test/authorize"}, nil
}
func (p *oauthFlowProtocol) ExchangeAndVerify(ctx context.Context, cfg authoauth2.Config, in authprotocol.ExchangeRequest) (authprotocol.Identity, error) {
	if p.exchange != nil {
		return p.exchange(ctx, cfg, in)
	}
	if in.CodeVerifier == "" || cfg.ClientSecret != "oauth-client-secret" || in.RequireFresh {
		return authprotocol.Identity{}, errors.New("invalid generic OAuth exchange")
	}
	return authprotocol.Identity{Protocol: "oauth2", Authority: cfg.Authority, Subject: in.Code, DisplayName: "OAuth member"}, nil
}

func oauthTestServices(t *testing.T, driver string) ([2]*ExternalAuthService, [2]*ent.Client, *ent.User, *oauthFlowProtocol) {
	t.Helper()
	f := newB4Fixture(t, driver)
	svcs, clients, admin := f.services, f.clients, f.admin
	p := &oauthFlowProtocol{adminCtx: f.ctx, clock: f.clock}
	for _, svc := range svcs {
		svc.cfg.PublicBaseURL = "https://app.example.test"
		svc.cfg.CompletionURLs = []string{"https://app.example.test/auth/complete"}
		svc.oauth2Protocol = p
	}
	return svcs, clients, admin, p
}

func oauthTestConnection(t *testing.T, svc *ExternalAuthService, admin *ent.User, protocol *oauthFlowProtocol) AuthConnectionRecord {
	t.Helper()
	ctx := protocol.adminCtx
	input := OAuth2ConnectionInput{AuthorizationEndpoint: "https://oauth.example.test/authorize", TokenEndpoint: "https://oauth.example.test/token", ProfileEndpoint: "https://oauth.example.test/profile", ClientID: "oauth-client", SubjectMapping: authoauth2.SubjectMapping{Pointer: "/id", Type: "string"}}
	rec, err := svc.CreateConnection(ctx, admin.ID, CreateAuthConnectionInput{Kind: "generic_oauth2", DisplayName: "Generic identity", OAuth2: &input, ClientSecret: "oauth-client-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.IdentityAuthorityID == nil || *rec.IdentityAuthorityID == "" || rec.Configuration == nil || rec.Configuration.OIDC != nil || rec.Configuration.OAuth2 == nil {
		t.Fatalf("generic connection lacks its own authority or protocol config: %+v", rec)
	}
	policy := rec.Configuration.Policy
	policy.LoginEnabled, policy.SignupEnabled, policy.LinkEnabled = true, true, true
	policy.Signup = SignupPolicyRequirements{EmailRequirement: "optional", PasswordRequirement: "required"}
	rec, err = svc.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, AuthConnectionChanges: AuthConnectionChanges{Policy: &policy}})
	if err != nil {
		t.Fatal(err)
	}
	check, err := svc.CheckConnection(ctx, admin.ID, rec.ID, AuthConnectionCandidateInput{ExpectedRevision: rec.AggregateRevision})
	if err != nil || !check.Valid || check.FreshSupported {
		t.Fatalf("generic check=%+v err=%v", check, err)
	}
	_, err = svc.EnableConnection(ctx, admin.ID, rec.ID, AuthConnectionActionInput{ExpectedRevision: rec.AggregateRevision})
	b4Code(t, err, "test-required")
	tested := oauthTestProof(t, svc, admin, protocol, rec, nil)
	rec, err = svc.EnableConnection(ctx, admin.ID, rec.ID, AuthConnectionActionInput{ExpectedRevision: rec.AggregateRevision, TestID: tested.TestID, Verifier: flowTestVerifier})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Validation.FreshSupported || rec.Validation.TestedAt == nil || rec.Validation.AppliedTestID != tested.TestID || rec.State != "enabled" || rec.Configuration == nil {
		t.Fatalf("enabled validation=%+v", rec.Validation)
	}
	return rec
}

func oauthTestProof(t *testing.T, svc *ExternalAuthService, admin *ent.User, protocol *oauthFlowProtocol, rec AuthConnectionRecord, changes *AuthConnectionChanges) ExternalTestResult {
	t.Helper()
	ctx := protocol.adminCtx
	start, err := svc.StartTest(ctx, admin.ID, rec.ID, AuthConnectionTestInput{AuthConnectionCandidateInput: AuthConnectionCandidateInput{ExpectedRevision: rec.AggregateRevision, Changes: changes}, ExternalBrowserInput: ExternalBrowserInput{BrowserChallenge: browserChallenge(flowTestVerifier), CompletionURL: "https://app.example.test/auth/complete"}})
	if err != nil {
		t.Fatal(err)
	}
	if protocol.authorization.Fresh {
		t.Fatal("generic administrator test requested fresh authentication")
	}
	cb, err := svc.HandleCallback(context.Background(), rec.ID, protocol.state, "administrator-test-subject", "")
	if err != nil || cb.Failed {
		t.Fatalf("test callback=%+v err=%v", cb, err)
	}
	x, err := svc.Exchange(ctx, &admin.ID, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier})
	if err != nil || x.Test == nil || x.Kind != "test_result" || x.Test.FreshSupported || x.Session != nil || x.Proof != nil {
		t.Fatalf("test result=%+v err=%v", x, err)
	}
	if x.Test.TestID != start.TestID || start.TestID != start.TransactionID || !x.Test.ExpiresAt.Equal(start.ExpiresAt) {
		t.Fatal("test proof changed transaction identity or deadline")
	}
	return *x.Test
}

func oauthTestExchange(t *testing.T, svc *ExternalAuthService, protocol *oauthFlowProtocol, id, subject string) (ExternalStartResult, ExternalExchangeResult, error) {
	t.Helper()
	start := flowTestStart(t, svc, id)
	cb, err := svc.HandleCallback(context.Background(), id, protocol.state, subject, "")
	if err != nil {
		return start, ExternalExchangeResult{}, err
	}
	x, err := svc.Exchange(context.Background(), nil, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier})
	return start, x, err
}

func oauthTestConfirmation(t *testing.T, svc *ExternalAuthService, protocol *oauthFlowProtocol, id, subject, username string) (ExternalStartResult, ExternalCompleteInput) {
	t.Helper()
	start, x, err := oauthTestExchange(t, svc, protocol, id, subject)
	if err != nil || x.Kind != "signup_confirmation" || x.Confirmation == nil {
		t.Fatalf("confirmation=%+v err=%v", x, err)
	}
	if !reflect.DeepEqual(x.Confirmation.SignupRequirements, []string{"local_password"}) {
		t.Fatalf("signup requirements=%v", x.Confirmation.SignupRequirements)
	}
	password := "initial-password-123"
	return start, ExternalCompleteInput{ConfirmationToken: x.Confirmation.ConfirmationToken, Verifier: flowTestVerifier, Username: username, InitialPassword: &password}
}

func oauthAssertAccountCounts(t *testing.T, c *ent.Client, users, identities, sessions int) {
	t.Helper()
	ctx := context.Background()
	// newB4Fixture establishes exactly one real administrator session/token.
	// Provider tests must not create any additional sessions, users, or bindings.
	if c.User.Query().CountX(ctx) != users || c.ExternalIdentity.Query().CountX(ctx) != identities || c.LoginBinding.Query().CountX(ctx) != identities || c.RefreshSession.Query().CountX(ctx) != sessions+1 || c.RefreshToken.Query().CountX(ctx) != sessions+1 {
		t.Fatalf("partial/duplicate state: users=%d identities=%d bindings=%d sessions=%d tokens=%d", c.User.Query().CountX(ctx), c.ExternalIdentity.Query().CountX(ctx), c.LoginBinding.Query().CountX(ctx), c.RefreshSession.Query().CountX(ctx), c.RefreshToken.Query().CountX(ctx))
	}
}

func oauthWriteLocalPolicy(ctx context.Context, c *ent.Client, mutate func(*LocalAccountPolicyInput)) error {
	return withOrganizationTransaction(ctx, c, func(tx *ent.Client) error {
		if err := lockAuthenticationConfiguration(ctx, tx); err != nil {
			return err
		}
		current, err := readPasswordPolicy(ctx, tx)
		if err != nil {
			return err
		}
		candidate := localAccountInput(current.Local)
		mutate(&candidate)
		return writeLocalAccountPolicy(ctx, tx, current, candidate, time.Now().UTC())
	})
}

func oauthWriteLocalPolicyTest(t *testing.T, ctx context.Context, c *ent.Client, mutate func(*LocalAccountPolicyInput)) {
	t.Helper()
	if err := oauthWriteLocalPolicy(ctx, c, mutate); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticationB4OAuthSignupAndMaintenance(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, admin, protocol := oauthTestServices(t, driver)
			s, c, ctx := svcs[0], clients[0], context.Background()
			rec := oauthTestConnection(t, s, admin, protocol)
			oauthWriteLocalPolicyTest(t, ctx, c, func(candidate *LocalAccountPolicyInput) {
				candidate.Signup.Enabled = false
			})
			caps, err := s.auth.Capabilities(ctx)
			if err != nil || caps.LocalAccount.CanSignup || len(caps.Connections) != 1 || !caps.Connections[0].CanSignup {
				t.Fatalf("signup capabilities=%+v err=%v", caps, err)
			}
			if _, err := s.auth.Register(ctx, RegisterInput{Username: "direct-signup", Password: "password-123"}); !errors.Is(err, ErrRegistrationClosed) {
				t.Fatalf("password signup remains enabled: %v", err)
			}
			started, input := oauthTestConfirmation(t, s, protocol, rec.ID, "new-subject", "oauth-member")
			for _, password := range []*string{nil, new(string)} {
				bad := input
				bad.InitialPassword = password
				if _, err := s.Complete(ctx, nil, bad); !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("missing/empty initial password: %v", err)
				}
				oauthAssertAccountCounts(t, c, 1, 0, 0)
				if c.AuthTransaction.GetX(ctx, started.TransactionID).Status != authtransaction.StatusAwaitingConfirmation {
					t.Fatal("invalid password consumed confirmation")
				}
			}
			out, err := s.Complete(ctx, nil, input)
			if err != nil || out.Session == nil {
				t.Fatalf("initial-password signup=%+v err=%v", out, err)
			}
			account := c.User.GetX(ctx, out.Session.User.ID)
			if compareErr := comparePassword(account.PasswordHash, *input.InitialPassword); compareErr != nil {
				t.Fatalf("signup password mismatch: %v", compareErr)
			}
			if account.PasswordVerifiedAt == nil || account.PasswordVerifiedHash != hash.Full(account.PasswordHash) {
				t.Fatal("required initial password did not create verified fallback")
			}
			identity := c.ExternalIdentity.Query().OnlyX(ctx)
			if identity.Protocol != externalidentity.ProtocolOauth2 || identity.Authority != *rec.IdentityAuthorityID || identity.Subject != "new-subject" || identity.UserID != account.ID {
				t.Fatal("generic signup used the wrong identity namespace")
			}
			oauthAssertAccountCounts(t, c, 2, 1, 1)
			if _, err := svcs[1].Complete(ctx, nil, input); !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatalf("confirmation replay=%v", err)
			}
			passwordSession, err := s.auth.Login(ctx, LoginInput{Identifier: account.Username, Password: *input.InitialPassword})
			if err != nil || passwordSession.User.ID != account.ID {
				t.Fatalf("local password cannot log in: %v", err)
			}
			verified := c.User.GetX(ctx, account.ID)
			if verified.PasswordVerifiedAt == nil || verified.PasswordVerifiedHash != hash.Full(verified.PasswordHash) {
				t.Fatal("real password login did not establish verified fallback")
			}
			_, login, err := oauthTestExchange(t, s, protocol, rec.ID, "new-subject")
			if err != nil || login.Session == nil || login.Session.User.ID != account.ID {
				t.Fatalf("generic relogin=%+v err=%v", login, err)
			}
			methods, err := s.LoginMethods(ctx, account.ID)
			if err != nil || len(methods.Bindings) != 1 || !methods.Bindings[0].CanLogin || methods.Bindings[0].CanReauthenticate || methods.Password.CanDelete {
				t.Fatalf("unsafe maintenance capabilities=%+v err=%v", methods, err)
			}
			binding := c.LoginBinding.Query().Where(loginbinding.UserIDEQ(account.ID)).OnlyX(ctx)
			for _, method := range []string{"oauth2", "oidc"} {
				_, start, err := s.Reauthenticate(ctx, account.ID, ReauthenticationInput{Method: method, BindingID: &binding.ID, Action: "password_delete", TargetID: PasswordConnectionID, BrowserChallenge: browserChallenge(flowTestVerifier), CompletionURL: "https://app.example.test/auth/complete"})
				if start != nil || !(errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrReauthenticationRequired)) {
					t.Fatalf("generic binding reauthenticated via %s: %v", method, err)
				}
			}
			proof, _, err := s.Reauthenticate(ctx, account.ID, ReauthenticationInput{Method: "password", Password: *input.InitialPassword, Action: "password_delete", TargetID: PasswordConnectionID})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.DeletePassword(ctx, account.ID, proof.Proof); !errors.Is(err, ErrLoginMethodConflict) {
				t.Fatalf("generic binding allowed removal of maintenance password: %v", err)
			}
			if c.User.GetX(ctx, account.ID).PasswordHash != account.PasswordHash {
				t.Fatal("rejected password removal changed account")
			}
		})
	}
}

func TestAuthenticationB4OAuthPolicyDependencies(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			for _, setting := range []string{"password_enrollment", "password_login", "username_login", "global_registration"} {
				t.Run(setting, func(t *testing.T) {
					svcs, clients, admin, protocol := oauthTestServices(t, driver)
					s, c, ctx := svcs[0], clients[0], context.Background()
					rec := oauthTestConnection(t, s, admin, protocol)
					_, input := oauthTestConfirmation(t, s, protocol, rec.ID, "pending-subject", "pending-member")
					switch setting {
					case "password_enrollment":
						oauthWriteLocalPolicyTest(t, ctx, c, func(candidate *LocalAccountPolicyInput) {
							candidate.PasswordEnrollmentEnabled = false
						})
					case "password_login", "username_login":
						oauthWriteLocalPolicyTest(t, ctx, c, func(candidate *LocalAccountPolicyInput) {
							candidate.PasswordLoginEnabled = false
							candidate.PasswordEnrollmentEnabled = false
							candidate.Signup.Enabled = false
						})
					case "global_registration":
						c.SystemSetting.Create().SetKey(SettingTaskRetention).
							SetValue(`{"enabled":false,"retention_days":30,"revision":1}`).ExecX(ctx)
						registration := false
						settings, settingsErr := NewSettingsService(c).Patch(ctx, admin.ID, SettingsPatch{
							RegistrationEnabled: &registration,
							TaskRetention:       &TaskRetentionPatch{Enabled: false, RetentionDays: 30, ExpectedRevision: 1},
						})
						if settingsErr != nil {
							t.Fatal(settingsErr)
						}
						if settings.RegistrationEnabled {
							t.Fatal("registration remained enabled")
						}
						if _, revision, revisionErr := readRegistrationState(ctx, c); revisionErr != nil || revision != 2 {
							t.Fatalf("registration revision=%d err=%v", revision, revisionErr)
						}
					default:
						t.Fatalf("unknown dependency case %q", setting)
					}
					caps, err := s.auth.Capabilities(ctx)
					if err != nil || len(caps.Connections) != 1 || caps.Connections[0].CanSignup {
						t.Fatalf("disabled dependency advertised signup: %+v, %v", caps, err)
					}
					if _, err := s.Complete(ctx, nil, input); !errors.Is(err, ErrAuthConnectionConflict) {
						t.Fatalf("complete accepted stale confirmation after dependency change: %v", err)
					}
					if _, _, err := oauthTestExchange(t, s, protocol, rec.ID, "another-subject"); !errors.Is(err, ErrRegistrationClosed) {
						t.Fatalf("exchange ignored signup dependency: %v", err)
					}
					oauthAssertAccountCounts(t, c, 1, 0, 0)
				})
			}
		})
	}
}

func TestAuthenticationB4OAuthAtomicCompletion(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			t.Run("late session failure rolls back password and identity", func(t *testing.T) {
				svcs, clients, admin, p := oauthTestServices(t, driver)
				s, c, ctx := svcs[0], clients[0], context.Background()
				rec := oauthTestConnection(t, s, admin, p)
				start, input := oauthTestConfirmation(t, s, p, rec.ID, "subject", "atomic-member")
				injected := errors.New("injected session creation failure")
				fail := true
				c.RefreshSession.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
						if fail {
							return nil, injected
						}
						return next.Mutate(ctx, m)
					})
				})
				if _, err := s.Complete(ctx, nil, input); !errors.Is(err, injected) {
					t.Fatalf("late failure=%v", err)
				}
				oauthAssertAccountCounts(t, c, 1, 0, 0)
				if c.AuthTransaction.GetX(ctx, start.TransactionID).Status != authtransaction.StatusAwaitingConfirmation {
					t.Fatal("rollback consumed confirmation")
				}
				fail = false
				if _, err := s.Complete(ctx, nil, input); err != nil {
					t.Fatalf("retry after rollback: %v", err)
				}
				oauthAssertAccountCounts(t, c, 2, 1, 1)
			})
			t.Run("username conflict retry and completion replay race", func(t *testing.T) {
				svcs, clients, admin, p := oauthTestServices(t, driver)
				ctx, c := context.Background(), clients[0]
				rec := oauthTestConnection(t, svcs[0], admin, p)
				_, input := oauthTestConfirmation(t, svcs[0], p, rec.ID, "subject", admin.Username)
				if _, err := svcs[0].Complete(ctx, nil, input); !errors.Is(err, ErrUserExists) {
					t.Fatalf("username conflict=%v", err)
				}
				oauthAssertAccountCounts(t, c, 1, 0, 0)
				input.Username = "race-member"
				expectOneExternalWinner(t, raceExternalCalls(func() error { _, err := svcs[0].Complete(ctx, nil, input); return err }, func() error { _, err := svcs[1].Complete(ctx, nil, input); return err }), ErrExternalFlowInvalid)
				oauthAssertAccountCounts(t, c, 2, 1, 1)
			})
			t.Run("one subject cannot acquire two owners", func(t *testing.T) {
				svcs, clients, admin, p := oauthTestServices(t, driver)
				ctx := context.Background()
				rec := oauthTestConnection(t, svcs[0], admin, p)
				_, first := oauthTestConfirmation(t, svcs[0], p, rec.ID, "same-subject", "first-member")
				_, second := oauthTestConfirmation(t, svcs[1], p, rec.ID, "same-subject", "second-member")
				expectOneExternalWinner(t, raceExternalCalls(func() error { _, err := svcs[0].Complete(ctx, nil, first); return err }, func() error { _, err := svcs[1].Complete(ctx, nil, second); return err }), ErrAuthConnectionConflict)
				oauthAssertAccountCounts(t, clients[0], 2, 1, 1)
			})
			t.Run("cancel versus complete leaves no partial account", func(t *testing.T) {
				svcs, clients, admin, p := oauthTestServices(t, driver)
				ctx := context.Background()
				rec := oauthTestConnection(t, svcs[0], admin, p)
				start, input := oauthTestConfirmation(t, svcs[0], p, rec.ID, "cancel-subject", "cancel-member")
				expectOneExternalWinner(t, raceExternalCalls(func() error { _, err := svcs[0].Complete(ctx, nil, input); return err }, func() error {
					return svcs[1].Cancel(ctx, nil, ExternalCancelInput{TransactionID: start.TransactionID, Verifier: flowTestVerifier})
				}), ErrExternalFlowInvalid)
				want := 0
				if clients[0].AuthTransaction.GetX(ctx, start.TransactionID).Status == authtransaction.StatusCompleted {
					want = 1
				}
				oauthAssertAccountCounts(t, clients[0], 1+want, want, want)
				if _, err := svcs[1].Complete(ctx, nil, input); !errors.Is(err, ErrExternalFlowInvalid) {
					t.Fatalf("terminal confirmation replay=%v", err)
				}
			})
		})
	}
}

func TestAuthenticationB4OAuthCompletePasswordPolicySaveRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, admin, protocol := oauthTestServices(t, driver)
			ctx, c := context.Background(), clients[0]
			rec := oauthTestConnection(t, svcs[0], admin, protocol)
			started, input := oauthTestConfirmation(t, svcs[0], protocol, rec.ID, "policy-race-subject", "policy-race-member")
			// The local singleton is the password configuration. A concurrent
			// signup-disabled save must serialize against an in-flight OAuth
			// confirmation without creating an obsolete password revision.
			type outcome struct {
				save      bool
				completed ExternalCompleteResult
				err       error
			}
			start, results := make(chan struct{}), make(chan outcome, 2)
			go func() {
				<-start
				completed, completeErr := svcs[0].Complete(ctx, nil, input)
				results <- outcome{completed: completed, err: completeErr}
			}()
			go func() {
				<-start
				err := withOrganizationTransaction(ctx, clients[1], func(tx *ent.Client) error {
					if err := lockAuthenticationConfiguration(ctx, tx); err != nil {
						return err
					}
					current, policyErr := readPasswordPolicy(ctx, tx)
					if policyErr != nil {
						return policyErr
					}
					// Local signup is deliberately independent of external
					// signup initialization; changing it must not no-op merely
					// because the enabled OAuth source already requires a
					// local password.
					candidate := localAccountInput(current.Local)
					candidate.Signup.Enabled = false
					return writeLocalAccountPolicy(ctx, tx, current, candidate, time.Now().UTC())
				})
				results <- outcome{save: true, err: err}
			}()
			close(start)
			var completed outcome
			for range 2 {
				result := <-results
				if result.save {
					if result.err != nil {
						t.Fatalf("local policy save failed: %v", result.err)
					}
				} else {
					completed = result
				}
			}
			saved, err := readPasswordPolicy(ctx, c)
			if err != nil || saved.Local.SignupEnabled || saved.Local.SignupPolicyRevision != 2 || saved.Local.Revision != 2 {
				t.Fatalf("local signup policy was not saved: %+v, %v", saved, err)
			}
			// Both serial orders must complete: the external source already
			// requires a local password, so a local-signup-only revision is
			// outside its confirmation dependency fingerprint.
			if completed.err != nil || completed.completed.Session == nil {
				t.Fatalf("external required-password signup ignored local-signup isolation: %+v, %v", completed.completed, completed.err)
			}
			transaction := c.AuthTransaction.GetX(ctx, started.TransactionID)
			if transaction.Status != authtransaction.StatusCompleted {
				t.Fatalf("completed transaction=%s", transaction.Status)
			}
			account := c.User.GetX(ctx, completed.completed.Session.User.ID)
			if comparePassword(account.PasswordHash, *input.InitialPassword) != nil {
				t.Fatal("completed signup did not retain the required initial password")
			}
			oauthAssertAccountCounts(t, c, 2, 1, 1)
		})
	}
}

func TestAuthenticationB4OAuthIdentityAndStateIsolation(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, admin, p := oauthTestServices(t, driver)
			s, c, ctx := svcs[0], clients[0], context.Background()
			first, second := oauthTestConnection(t, s, admin, p), oauthTestConnection(t, s, admin, p)
			if *first.IdentityAuthorityID == *second.IdentityAuthorityID {
				t.Fatal("independent connections share authority")
			}
			// Even an identical authority/subject in OIDC cannot own the OAuth identity.
			c.ExternalIdentity.Create().SetUserID(admin.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority(*first.IdentityAuthorityID).SetSubject("same-subject").ExecX(ctx)
			var ids []int
			for i, rec := range []AuthConnectionRecord{first, second} {
				_, input := oauthTestConfirmation(t, s, p, rec.ID, "same-subject", []string{"namespace-one", "namespace-two"}[i])
				out, err := s.Complete(ctx, nil, input)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, out.Session.User.ID)
			}
			if ids[0] == ids[1] || ids[0] == admin.ID || c.ExternalIdentity.Query().CountX(ctx) != 3 {
				t.Fatal("protocol or authority namespaces merged accounts")
			}
			started := flowTestStart(t, s, first.ID)
			state := p.state
			stored := c.AuthTransaction.GetX(ctx, started.TransactionID)
			if stored.StateHash != hash.Full(state) || stored.StateHash == state {
				t.Fatal("state persisted in plaintext")
			}
			if _, err := s.HandleCallback(ctx, second.ID, state, "same-subject", ""); !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatalf("cross-connection callback=%v", err)
			}
			cb, err := s.HandleCallback(ctx, first.ID, state, "same-subject", "")
			if err != nil || cb.Failed {
				t.Fatalf("valid callback=%+v err=%v", cb, err)
			}
			if _, err := svcs[1].HandleCallback(ctx, first.ID, state, "same-subject", ""); !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatalf("callback replay=%v", err)
			}
			if _, err := s.Exchange(ctx, nil, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: "wrong-browser"}); !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatalf("wrong browser=%v", err)
			}
			if _, err := s.Exchange(ctx, nil, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier}); err != nil {
				t.Fatal(err)
			}
			if _, err := svcs[1].Exchange(ctx, nil, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier}); !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatalf("ticket replay=%v", err)
			}
			for _, wrong := range []string{"protocol", "authority", "subject"} {
				p.exchange = func(_ context.Context, cfg authoauth2.Config, in authprotocol.ExchangeRequest) (authprotocol.Identity, error) {
					identity := authprotocol.Identity{Protocol: "oauth2", Authority: cfg.Authority, Subject: in.Code, AuthTime: time.Now()}
					switch wrong {
					case "protocol":
						identity.Protocol = "oidc"
					case "authority":
						identity.Authority = *second.IdentityAuthorityID
					case "subject":
						identity.Subject = ""
					}
					return identity, nil
				}
				if _, _, err := oauthTestExchange(t, s, p, first.ID, "untrusted-subject"); !errors.Is(err, ErrExternalFlowInvalid) {
					t.Fatalf("accepted wrong %s: %v", wrong, err)
				}
			}
		})
	}
}

func TestAuthenticationB4OAuthOIDCWithoutAuthTime(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, admin, oauthProtocol := oauthTestServices(t, driver)
			s, c := svcs[0], clients[0]
			ctx := oauthProtocol.adminCtx
			protocol := &externalFlowTestProtocol{}
			s.SetOIDCProtocol(protocol)
			rec, err := s.CreateConnection(ctx, admin.ID, CreateAuthConnectionInput{Kind: "oidc", DisplayName: "OIDC without auth_time", OIDC: &OIDCConnectionInput{Issuer: "https://issuer.example.test", ClientID: "oidc-client"}, ClientSecret: "oidc-secret"})
			if err != nil {
				t.Fatal(err)
			}
			policy := rec.Configuration.Policy
			policy.LoginEnabled, policy.SignupEnabled, policy.LinkEnabled = true, true, true
			policy.Signup = SignupPolicyRequirements{EmailRequirement: "optional", PasswordRequirement: "required"}
			rec, err = s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, AuthConnectionChanges: AuthConnectionChanges{Policy: &policy}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.StartTest(ctx, admin.ID, rec.ID, AuthConnectionTestInput{AuthConnectionCandidateInput: AuthConnectionCandidateInput{ExpectedRevision: rec.AggregateRevision}, ExternalBrowserInput: ExternalBrowserInput{BrowserChallenge: browserChallenge(flowTestVerifier), CompletionURL: "https://app.example.test/auth/complete"}})
			if err != nil {
				t.Fatal(err)
			}
			callback, err := s.HandleCallback(ctx, rec.ID, protocol.state, "authorization-code-secret", "")
			if err != nil || callback.Failed {
				t.Fatalf("valid OIDC test without auth_time: %+v, %v", callback, err)
			}
			tested, err := s.Exchange(ctx, &admin.ID, ExternalExchangeInput{Ticket: callback.Ticket, Verifier: flowTestVerifier})
			if err != nil || tested.Kind != "test_result" || tested.Test == nil || tested.Test.FreshSupported {
				t.Fatalf("authentication-only OIDC test=%+v err=%v", tested, err)
			}
			rec, err = s.EnableConnection(ctx, admin.ID, rec.ID, AuthConnectionActionInput{ExpectedRevision: rec.AggregateRevision, TestID: tested.Test.TestID, Verifier: flowTestVerifier})
			if err != nil || rec.Configuration == nil || rec.Validation.FreshSupported {
				t.Fatalf("enable authentication-only OIDC=%+v err=%v", rec, err)
			}
			flowTestStart(t, s, rec.ID)
			callback, err = s.HandleCallback(context.Background(), rec.ID, protocol.state, "authorization-code-secret", "")
			if err != nil {
				t.Fatal(err)
			}
			confirmation, err := s.Exchange(context.Background(), nil, ExternalExchangeInput{Ticket: callback.Ticket, Verifier: flowTestVerifier})
			if err != nil || confirmation.Confirmation == nil || !reflect.DeepEqual(confirmation.Confirmation.SignupRequirements, []string{"local_password"}) {
				t.Fatalf("OIDC signup requirements=%+v err=%v", confirmation, err)
			}
			input := ExternalCompleteInput{ConfirmationToken: confirmation.Confirmation.ConfirmationToken, Verifier: flowTestVerifier, Username: "oidc-password-member"}
			if _, err := s.Complete(context.Background(), nil, input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("OIDC without fresh capability created passwordless account: %v", err)
			}
			password := "initial-password-123"
			input.InitialPassword = &password
			created, err := s.Complete(context.Background(), nil, input)
			if err != nil || created.Session == nil {
				t.Fatalf("OIDC signup with initial password=%+v err=%v", created, err)
			}
			if comparePassword(c.User.GetX(context.Background(), created.Session.User.ID).PasswordHash, password) != nil {
				t.Fatal("OIDC signup password not stored")
			}
			methods, err := s.LoginMethods(context.Background(), created.Session.User.ID)
			if err != nil || len(methods.Bindings) != 1 || methods.Bindings[0].CanReauthenticate {
				t.Fatalf("OIDC without auth_time advertised reauthentication: %+v err=%v", methods, err)
			}
		})
	}
}

func TestAuthenticationB4OAuthRulesAndRefreshEvidence(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, admin, protocol := oauthTestServices(t, driver)
			s, c, ctx := svcs[0], clients[0], context.Background()
			rec := oauthTestConnection(t, s, admin, protocol)
			department := "sales"
			protocol.exchange = func(_ context.Context, cfg authoauth2.Config, in authprotocol.ExchangeRequest) (authprotocol.Identity, error) {
				raw, err := json.Marshal(map[string]string{"id": in.Code, "department": department})
				return authprotocol.Identity{Protocol: "oauth2", Authority: cfg.Authority, Subject: in.Code, Sources: map[string]json.RawMessage{"oauth2_profile": raw}}, err
			}
			mapping := []authrules.Field{{FieldID: "custom.department", Label: "Department", Type: "string", Source: "oauth2_profile", Pointer: "/department"}}
			policy := rec.Configuration.Policy
			policy.LoginRules = departmentRule()
			policy.LinkRules = departmentRule()
			policy.LinkRules.Conditions[0].Value = json.RawMessage(`"never"`)
			age := 60
			changes := AuthConnectionChanges{Policy: &policy, AttributeMappings: &mapping, EvidenceMaxAgeSeconds: &age}
			tested := oauthTestProof(t, s, admin, protocol, rec, &changes)
			var err error
			rec, err = s.UpdateConnection(protocol.adminCtx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, TestID: tested.TestID, Verifier: flowTestVerifier, AuthConnectionChanges: changes})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = oauthTestExchange(t, s, protocol, rec.ID, "rule-subject"); !errors.Is(err, ErrAuthenticationRuleDenied) {
				t.Fatalf("denied OAuth profile admitted signup: %v", err)
			}
			oauthAssertAccountCounts(t, c, 1, 0, 0)
			department = "engineering"
			pending, input := oauthTestConfirmation(t, s, protocol, rec.ID, "rule-subject", "rule-owner")
			deadline := c.AuthTransaction.GetX(ctx, pending.TransactionID).Evidence.ExpiresAt
			protocol.clock.Store(deadline.UnixNano())
			if _, err = s.Complete(ctx, nil, input); !errors.Is(err, ErrAuthenticationRuleDenied) {
				t.Fatalf("final confirmation accepted expired OAuth evidence: %v", err)
			}
			oauthAssertAccountCounts(t, c, 1, 0, 0)
			if c.AuthTransaction.GetX(ctx, pending.TransactionID).Status != authtransaction.StatusAwaitingConfirmation {
				t.Fatal("rejected stale evidence consumed confirmation")
			}
			_, input = oauthTestConfirmation(t, s, protocol, rec.ID, "rule-subject", "rule-owner")
			completed, err := s.Complete(ctx, nil, input)
			if err != nil || completed.Session == nil {
				t.Fatalf("OAuth signup incorrectly applied link rule: %+v, %v", completed, err)
			}
			if comparePassword(c.User.GetX(ctx, completed.Session.User.ID).PasswordHash, *input.InitialPassword) != nil {
				t.Fatal("rule-controlled signup omitted its initial password")
			}
			session := c.RefreshSession.Query().Where(refreshsession.UserIDEQ(completed.Session.User.ID)).OnlyX(ctx)
			deadline = session.Evidence.ExpiresAt
			if len(session.Evidence.Attributes) != 1 || deadline.IsZero() || session.Evidence.Issuer != *rec.IdentityAuthorityID || session.Evidence.Subject != "rule-subject" {
				t.Fatal("OAuth refresh session lacks scoped profile evidence")
			}
			protocol.clock.Add(int64(30 * time.Second))
			rotated, err := s.auth.Refresh(ctx, completed.Session.RefreshToken)
			if err != nil {
				t.Fatal(err)
			}
			if !c.RefreshSession.GetX(ctx, session.ID).Evidence.ExpiresAt.Equal(deadline) {
				t.Fatal("refresh rotation extended OAuth evidence lifetime")
			}
			protocol.clock.Store(deadline.UnixNano())
			if _, err = s.auth.Refresh(ctx, rotated.RefreshToken); !errors.Is(err, ErrRefreshTokenRevoked) {
				t.Fatalf("OAuth evidence expiry admitted refresh: %v", err)
			}
			if c.RefreshSession.GetX(ctx, session.ID).RevokedAt == nil {
				t.Fatal("rejected refresh did not persist revocation")
			}
			_, fresh, err := oauthTestExchange(t, s, protocol, rec.ID, "rule-subject")
			if err != nil || fresh.Session == nil || fresh.Session.User.ID != completed.Session.User.ID {
				t.Fatalf("new OAuth authentication failed after evidence expiry: %+v, %v", fresh, err)
			}
		})
	}
}

func TestAuthenticationB4OAuthCompleteSignupRuleSaveRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, admin, protocol := oauthTestServices(t, driver)
			s, c, ctx := svcs[0], clients[0], context.Background()
			rec := oauthTestConnection(t, s, admin, protocol)
			mapping := []authrules.Field{{FieldID: "custom.department", Label: "Department", Type: "string", Source: "oauth2_profile", Pointer: "/department"}}
			changes := AuthConnectionChanges{AttributeMappings: &mapping}
			tested := oauthTestProof(t, s, admin, protocol, rec, &changes)
			var err error
			rec, err = s.UpdateConnection(protocol.adminCtx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, TestID: tested.TestID, Verifier: flowTestVerifier, AuthConnectionChanges: changes})
			if err != nil {
				t.Fatal(err)
			}
			started, input := oauthTestConfirmation(t, s, protocol, rec.ID, "rule-race-subject", "rule-race-member")
			policy := rec.Configuration.Policy
			policy.SignupRules = departmentRule()
			var completed ExternalCompleteResult
			results := raceExternalCalls(func() error {
				var err error
				completed, err = s.Complete(ctx, nil, input)
				return err
			}, func() error {
				_, err := svcs[1].UpdateConnection(protocol.adminCtx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, AuthConnectionChanges: AuthConnectionChanges{Policy: &policy}})
				return err
			})
			// The rule save always succeeds. Completion either commits first or
			// observes the changed signup rule before creating any account state.
			for _, err := range results {
				if err != nil && !errors.Is(err, ErrAuthConnectionConflict) {
					t.Fatalf("unexpected rule/confirmation race error: %v", err)
				}
			}
			saved, err := s.GetConnection(protocol.adminCtx, admin.ID, rec.ID)
			if err != nil || saved.AggregateRevision != rec.AggregateRevision+1 || !reflect.DeepEqual(saved.Configuration.Policy.SignupRules, policy.SignupRules) {
				t.Fatalf("signup rule save did not commit: %+v, %v", saved, err)
			}
			transaction := c.AuthTransaction.GetX(ctx, started.TransactionID)
			if completed.Session != nil {
				if transaction.Status != authtransaction.StatusCompleted || comparePassword(c.User.GetX(ctx, completed.Session.User.ID).PasswordHash, *input.InitialPassword) != nil {
					t.Fatal("winning confirmation left partial password/account state")
				}
				oauthAssertAccountCounts(t, c, 2, 1, 1)
				if _, err := s.auth.Refresh(ctx, completed.Session.RefreshToken); err != nil {
					t.Fatalf("signup-only rule save revoked an existing login session: %v", err)
				}
			} else {
				if transaction.Status != authtransaction.StatusAwaitingConfirmation || transaction.ConfirmationTokenHash == nil {
					t.Fatal("rejected confirmation was consumed")
				}
				oauthAssertAccountCounts(t, c, 1, 0, 0)
			}
			if _, err := s.Complete(ctx, nil, input); !(errors.Is(err, ErrExternalFlowInvalid) || errors.Is(err, ErrAuthConnectionConflict)) {
				t.Fatalf("old confirmation usable after signup rule save: %v", err)
			}
		})
	}
}
