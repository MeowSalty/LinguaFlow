package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
)

// b4RulesFixture brings an external connection through the B4 management flow:
// configured, really tested through the mock provider callback and then enabled.
// Protocol-affecting changes consume the test proof; rule-only changes do not.
func b4RulesFixture(t *testing.T, f *b4Fixture, restricted bool) AuthConnectionRecord {
	t.Helper()
	rec := f.configured(t)
	var changes *AuthConnectionChanges
	if restricted {
		p := b4Policy()
		p.LoginRules = departmentRule()
		mapping := departmentMapping()
		changes = &AuthConnectionChanges{Policy: &p, AttributeMappings: &mapping}
	}
	return b4Reenable(t, f, rec, changes)
}

func b4Reenable(t *testing.T, f *b4Fixture, rec AuthConnectionRecord, changes *AuthConnectionChanges) AuthConnectionRecord {
	t.Helper()
	s := f.services[0]
	proof := f.test(t, rec, changes)
	var err error
	if changes != nil {
		rec, err = s.UpdateConnection(f.ctx, f.admin.ID, rec.ID, UpdateAuthConnectionInput{AuthConnectionChanges: *changes, ExpectedRevision: rec.AggregateRevision, TestID: proof.TestID, Verifier: b4Verifier})
		if err != nil {
			t.Fatal(err)
		}
	}
	rec, err = s.EnableConnection(f.ctx, f.admin.ID, rec.ID, AuthConnectionActionInput{ExpectedRevision: rec.AggregateRevision, TestID: proof.TestID, Verifier: b4Verifier})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func ruleBindingFixture(t *testing.T, c *ent.Client, owner *ent.User, id string, now time.Time, generation int64) *ent.LoginBinding {
	t.Helper()
	ctx := context.Background()
	identity := c.ExternalIdentity.Create().SetUserID(owner.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://idp.example.test").SetSubject("subject-" + strconv.Itoa(owner.ID) + id).SaveX(ctx)
	return c.LoginBinding.Create().SetUserID(owner.ID).SetConnectionID(id).SetExternalIdentityID(identity.ID).SetLastVerifiedAt(now).SetVerifiedAuthGeneration(generation).SetVerifiedLoginPolicyRevision(1).SaveX(ctx)
}

func TestAuthRulesSaveRechecksExpiredEvidence(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			f := newB4Fixture(t, driver)
			s, c, ctx := f.services[0], f.clients[0], f.ctx
			now := f.services[0].now()
			rec := b4RulesFixture(t, f, true)
			owner := c.User.Create().SetUsername("expiry-owner").SaveX(ctx)
			p, err := readExternalConnection(context.Background(), c, rec.ID, 0)
			b := ruleBindingFixture(t, c, owner, rec.ID, now, p.Connection.AuthGeneration)
			if err != nil {
				t.Fatal(err)
			}
			attrs, err := authrules.Extract("oidc", p.Config.AttributeMappings, map[string]json.RawMessage{"oidc_id_token": json.RawMessage(`{"department":"engineering"}`)}, nil, now)
			if err != nil {
				t.Fatal(err)
			}
			identity := c.ExternalIdentity.GetX(context.Background(), b.ExternalIdentityID)
			e := authrules.Evidence{Issuer: identity.Authority, Subject: identity.Subject, SourceFingerprint: sourceTrustFingerprint(p.Config), LoginPolicyRevision: 1, ObservedAt: now, ExpiresAt: now.Add(time.Minute), Attributes: attrs}
			e.LoginPolicyRevision = p.Policy.LoginPolicyRevision
			c.LoginBinding.UpdateOneID(b.ID).SetEvidence(e).ExecX(context.Background())
			policy := policyInput(p.Policy)
			policy.LoginRules.Conditions[0].Operator = "ne"
			policy.LoginRules.Conditions[0].Value = json.RawMessage(`"sales"`)
			candidate := AuthConnectionCandidateInput{ExpectedRevision: rec.AggregateRevision, Changes: &AuthConnectionChanges{Policy: &policy}}
			impact, err := s.ImpactConnection(ctx, f.admin.ID, rec.ID, candidate)
			if err != nil || !impact.CanSave || len(impact.PassUsers) != 1 {
				t.Fatalf("fresh candidate: %+v %v", impact, err)
			}
			f.clock.Store(e.ExpiresAt.UnixNano())
			_, err = s.UpdateConnection(ctx, f.admin.ID, rec.ID, UpdateAuthConnectionInput{AuthConnectionChanges: AuthConnectionChanges{Policy: &policy}, ExpectedRevision: rec.AggregateRevision})
			var blocked *AuthConnectionError
			if !errors.As(err, &blocked) || blocked.Code != "fallback-required" || blocked.Impact == nil || len(blocked.Impact.UnknownUsers) != 1 {
				t.Fatalf("save trusted prior impact: %v", err)
			}
			current := c.AuthConnection.GetX(context.Background(), rec.ID)
			if current.CurrentRevision != rec.AggregateRevision {
				t.Fatal("blocked save replaced online snapshot")
			}
		})
	}
}

func TestAuthRulesSaveAndFallbackRemovalRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		for _, method := range []string{"password", "binding"} {
			t.Run(driver+"/"+method, func(t *testing.T) {
				f := newB4Fixture(t, driver)
				s, c, ctx := f.services[0], f.clients[0], context.Background()
				now := f.services[0].now()
				rec := b4RulesFixture(t, f, true)
				owner := createPasswordTestUser(t, c, "race-rule-owner", "password-123")
				ruleBindingFixture(t, c, owner, rec.ID, now, c.AuthConnection.GetX(ctx, rec.ID).AuthGeneration)
				action, target := "password_delete", PasswordConnectionID
				var fallbackBinding *ent.LoginBinding
				if method == "binding" {
					other := b4RulesFixture(t, f, false)
					fallbackBinding = ruleBindingFixture(t, c, owner, other.ID, now, c.AuthConnection.GetX(ctx, other.ID).AuthGeneration)
					oauthWriteLocalPolicyTest(t, ctx, c, func(candidate *LocalAccountPolicyInput) {
						candidate.PasswordEnrollmentEnabled = false
					})
					action, target = "unlink", strconv.Itoa(fallbackBinding.ID)
				}
				proof, err := s.issueReauthenticationProof(ctx, c, owner, "oidc", rec.ID, action, target, now)
				if err != nil {
					t.Fatal(err)
				}
				p := rec.Configuration.Policy
				p.LoginRules = authrules.RuleSet{Mode: "conditions", Match: "all", Conditions: []authrules.Condition{{FieldID: "custom.department", Operator: "ne", Value: json.RawMessage(`"sales"`)}}}
				start := make(chan struct{})
				save, removal := make(chan error, 1), make(chan error, 1)
				go func() {
					<-start
					_, e := s.UpdateConnection(f.ctx, f.admin.ID, rec.ID, UpdateAuthConnectionInput{AuthConnectionChanges: AuthConnectionChanges{Policy: &p}, ExpectedRevision: rec.AggregateRevision})
					save <- e
				}()
				go func() {
					<-start
					var e error
					if method == "password" {
						e = f.services[1].DeletePassword(ctx, owner.ID, proof.Proof)
					} else {
						e = f.services[1].Unlink(ctx, owner.ID, fallbackBinding.ID, proof.Proof)
					}
					removal <- e
				}()
				close(start)
				saveErr, removeErr := <-save, <-removal
				if saveErr == nil && removeErr == nil {
					t.Fatal("rule save and last fallback removal both committed")
				}
				if saveErr != nil {
					var conflict *AuthConnectionError
					if !errors.As(saveErr, &conflict) || conflict.Code != "fallback-required" {
						t.Fatalf("save race: %v", saveErr)
					}
				}
				if removeErr != nil && !errors.Is(removeErr, ErrInvalidInput) && !errors.Is(removeErr, ErrLoginMethodConflict) && !errors.Is(removeErr, ErrReauthenticationRequired) {
					t.Fatalf("removal race: %v", removeErr)
				}
				if saveErr != nil && removeErr != nil {
					t.Fatalf("neither serial outcome committed: %v / %v", saveErr, removeErr)
				}
				if saveErr == nil {
					fallback, err := connectionFallbackAt(ctx, c, c.User.GetX(ctx, owner.ID), rec.ID, time.Now().UTC())
					if err != nil || !fallback {
						t.Fatalf("saved without fallback: %v", err)
					}
				}
			})
		}
	}
}

func TestAuthRulesSaveAndRefreshRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			f := newB4Fixture(t, driver)
			s, c, ctx := f.services[0], f.clients[0], f.ctx
			rec := b4RulesFixture(t, f, true)
			now := f.services[0].now()
			b := ruleBindingFixture(t, c, f.admin, rec.ID, now, c.AuthConnection.GetX(ctx, rec.ID).AuthGeneration)
			var initial *Session
			err := withOrganizationTransaction(ctx, c, func(tx *ent.Client) error {
				p, e := readExternalConnection(ctx, tx, rec.ID, 0)
				if e != nil {
					return e
				}
				initial, e = s.auth.createSessionForConnection(ctx, tx, f.admin, p.Connection, p.Policy.LoginPolicyRevision, b, nil)
				return e
			})
			if err != nil {
				t.Fatal(err)
			}
			p := rec.Configuration.Policy
			p.LoginRules = authrules.RuleSet{Mode: "conditions", Match: "all", Conditions: []authrules.Condition{{FieldID: "custom.department", Operator: "ne", Value: json.RawMessage(`"sales"`)}}}
			start := make(chan struct{})
			save, refresh := make(chan error, 1), make(chan error, 1)
			go func() {
				<-start
				_, e := s.UpdateConnection(ctx, f.admin.ID, rec.ID, UpdateAuthConnectionInput{AuthConnectionChanges: AuthConnectionChanges{Policy: &p}, ExpectedRevision: rec.AggregateRevision})
				save <- e
			}()
			go func() {
				<-start
				_, e := f.services[1].auth.Refresh(ctx, initial.RefreshToken)
				refresh <- e
			}()
			close(start)
			if e := <-save; e != nil {
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

func TestAuthRulesSaveAndCompleteRace(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		for _, purpose := range []string{"signup", "link"} {
			t.Run(driver+"/"+purpose, func(t *testing.T) {
				f := newB4Fixture(t, driver)
				s, c := f.services[0], f.clients[0]
				ctx := context.Background()
				now := f.services[0].now()
				record := b4RulesFixture(t, f, purpose == "link")
				browser := ExternalBrowserInput{BrowserChallenge: browserChallenge(flowTestVerifier), CompletionURL: "https://app.example.test/auth/complete"}
				var actor *int
				var authURL string
				target := record.ID
				if purpose == "link" {
					owner := c.User.Create().SetUsername("link-race-owner").SaveX(ctx)
					actor = &owner.ID
					ruleBindingFixture(t, c, owner, record.ID, now, c.AuthConnection.GetX(ctx, record.ID).AuthGeneration)
					other := b4RulesFixture(t, f, false)
					target = other.ID
					proof, err := s.issueReauthenticationProof(ctx, c, owner, "oidc", record.ID, "link", target, now)
					if err != nil {
						t.Fatal(err)
					}
					started, err := s.StartLink(ctx, owner.ID, target, ExternalLinkStartInput{ExternalBrowserInput: browser, ReauthenticationProof: proof.Proof})
					if err != nil {
						t.Fatal(err)
					}
					authURL = started.AuthorizationURL
				} else {
					started, err := s.StartLogin(ctx, target, browser)
					if err != nil {
						t.Fatal(err)
					}
					authURL = started.AuthorizationURL
				}
				u, err := url.Parse(authURL)
				if err != nil {
					t.Fatal(err)
				}
				callback, err := s.HandleCallback(ctx, target, u.Query().Get("state"), "authorization-code-secret", "")
				if err != nil {
					t.Fatal(err)
				}
				confirmation, err := s.Exchange(ctx, actor, ExternalExchangeInput{Ticket: callback.Ticket, Verifier: flowTestVerifier})
				if err != nil {
					t.Fatal(err)
				}
				p := record.Configuration.Policy
				p.LoginRules = authrules.RuleSet{Mode: "conditions", Match: "all", Conditions: []authrules.Condition{{FieldID: "custom.department", Operator: "ne", Value: json.RawMessage(`"sales"`)}}}
				changes := AuthConnectionChanges{Policy: &p}
				if purpose == "signup" {
					mapping := departmentMapping()
					changes.AttributeMappings = &mapping
				}
				sessions := c.RefreshSession.Query().CountX(ctx)
				start := make(chan struct{})
				save, complete := make(chan error, 1), make(chan error, 1)
				go func() {
					<-start
					_, e := s.UpdateConnection(f.ctx, f.admin.ID, record.ID, UpdateAuthConnectionInput{AuthConnectionChanges: changes, ExpectedRevision: record.AggregateRevision})
					save <- e
				}()
				go func() {
					<-start
					completeInput := ExternalCompleteInput{ConfirmationToken: confirmation.Confirmation.ConfirmationToken, Verifier: flowTestVerifier}
					if purpose == "signup" {
						completeInput.Username = "signup-race-owner"
					}
					_, e := f.services[1].Complete(ctx, actor, completeInput)
					complete <- e
				}()
				close(start)
				saveErr, completeErr := <-save, <-complete
				if purpose == "signup" {
					expectOneExternalWinner(t, []error{saveErr, completeErr}, ErrAuthConnectionConflict)
				} else {
					var blocked *AuthConnectionError
					if !errors.As(saveErr, &blocked) || blocked.Code != "fallback-required" {
						t.Fatalf("unverified linked binding must block fallback-dependent save: %v", saveErr)
					}
					if completeErr != nil {
						t.Fatalf("link complete race: %v", completeErr)
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
