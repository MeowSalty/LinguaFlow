package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
)

func TestAuthRulesInFlightMappingChange(t *testing.T) {
	for _, stage := range []string{"before_callback", "during_callback", "exchange", "complete"} {
		t.Run(stage, func(t *testing.T) {
			now := time.Now().UTC()
			svcs, clients, protocol := externalFlowServices(t, dialect.SQLite, now)
			s, c, ctx := svcs[0], clients[0], context.Background()
			p, err := readExternalConnection(ctx, c, "flow-oidc", 0)
			if err != nil {
				t.Fatal(err)
			}
			cfg := *p.Config
			cfg.AttributeMappings = departmentMapping()
			policy := policyInput(p.Policy)
			policy.LoginRules = departmentRule()
			if err = saveExternalSnapshot(ctx, c, "flow-oidc", 2, 1, &cfg, policy); err != nil {
				t.Fatal(err)
			}
			c.AuthConnection.UpdateOneID("flow-oidc").SetAggregateRevision(2).SetCurrentRevision(2).ExecX(ctx)
			change := func() {
				next := cfg
				next.AttributeMappings = departmentMapping()
				next.AttributeMappings[0].Pointer = "/new_department"
				if e := saveExternalSnapshot(ctx, c, "flow-oidc", 3, 1, &next, policy); e != nil {
					t.Fatal(e)
				}
				c.AuthConnection.UpdateOneID("flow-oidc").SetAggregateRevision(3).SetCurrentRevision(3).ExecX(ctx)
			}
			protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
				if stage == "during_callback" {
					change()
				}
				return authoidc.Identity{Issuer: cfg.OidcIssuer, Subject: "in-flight", Sources: map[string]json.RawMessage{"oidc_id_token": json.RawMessage(`{"department":"engineering"}`)}}, nil
			}
			flowTestStart(t, s, "flow-oidc")
			if stage == "before_callback" {
				change()
			}
			cb, err := s.HandleCallback(ctx, "flow-oidc", protocol.state, "authorization-code-secret", "")
			if err != nil {
				t.Fatal(err)
			}
			if stage == "exchange" {
				change()
			}
			result, err := s.Exchange(ctx, nil, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier})
			if stage != "complete" {
				if !errors.Is(err, ErrAuthConnectionConflict) {
					t.Fatalf("old mapping admitted at %s: %v", stage, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			change()
			_, err = s.Complete(ctx, nil, ExternalCompleteInput{ConfirmationToken: result.Confirmation.ConfirmationToken, Verifier: flowTestVerifier, Username: "inflight-user"})
			if !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatalf("old confirmation admitted: %v", err)
			}
		})
	}
}

func TestAuthRulesTestCandidateChangedDuringCallback(t *testing.T) {
	now := time.Now().UTC()
	svcs, clients, protocol := externalFlowServices(t, dialect.SQLite, now)
	s, c := svcs[0], clients[0]
	admin := createPasswordTestUser(t, c, "draft-test-admin", "password-123")
	admin = c.User.UpdateOneID(admin.ID).SetRole(SystemRoleAdmin).SaveX(context.Background())
	s.now = func() time.Time { return now }
	ctx := adminSessionContext(t, c, admin)
	protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
		c.AuthConnection.UpdateOneID("flow-oidc").SetAggregateRevision(2).ExecX(context.Background())
		return authoidc.Identity{Issuer: "https://issuer.example.test", Subject: "test-admin", AuthTime: now}, nil
	}
	in := AuthConnectionTestInput{
		AuthConnectionCandidateInput: AuthConnectionCandidateInput{ExpectedRevision: 1, Changes: &AuthConnectionChanges{ClientSecret: json.RawMessage(`"inflight-test-secret"`)}},
		ExternalBrowserInput:         ExternalBrowserInput{BrowserChallenge: browserChallenge(flowTestVerifier), CompletionURL: "https://app.example.test/auth/complete"},
	}
	freshBefore, err := readExternalConnection(context.Background(), c, "flow-oidc", 0)
	if err != nil {
		t.Fatal(err)
	}
	started, err := s.StartTest(ctx, admin.ID, "flow-oidc", in)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := s.HandleCallback(context.Background(), "flow-oidc", protocol.state, "authorization-code-secret", "")
	if err != nil {
		t.Fatal(err)
	}
	if !cb.Failed {
		t.Fatal("callback retained stale test candidate after network exchange")
	}
	if _, err = s.Exchange(ctx, &admin.ID, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier}); !errors.Is(err, ErrAuthConnectionConflict) {
		t.Fatalf("stale test result: %v", err)
	}
	row := c.AuthTransaction.GetX(context.Background(), started.TransactionID)
	if row.TestValidatedAt != nil || row.TestAppliedRevision != nil {
		t.Fatal("stale test persisted a proof capability")
	}
	current, err := readExternalConnection(context.Background(), c, "flow-oidc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if current.Config.FreshSupported != freshBefore.Config.FreshSupported {
		t.Fatal("stale test changed saved validation freshness")
	}
	if current.Connection.CurrentRevision != freshBefore.Connection.CurrentRevision {
		t.Fatal("stale test replaced the saved configuration")
	}
}

func TestAuthRulesTemporarySourceAndPurposeIsolation(t *testing.T) {
	now := time.Now().UTC()
	svcs, clients, protocol := externalFlowServices(t, dialect.SQLite, now)
	s, c, ctx := svcs[0], clients[0], context.Background()
	p, err := readExternalConnection(ctx, c, "flow-oidc", 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg := *p.Config
	cfg.AttributeMappings = departmentMapping()
	cfg.AttributeMappings[0].Source = "oidc_userinfo"
	cfg.OidcUserinfoEnabled = true
	policy := policyInput(p.Policy)
	policy.SignupRules = departmentRule()
	if err = saveExternalSnapshot(ctx, c, "flow-oidc", 2, 1, &cfg, policy); err != nil {
		t.Fatal(err)
	}
	c.AuthConnection.UpdateOneID("flow-oidc").SetAggregateRevision(2).SetCurrentRevision(2).ExecX(ctx)
	protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
		return authoidc.Identity{Issuer: cfg.OidcIssuer, Subject: "unavailable", SourceErrors: map[string]string{"oidc_userinfo": "source_unavailable"}}, nil
	}
	signup, signupVerifier, err := ruleFlow(t, s, protocol)
	if err != nil || signup.Kind != "signup_confirmation" || signup.Confirmation == nil {
		t.Fatalf("signup transient source: %+v, %v", signup, err)
	}
	before, identities, bindings, sessions, tokens := c.User.Query().CountX(ctx), c.ExternalIdentity.Query().CountX(ctx), c.LoginBinding.Query().CountX(ctx), c.RefreshSession.Query().CountX(ctx), c.RefreshToken.Query().CountX(ctx)
	invalidInput := ExternalCompleteInput{ConfirmationToken: signup.Confirmation.ConfirmationToken, Verifier: signupVerifier, Username: "signup-unavailable"}
	if _, err := s.Complete(ctx, nil, invalidInput); !errors.Is(err, ErrExternalUnavailable) {
		t.Fatalf("unavailable signup-only source at completion: %v (requirements=%v signup=%+v)", err, signup.Confirmation.SignupRequirements, signup.Confirmation.Signup)
	}
	if c.User.Query().CountX(ctx) != before || c.ExternalIdentity.Query().CountX(ctx) != identities || c.LoginBinding.Query().CountX(ctx) != bindings || c.RefreshSession.Query().CountX(ctx) != sessions || c.RefreshToken.Query().CountX(ctx) != tokens {
		t.Fatal("unavailable signup rule left partial account state")
	}
	protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
		return authoidc.Identity{Issuer: cfg.OidcIssuer, Subject: "unavailable", Sources: map[string]json.RawMessage{"oidc_userinfo": json.RawMessage(`{"department":"engineering"}`)}}, nil
	}
	confirmation, verifier, err := ruleFlow(t, s, protocol)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := s.Complete(ctx, nil, ExternalCompleteInput{ConfirmationToken: confirmation.Confirmation.ConfirmationToken, Verifier: verifier, Username: "signup-rules"})
	if err != nil {
		t.Fatal(err)
	}
	// Only signup needs UserInfo. An existing login remains valid without it.
	protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
		return authoidc.Identity{Issuer: cfg.OidcIssuer, Subject: "unavailable", SourceErrors: map[string]string{"oidc_userinfo": "source_unavailable"}}, nil
	}
	if out, _, e := ruleFlow(t, s, protocol); e != nil || out.Session == nil {
		t.Fatalf("signup rule applied to bound login: %v", e)
	}
	s.auth.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err = s.auth.Refresh(ctx, completed.Session.RefreshToken); err != nil {
		t.Fatalf("signup evidence imposed hourly login limit: %v", err)
	}
}
