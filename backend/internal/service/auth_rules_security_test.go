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
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
)

func publishedRuleFixture(t *testing.T, s *ExternalAuthService, c *ent.Client, admin *ent.User, restricted bool) AuthConnectionRecord {
	t.Helper()
	ctx := context.Background()
	rec, err := s.CreateConnection(ctx, admin.ID, CreateAuthConnectionInput{Kind: "oidc", DisplayName: "Rules", OIDC: &OIDCConnectionInput{Issuer: "https://idp.example.test", ClientID: "rules"}, ClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	p := defaultExternalPolicy()
	p.Enabled = true
	p.LoginEnabled = true
	p.SignupEnabled = true
	p.LinkEnabled = true
	fields := departmentMapping()
	if restricted {
		p.LoginRules = departmentRule()
	}
	rec, err = s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, Policy: &p, AttributeMappings: &fields})
	if err != nil {
		t.Fatal(err)
	}
	validateConnectionCandidate(t, s, c, admin.ID, rec.ID, rec.AggregateRevision)
	rec, err = s.PublishConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func ruleBindingFixture(t *testing.T, c *ent.Client, owner *ent.User, id string, now time.Time) *ent.LoginBinding {
	t.Helper()
	ctx := context.Background()
	identity := c.ExternalIdentity.Create().SetUserID(owner.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://idp.example.test").SetSubject("subject-" + strconv.Itoa(owner.ID) + id).SaveX(ctx)
	return c.LoginBinding.Create().SetUserID(owner.ID).SetConnectionID(id).SetExternalIdentityID(identity.ID).SetLastVerifiedAt(now).SetVerifiedAuthGeneration(1).SetVerifiedLoginPolicyRevision(1).SaveX(ctx)
}

func TestAuthRulesPublishRechecksExpiredEvidence(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, admin := connectionTestServices(t, driver)
			s, c, ctx := svcs[0], clients[0], context.Background()
			now := time.Now().UTC()
			s.now = func() time.Time { return now }
			rec := publishedRuleFixture(t, s, c, admin, true)
			owner := c.User.Create().SetUsername("expiry-owner").SaveX(ctx)
			b := ruleBindingFixture(t, c, owner, rec.ID, now)
			p, err := readExternalConnection(ctx, c, rec.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			attrs, err := authrules.Extract("oidc", p.Config.AttributeMappings, map[string]json.RawMessage{"oidc_id_token": json.RawMessage(`{"department":"engineering"}`)}, nil, now)
			if err != nil {
				t.Fatal(err)
			}
			identity := c.ExternalIdentity.GetX(ctx, b.ExternalIdentityID)
			e := authrules.Evidence{Issuer: identity.Authority, Subject: identity.Subject, SourceFingerprint: sourceTrustFingerprint(p.Config), LoginPolicyRevision: 1, ObservedAt: now, ExpiresAt: now.Add(time.Minute), Attributes: attrs}
			c.LoginBinding.UpdateOneID(b.ID).SetEvidence(e).ExecX(ctx)
			policy := policyInput(p.Policy)
			policy.LoginRules.Conditions[0].Operator = "ne"
			policy.LoginRules.Conditions[0].Value = json.RawMessage(`"sales"`)
			rec, err = s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, Policy: &policy})
			if err != nil {
				t.Fatal(err)
			}
			impact, err := s.ImpactConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision)
			if err != nil || !impact.CanPublish || len(impact.PassUsers) != 1 {
				t.Fatalf("fresh candidate: %+v %v", impact, err)
			}
			now = e.ExpiresAt
			_, err = s.PublishConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision)
			var conflict *AuthenticationPublishConflict
			if !errors.As(err, &conflict) || len(conflict.Impact.UnknownUsers) != 1 {
				t.Fatalf("publish trusted prior impact: %v", err)
			}
			current := c.AuthConnection.GetX(ctx, rec.ID)
			if *current.PublishedRevision != *rec.PublishedRevision || current.DraftRevision == nil {
				t.Fatal("blocked publish replaced online snapshot")
			}
		})
	}
}

func TestAuthRulesPublishAndFallbackRemovalRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		for _, method := range []string{"password", "binding"} {
			t.Run(driver+"/"+method, func(t *testing.T) {
				svcs, clients, admin := connectionTestServices(t, driver)
				s, c, ctx := svcs[0], clients[0], context.Background()
				now := time.Now().UTC()
				rec := publishedRuleFixture(t, s, c, admin, false)
				owner := createPasswordTestUser(t, c, "race-rule-owner", "password-123")
				ruleBindingFixture(t, c, owner, rec.ID, now)
				action, target := "password_delete", PasswordConnectionID
				var fallbackBinding *ent.LoginBinding
				if method == "binding" {
					other := publishedRuleFixture(t, s, c, admin, false)
					fallbackBinding = ruleBindingFixture(t, c, owner, other.ID, now)
					owner = c.User.UpdateOneID(owner.ID).ClearPasswordHash().SaveX(ctx)
					action, target = "unlink", strconv.Itoa(fallbackBinding.ID)
				}
				proof, err := s.issueReauthenticationProof(ctx, c, owner, "oidc", rec.ID, action, target, now)
				if err != nil {
					t.Fatal(err)
				}
				p := rec.Published.Policy
				p.LoginRules = departmentRule()
				rec, err = s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, Policy: &p})
				if err != nil {
					t.Fatal(err)
				}
				start := make(chan struct{})
				pub, removal := make(chan error, 1), make(chan error, 1)
				go func() { <-start; _, e := s.PublishConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision); pub <- e }()
				go func() {
					<-start
					var e error
					if method == "password" {
						e = svcs[1].DeletePassword(ctx, owner.ID, proof.Proof)
					} else {
						e = svcs[1].Unlink(ctx, owner.ID, fallbackBinding.ID, proof.Proof)
					}
					removal <- e
				}()
				close(start)
				pubErr, removeErr := <-pub, <-removal
				if pubErr == nil && removeErr == nil {
					t.Fatal("publication and last fallback removal both committed")
				}
				if pubErr != nil && !errors.Is(pubErr, ErrAuthConnectionConflict) {
					t.Fatalf("publish race: %v", pubErr)
				}
				if removeErr != nil && !errors.Is(removeErr, ErrLoginMethodConflict) && !errors.Is(removeErr, ErrReauthenticationRequired) {
					t.Fatalf("removal race: %v", removeErr)
				}
				if pubErr != nil && removeErr != nil {
					t.Fatalf("neither serial outcome committed: %v / %v", pubErr, removeErr)
				}
				if pubErr == nil {
					fallback, err := connectionFallbackAt(ctx, c, c.User.GetX(ctx, owner.ID), rec.ID, time.Now().UTC())
					if err != nil || !fallback {
						t.Fatalf("published without fallback: %v", err)
					}
				}
			})
		}
	}
}

func TestAuthRulesPublishAndRefreshRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			svcs, clients, admin := connectionTestServices(t, driver)
			s, c, ctx := svcs[0], clients[0], context.Background()
			rec := publishedRuleFixture(t, s, c, admin, false)
			b := ruleBindingFixture(t, c, admin, rec.ID, time.Now().UTC())
			var initial *Session
			err := withOrganizationTransaction(ctx, c, func(tx *ent.Client) error {
				p, e := readExternalConnection(ctx, tx, rec.ID, 0)
				if e != nil {
					return e
				}
				initial, e = s.auth.createSessionForConnection(ctx, tx, admin, p.Connection, p.Policy.LoginPolicyRevision, b, nil)
				return e
			})
			if err != nil {
				t.Fatal(err)
			}
			p := rec.Published.Policy
			p.LoginRules = departmentRule()
			rec, err = s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, Policy: &p})
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			pub, refresh := make(chan error, 1), make(chan error, 1)
			go func() { <-start; _, e := s.PublishConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision); pub <- e }()
			go func() { <-start; _, e := svcs[1].auth.Refresh(ctx, initial.RefreshToken); refresh <- e }()
			close(start)
			if e := <-pub; e != nil {
				t.Fatal(e)
			}
			if e := <-refresh; e != nil && !errors.Is(e, ErrRefreshTokenRevoked) {
				t.Fatal(e)
			}
			if c.RefreshSession.Query().Where(refreshsession.ConnectionIDEQ(rec.ID), refreshsession.RevokedAtIsNil()).CountX(ctx) != 0 {
				t.Fatal("old rules retained a refresh chain")
			}
		})
	}
}

func TestAuthRulesPublishAndCompleteRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		for _, purpose := range []string{"signup", "link"} {
			t.Run(driver+"/"+purpose, func(t *testing.T) {
				svcs, clients, admin := connectionTestServices(t, driver)
				s, c, ctx := svcs[0], clients[0], context.Background()
				record := publishedRuleFixture(t, s, c, admin, false)
				protocol := &externalFlowTestProtocol{exchange: func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
					return authoidc.Identity{Issuer: "https://idp.example.test", Subject: "new-subject"}, nil
				}}
				for _, svc := range svcs {
					svc.cfg = config.AuthDeploymentConfig{PublicBaseURL: "https://app.example.test", CompletionURLs: []string{"https://app.example.test/auth/complete"}}
					svc.SetOIDCProtocol(protocol)
				}
				browser := ExternalBrowserInput{BrowserChallenge: browserChallenge(flowTestVerifier), CompletionURL: "https://app.example.test/auth/complete"}
				var actor *int
				target := record.ID
				if purpose == "link" {
					owner := c.User.Create().SetUsername("link-race-owner").SaveX(ctx)
					actor = &owner.ID
					ruleBindingFixture(t, c, owner, record.ID, time.Now().UTC())
					other := publishedRuleFixture(t, s, c, admin, false)
					target = other.ID
					proof, err := s.issueReauthenticationProof(ctx, c, owner, "oidc", record.ID, "link", target, time.Now().UTC())
					if err != nil {
						t.Fatal(err)
					}
					if _, err = s.StartLink(ctx, owner.ID, target, ExternalLinkStartInput{ExternalBrowserInput: browser, ReauthenticationProof: proof.Proof}); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := s.StartLogin(ctx, target, browser); err != nil {
						t.Fatal(err)
					}
				}
				callback, err := s.HandleCallback(ctx, target, protocol.state, "authorization-code-secret", "")
				if err != nil {
					t.Fatal(err)
				}
				confirmation, err := s.Exchange(ctx, actor, ExternalExchangeInput{Ticket: callback.Ticket, Verifier: flowTestVerifier})
				if err != nil {
					t.Fatal(err)
				}
				p := record.Published.Policy
				p.LoginRules = departmentRule()
				record, err = s.UpdateConnection(ctx, admin.ID, record.ID, UpdateAuthConnectionInput{ExpectedRevision: record.AggregateRevision, Policy: &p})
				if err != nil {
					t.Fatal(err)
				}
				sessions := c.RefreshSession.Query().CountX(ctx)
				start := make(chan struct{})
				publish, complete := make(chan error, 1), make(chan error, 1)
				go func() {
					<-start
					_, e := s.PublishConnection(ctx, admin.ID, record.ID, record.AggregateRevision)
					publish <- e
				}()
				go func() {
					<-start
					_, e := svcs[1].Complete(ctx, actor, ExternalCompleteInput{ConfirmationToken: confirmation.Confirmation.ConfirmationToken, Verifier: flowTestVerifier, Username: "signup-race-owner"})
					complete <- e
				}()
				close(start)
				pubErr, completeErr := <-publish, <-complete
				if purpose == "signup" {
					expectOneExternalWinner(t, []error{pubErr, completeErr}, ErrAuthConnectionConflict)
				} else {
					if !errors.Is(pubErr, ErrAuthConnectionConflict) || completeErr != nil {
						t.Fatalf("unverified binding became fallback: %v / %v", pubErr, completeErr)
					}
					if c.RefreshSession.Query().CountX(ctx) != sessions {
						t.Fatal("link completion signed a session")
					}
				}
			})
		}
	}
}

func TestAuthRulesTransferredAuthorizationAndOldProof(t *testing.T) {
	now := time.Now().UTC()
	svcs, clients, _ := externalFlowServices(t, dialect.SQLite, now)
	s, c, ctx := svcs[0], clients[0], context.Background()
	owner := createPasswordTestUser(t, c, "proof-rule-owner", "password-123")
	seedExternalFlowConnection(t, c, "other-oidc")
	evidence := authrules.Evidence{ObservedAt: now, ExpiresAt: now.Add(time.Minute)}
	proof, err := s.issueReauthenticationProof(ctx, c, owner, "oidc", "flow-oidc", "link", "other-oidc", now, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.ExpiresAt.Equal(evidence.ExpiresAt) {
		t.Fatal("proof extended evidence deadline")
	}
	verifier := "proof-transfer-browser-verifier"
	started, err := s.StartLink(ctx, owner.ID, "other-oidc", ExternalLinkStartInput{ExternalBrowserInput: ExternalBrowserInput{BrowserChallenge: browserChallenge(verifier), CompletionURL: "https://app.example.test/auth/complete"}, ReauthenticationProof: proof.Proof})
	if err != nil {
		t.Fatal(err)
	}
	row := c.AuthTransaction.GetX(ctx, started.TransactionID)
	if row.AuthorizationConnectionID != "flow-oidc" || row.AuthorizationExpiresAt == nil || !row.AuthorizationExpiresAt.Equal(proof.ExpiresAt) {
		t.Fatal("link lost proof origin or deadline")
	}
	if err = validateTransactionAuthorization(ctx, c, row, now); err != nil {
		t.Fatal(err)
	}
	old, err := s.issueReauthenticationProof(ctx, c, owner, "oidc", "flow-oidc", "password_set", PasswordConnectionID, now, evidence)
	if err != nil {
		t.Fatal(err)
	}
	c.AuthConnection.UpdateOneID("flow-oidc").AddAuthGeneration(1).ExecX(ctx)
	if err = validateTransactionAuthorization(ctx, c, row, now); !errors.Is(err, ErrReauthenticationRequired) {
		t.Fatalf("transferred old version: %v", err)
	}
	if _, err = s.consumeReauthenticationProof(ctx, c, owner.ID, old.Proof, "password_set", PasswordConnectionID); !errors.Is(err, ErrReauthenticationRequired) {
		t.Fatalf("old proof: %v", err)
	}
}
