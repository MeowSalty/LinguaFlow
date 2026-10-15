package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authhttp"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
)

const b4Verifier = "0123456789012345678901234567890123456789012345678901234567890123"

type b4Protocol struct {
	calls    atomic.Int64
	exchange func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error)
}

func (p *b4Protocol) Check(context.Context, authoidc.Config) (authoidc.ProviderMetadata, error) {
	p.calls.Add(1)
	return authoidc.ProviderMetadata{}, nil
}
func (p *b4Protocol) BuildAuthorization(ctx context.Context, c authoidc.Config, r authoidc.AuthorizationRequest) (authoidc.Authorization, error) {
	p.calls.Add(1)
	return authoidc.Authorization{URL: "https://idp.example.test/authorize?state=" + url.QueryEscape(r.State)}, nil
}
func (p *b4Protocol) ExchangeAndVerify(ctx context.Context, c authoidc.Config, r authoidc.ExchangeRequest) (authoidc.Identity, error) {
	p.calls.Add(1)
	if p.exchange != nil {
		return p.exchange(ctx, c, r)
	}
	return authoidc.Identity{Issuer: c.Issuer, Subject: "test-subject", AuthTime: r.Now}, nil
}

type b4Fixture struct {
	services [2]*ExternalAuthService
	clients  [2]*ent.Client
	admin    *ent.User
	ctx      context.Context
	protocol *b4Protocol
	clock    *atomic.Int64
}

func newB4Fixture(t *testing.T, driver string) *b4Fixture {
	t.Helper()
	auth, clients := concurrentAuthClients(t, driver)
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().UnixNano())
	f := &b4Fixture{clients: clients, protocol: &b4Protocol{}, clock: now}
	f.admin = createPasswordTestUser(t, clients[0], "b4-admin", "password-123")
	f.admin = clients[0].User.UpdateOne(f.admin).SetRole(SystemRoleAdmin).SaveX(context.Background())
	gate := authhttp.NewGate()
	cfg := config.AuthDeploymentConfig{PublicBaseURL: "https://api.example.test/base", CompletionURLs: []string{"https://app.example.test/auth/complete"}, RequestTimeout: time.Second, MaxResponseBytes: 1 << 20, RateLimitPerMinute: 60}
	for i := range f.services {
		var err error
		auth[i].now = func() time.Time { return time.Unix(0, now.Load()).UTC() }
		f.services[i], err = NewExternalAuthService(clients[i], auth[i], credentialTestKeyring(t, "b4", "b4"), cfg, config.CORSConfig{AllowedOrigins: []string{"https://app.example.test"}})
		if err != nil {
			t.Fatal(err)
		}
		f.services[i].now = auth[i].now
		f.services[i].SetRequestGate(gate)
		f.services[i].SetOIDCProtocol(f.protocol)
	}
	f.ctx = f.login(t)
	return f
}
func (f *b4Fixture) login(t *testing.T) context.Context {
	t.Helper()
	session, err := f.services[0].auth.Login(context.Background(), LoginInput{Identifier: f.admin.Username, Password: "password-123"})
	if err != nil {
		t.Fatal(err)
	}
	_, claims, err := f.services[0].auth.ResolveUserFromAccessToken(context.Background(), session.AccessToken)
	if err != nil || claims.SessionID == 0 {
		t.Fatal("missing stable session claim", err)
	}
	return WithExternalActorSession(context.Background(), f.admin.ID, f.admin.AuthVersion, claims.SessionID)
}
func b4Policy() AuthConnectionPolicyInput {
	return AuthConnectionPolicyInput{LoginEnabled: true, SignupEnabled: true, LinkEnabled: true, LoginRules: authrules.Unrestricted(), SignupRules: authrules.Unrestricted(), LinkRules: authrules.Unrestricted()}
}
func (f *b4Fixture) configured(t *testing.T) AuthConnectionRecord {
	t.Helper()
	s := f.services[0]
	r, err := s.CreateConnection(f.ctx, f.admin.ID, CreateAuthConnectionInput{Kind: "oidc", DisplayName: "OIDC"})
	if err != nil {
		t.Fatal(err)
	}
	policy := b4Policy()
	r, err = s.UpdateConnection(f.ctx, f.admin.ID, r.ID, UpdateAuthConnectionInput{ExpectedRevision: r.AggregateRevision, AuthConnectionChanges: AuthConnectionChanges{OIDC: &OIDCConnectionInput{Issuer: "https://idp.example.test", ClientID: "client"}, ClientSecret: json.RawMessage(`"secret-v1"`), Policy: &policy}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *b4Fixture) test(t *testing.T, r AuthConnectionRecord, changes *AuthConnectionChanges) ExternalTestResult {
	t.Helper()
	s := f.services[0]
	start, err := s.StartTest(f.ctx, f.admin.ID, r.ID, AuthConnectionTestInput{AuthConnectionCandidateInput: AuthConnectionCandidateInput{ExpectedRevision: r.AggregateRevision, Changes: changes}, ExternalBrowserInput: ExternalBrowserInput{BrowserChallenge: browserChallenge(b4Verifier), CompletionURL: s.cfg.CompletionURLs[0]}})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.AuthorizationURL)
	callback, err := s.HandleCallback(context.Background(), r.ID, u.Query().Get("state"), "code", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Exchange(f.ctx, &f.admin.ID, ExternalExchangeInput{Ticket: callback.Ticket, Verifier: b4Verifier})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "test_result" || result.Session != nil || result.Proof != nil || result.Test == nil || result.Test.TestID != start.TransactionID || !result.Test.ExpiresAt.Equal(start.ExpiresAt) {
		t.Fatal("test issued a wrong purpose or deadline")
	}
	return *result.Test
}
func (f *b4Fixture) enabled(t *testing.T) AuthConnectionRecord {
	t.Helper()
	r := f.configured(t)
	test := f.test(t, r, nil)
	r, err := f.services[0].EnableConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision, TestID: test.TestID, Verifier: b4Verifier})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func b4Code(t *testing.T, err error, want string) {
	t.Helper()
	var ce *AuthConnectionError
	if !errors.As(err, &ce) || ce.Code != want {
		t.Fatalf("error %v, want %s", err, want)
	}
}

func TestAuthenticationB4(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			t.Run("setup before credentials and local checks", func(t *testing.T) {
				f := newB4Fixture(t, driver)
				s := f.services[0]
				for _, kind := range []string{"oidc", "generic_oauth2"} {
					r, err := s.CreateConnection(f.ctx, f.admin.ID, CreateAuthConnectionInput{Kind: kind, DisplayName: kind})
					if err != nil {
						t.Fatal(err)
					}
					if r.State != "configuring" || r.ConfigurationComplete || r.Setup.CallbackURL == nil || *r.Setup.CallbackURL != "https://api.example.test/base/api/v1/auth/external/"+r.ID+"/callback" || r.Capabilities.CanLogin {
						t.Fatal("incorrect initial setup")
					}
					if kind == "generic_oauth2" && r.IdentityAuthorityID == nil {
						t.Fatal("authority missing")
					}
					if _, err = s.CheckConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionCandidateInput{ExpectedRevision: r.AggregateRevision}); err != nil {
						t.Fatal(err)
					}
					if _, err = s.ConnectionAttributes(f.ctx, f.admin.ID, r.ID, AuthConnectionCandidateInput{ExpectedRevision: r.AggregateRevision}); err != nil {
						t.Fatal(err)
					}
				}
				if f.protocol.calls.Load() != 0 {
					t.Fatal("local setup requested provider")
				}
			})
			t.Run("real proof has no account effects and explicit enable", func(t *testing.T) {
				f := newB4Fixture(t, driver)
				s := f.services[0]
				r := f.configured(t)
				users := f.clients[0].User.Query().CountX(f.ctx)
				sessions := f.clients[0].RefreshSession.Query().CountX(f.ctx)
				test := f.test(t, r, nil)
				current, err := s.GetConnection(f.ctx, f.admin.ID, r.ID)
				if err != nil {
					t.Fatal(err)
				}
				if current.State != "configuring" || current.AggregateRevision != r.AggregateRevision || f.clients[0].User.Query().CountX(f.ctx) != users || f.clients[0].RefreshSession.Query().CountX(f.ctx) != sessions || f.clients[0].LoginBinding.Query().CountX(f.ctx) != 0 {
					t.Fatal("diagnostic changed account or config")
				}
				_, err = s.EnableConnection(f.login(t), f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision, TestID: test.TestID, Verifier: b4Verifier})
				b4Code(t, err, "test-mismatch")
				_, err = s.EnableConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision, TestID: test.TestID, Verifier: "wrong"})
				b4Code(t, err, "test-mismatch")
				r, err = s.EnableConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision, TestID: test.TestID, Verifier: b4Verifier})
				if err != nil {
					t.Fatal(err)
				}
				if r.State != "enabled" || !r.Validation.FreshSupported || !r.Capabilities.CanLogin {
					t.Fatal("enable failed to preserve capability")
				}
				f.clock.Add(int64(11 * time.Minute))
				if err = s.CleanupExpired(f.ctx); err != nil {
					t.Fatal(err)
				}
				current, err = s.GetConnection(f.ctx, f.admin.ID, r.ID)
				if err != nil || !current.Validation.FreshSupported || !current.Capabilities.CanLogin {
					t.Fatal("test expiry disabled current capability", err)
				}
			})
			t.Run("unsaved snapshot secret binding and atomic save", func(t *testing.T) {
				f := newB4Fixture(t, driver)
				s := f.services[0]
				r := f.enabled(t)
				changes := AuthConnectionChanges{ClientSecret: json.RawMessage(`"secret-v2"`)}
				test := f.test(t, r, &changes)
				before := f.clients[0].AuthConnectionSecret.Query().CountX(f.ctx)
				current, err := s.GetConnection(f.ctx, f.admin.ID, r.ID)
				if err != nil || current.AggregateRevision != r.AggregateRevision {
					t.Fatal("test mutated current config", err)
				}
				wrong := changes
				wrong.ClientSecret = json.RawMessage(`"different-secret"`)
				_, err = s.UpdateConnection(f.ctx, f.admin.ID, r.ID, UpdateAuthConnectionInput{ExpectedRevision: r.AggregateRevision, TestID: test.TestID, Verifier: b4Verifier, AuthConnectionChanges: wrong})
				b4Code(t, err, "test-mismatch")
				var reject atomic.Bool
				f.clients[0].ActivityLog.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
						if reject.Load() {
							return nil, errors.New("injected audit failure")
						}
						return next.Mutate(ctx, m)
					})
				})
				reject.Store(true)
				_, err = s.UpdateConnection(f.ctx, f.admin.ID, r.ID, UpdateAuthConnectionInput{ExpectedRevision: r.AggregateRevision, TestID: test.TestID, Verifier: b4Verifier, AuthConnectionChanges: changes})
				if err == nil {
					t.Fatal("audit failure committed")
				}
				reject.Store(false)
				current, err = s.GetConnection(f.ctx, f.admin.ID, r.ID)
				if err != nil || current.AggregateRevision != r.AggregateRevision || f.clients[0].AuthConnectionSecret.Query().CountX(f.ctx) != before {
					t.Fatal("failed save damaged current secret", err)
				}
				r, err = s.UpdateConnection(f.ctx, f.admin.ID, r.ID, UpdateAuthConnectionInput{ExpectedRevision: r.AggregateRevision, TestID: test.TestID, Verifier: b4Verifier, AuthConnectionChanges: changes})
				if err != nil {
					t.Fatal(err)
				}
				if r.Validation.AppliedTestID != test.TestID {
					t.Fatal("missing reconciliation receipt")
				}
				_, err = s.UpdateConnection(f.ctx, f.admin.ID, r.ID, UpdateAuthConnectionInput{ExpectedRevision: r.AggregateRevision - 1, TestID: test.TestID, Verifier: b4Verifier, AuthConnectionChanges: changes})
				b4Code(t, err, "revision-conflict")
			})
			t.Run("metadata no-op and competing stale saves", func(t *testing.T) {
				f := newB4Fixture(t, driver)
				r := f.enabled(t)
				observed := f.clients[0].AuthConnection.GetX(f.ctx, r.ID)
				before := observed.AuthGeneration
				start := make(chan struct{})
				results := make(chan error, 2)
				var wg sync.WaitGroup
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						<-start
						name := []string{"first", "second"}[i]
						_, err := f.services[i].UpdateConnection(f.ctx, f.admin.ID, r.ID, UpdateAuthConnectionInput{ExpectedRevision: r.AggregateRevision, AuthConnectionChanges: AuthConnectionChanges{DisplayName: &name}})
						results <- err
					}(i)
				}
				close(start)
				wg.Wait()
				close(results)
				wins := 0
				for err := range results {
					if err == nil {
						wins++
					} else {
						b4Code(t, err, "revision-conflict")
					}
				}
				if wins != 1 {
					t.Fatal("CAS lost update")
				}
				current, err := f.services[0].GetConnection(f.ctx, f.admin.ID, r.ID)
				if err != nil {
					t.Fatal(err)
				}
				projected, err := f.services[0].connectionRecord(f.ctx, f.clients[0], observed)
				if err != nil || projected.AggregateRevision != current.AggregateRevision || projected.Configuration.DisplayName != current.Configuration.DisplayName {
					t.Fatal("read projection mixed configuration generations", err)
				}
				name := current.Configuration.DisplayName
				noOp, err := f.services[0].UpdateConnection(f.ctx, f.admin.ID, r.ID, UpdateAuthConnectionInput{ExpectedRevision: current.AggregateRevision, AuthConnectionChanges: AuthConnectionChanges{DisplayName: &name}})
				if err != nil || noOp.AggregateRevision != current.AggregateRevision || f.clients[0].AuthConnection.GetX(f.ctx, r.ID).AuthGeneration != before {
					t.Fatal("metadata restarted authentication", err)
				}
			})
			t.Run("expiry parameter revision and disable fencing", func(t *testing.T) {
				f := newB4Fixture(t, driver)
				s := f.services[0]
				r := f.configured(t)
				test := f.test(t, r, nil)
				f.clock.Add(int64(10 * time.Minute))
				_, err := s.EnableConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision, TestID: test.TestID, Verifier: b4Verifier})
				b4Code(t, err, "test-expired")
				test = f.test(t, r, nil)
				s.cfg.PublicBaseURL += "/changed"
				_, err = s.EnableConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision, TestID: test.TestID, Verifier: b4Verifier})
				b4Code(t, err, "test-mismatch")
				s.cfg.PublicBaseURL = strings.TrimSuffix(s.cfg.PublicBaseURL, "/changed")
				r, err = s.DisableConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision})
				if err != nil {
					t.Fatal(err)
				}
				calls := f.protocol.calls.Load()
				_, err = s.StartTest(f.ctx, f.admin.ID, r.ID, AuthConnectionTestInput{AuthConnectionCandidateInput: AuthConnectionCandidateInput{ExpectedRevision: r.AggregateRevision}})
				b4Code(t, err, "state-conflict")
				_, err = s.EnableConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision, TestID: test.TestID, Verifier: b4Verifier})
				b4Code(t, err, "state-conflict")
				if f.protocol.calls.Load() != calls {
					t.Fatal("disabled connection made a request")
				}
				r, err = s.ConfigureConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision})
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.EnableConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision, TestID: test.TestID, Verifier: b4Verifier})
				if err == nil {
					t.Fatal("reconfiguration revived proof")
				}
			})
			t.Run("disable cancels exchange before committing", func(t *testing.T) {
				f := newB4Fixture(t, driver)
				s := f.services[0]
				r := f.configured(t)
				entered := make(chan struct{})
				f.protocol.exchange = func(ctx context.Context, _ authoidc.Config, _ authoidc.ExchangeRequest) (authoidc.Identity, error) {
					close(entered)
					<-ctx.Done()
					return authoidc.Identity{}, ctx.Err()
				}
				start, err := s.StartTest(f.ctx, f.admin.ID, r.ID, AuthConnectionTestInput{AuthConnectionCandidateInput: AuthConnectionCandidateInput{ExpectedRevision: r.AggregateRevision}, ExternalBrowserInput: ExternalBrowserInput{BrowserChallenge: browserChallenge(b4Verifier), CompletionURL: s.cfg.CompletionURLs[0]}})
				if err != nil {
					t.Fatal(err)
				}
				u, _ := url.Parse(start.AuthorizationURL)
				done := make(chan error, 1)
				go func() {
					_, err := s.HandleCallback(context.Background(), r.ID, u.Query().Get("state"), "code", "")
					done <- err
				}()
				<-entered
				r, err = s.DisableConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision})
				if err != nil {
					t.Fatal(err)
				}
				<-done
				row := f.clients[0].AuthTransaction.GetX(f.ctx, start.TransactionID)
				if r.State != "disabled" || row.TestValidatedAt != nil || row.Status != authtransaction.StatusCancelled {
					t.Fatal("late exchange remained valid")
				}
			})
			t.Run("identity cannot change across disable and restore", func(t *testing.T) {
				f := newB4Fixture(t, driver)
				s := f.services[0]
				r := f.enabled(t)
				row := f.clients[0].AuthConnection.GetX(f.ctx, r.ID)
				contract := row.IdentityContract
				r, err := s.ArchiveConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision})
				if err != nil {
					t.Fatal(err)
				}
				r, err = s.RestoreConnection(f.ctx, f.admin.ID, r.ID, AuthConnectionActionInput{ExpectedRevision: r.AggregateRevision})
				if err != nil {
					t.Fatal(err)
				}
				if r.State != "disabled" || r.Archived || f.clients[0].AuthConnection.GetX(f.ctx, r.ID).IdentityContract != contract {
					t.Fatal("restore changed identity")
				}
				_, err = s.UpdateConnection(f.ctx, f.admin.ID, r.ID, UpdateAuthConnectionInput{ExpectedRevision: r.AggregateRevision, AuthConnectionChanges: AuthConnectionChanges{OIDC: &OIDCConnectionInput{Issuer: "https://different.example.test", ClientID: "client"}}})
				b4Code(t, err, "state-conflict")
				if f.clients[0].AuthConnection.GetX(f.ctx, r.ID).State != authconnection.StateDisabled {
					t.Fatal("failed edit enabled connection")
				}
			})
		})
	}
}
