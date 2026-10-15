package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
)

func departmentRule() authrules.RuleSet {
	return authrules.RuleSet{Mode: "conditions", Match: "all", Conditions: []authrules.Condition{{FieldID: "custom.department", Operator: "eq", Value: json.RawMessage(`"engineering"`)}}}
}
func departmentMapping() []authrules.Field {
	return []authrules.Field{{FieldID: "custom.department", Label: "Department", Type: "string", Source: "oidc_id_token", Pointer: "/department"}}
}

func ruleFlow(t *testing.T, s *ExternalAuthService, protocol *externalFlowTestProtocol) (ExternalExchangeResult, string, error) {
	t.Helper()
	ctx := context.Background()
	verifier := "rule-browser-verifier-with-enough-randomness"
	_, err := s.StartLogin(ctx, "flow-oidc", ExternalBrowserInput{BrowserChallenge: browserChallenge(verifier), CompletionURL: "https://app.example.test/auth/complete"})
	if err != nil {
		return ExternalExchangeResult{}, verifier, err
	}
	callback, err := s.HandleCallback(ctx, "flow-oidc", protocol.state, "authorization-code-secret", "")
	if err != nil {
		return ExternalExchangeResult{}, verifier, err
	}
	out, err := s.Exchange(ctx, nil, ExternalExchangeInput{Ticket: callback.Ticket, Verifier: verifier})
	return out, verifier, err
}

func TestAuthRulesPurposeAndEvidenceExpiry(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			now := time.Now().UTC()
			services, clients, protocol := externalFlowServices(t, driver, now)
			s, c, ctx := services[0], clients[0], context.Background()
			for _, svc := range services {
				svc.now = func() time.Time { return now }
				svc.auth.now = func() time.Time { return now }
			}
			p, err := readExternalConnection(ctx, c, "flow-oidc", 0)
			if err != nil {
				t.Fatal(err)
			}
			cfg := *p.Config
			cfg.AttributeMappings = departmentMapping()
			cfg.EvidenceMaxAgeSeconds = 60
			policy := policyInput(p.Policy)
			policy.LoginRules = departmentRule()
			policy.LinkRules = authrules.RuleSet{Mode: "conditions", Match: "all", Conditions: []authrules.Condition{{FieldID: "custom.department", Operator: "eq", Value: json.RawMessage(`"never"`)}}}
			if err = saveExternalSnapshot(ctx, c, "flow-oidc", 2, 2, &cfg, policy); err != nil {
				t.Fatal(err)
			}
			c.AuthConnection.UpdateOneID("flow-oidc").SetCurrentRevision(2).SetAggregateRevision(2).ExecX(ctx)
			department := "sales"
			protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
				raw, _ := json.Marshal(map[string]string{"sub": "rule-subject", "department": department})
				return authoidc.Identity{Issuer: cfg.OidcIssuer, Subject: "rule-subject", Sources: map[string]json.RawMessage{"oidc_id_token": raw}}, nil
			}
			before := c.User.Query().CountX(ctx)
			if _, _, err = ruleFlow(t, s, protocol); !errors.Is(err, ErrAuthenticationRuleDenied) {
				t.Fatalf("login rule denied signup: %v", err)
			}
			if c.User.Query().CountX(ctx) != before {
				t.Fatal("denied rule created account")
			}
			department = "engineering"
			confirmation, verifier, err := ruleFlow(t, s, protocol)
			if err != nil {
				t.Fatal(err)
			}
			completed, err := s.Complete(ctx, nil, ExternalCompleteInput{ConfirmationToken: confirmation.Confirmation.ConfirmationToken, Verifier: verifier, Username: "rule-owner"})
			if err != nil {
				t.Fatalf("initial registration must not execute link rule: %v", err)
			}
			session := completed.Session
			chain := c.RefreshSession.Query().Where(refreshsession.UserIDEQ(session.User.ID)).OnlyX(ctx)
			deadline := chain.Evidence.ExpiresAt
			if len(chain.Evidence.Attributes) != 1 || deadline.IsZero() {
				t.Fatal("missing minimal login evidence")
			}
			now = now.Add(30 * time.Second)
			refreshed, err := s.auth.Refresh(ctx, session.RefreshToken)
			if err != nil {
				t.Fatal(err)
			}
			if !c.RefreshSession.GetX(ctx, chain.ID).Evidence.ExpiresAt.Equal(deadline) {
				t.Fatal("rotation extended evidence")
			}
			now = deadline
			if _, err = s.auth.Refresh(ctx, refreshed.RefreshToken); !errors.Is(err, ErrRefreshTokenRevoked) {
				t.Fatalf("expiry boundary admitted refresh: %v", err)
			}
			if c.RefreshSession.GetX(ctx, chain.ID).RevokedAt == nil {
				t.Fatal("rejected refresh did not commit revocation")
			}
			// New authentication obtains new evidence, not a continuation of the old chain.
			out, _, err := ruleFlow(t, s, protocol)
			if err != nil || out.Session == nil {
				t.Fatalf("fresh login failed: %v", err)
			}
		})
	}
}

func TestAuthRulesUpdateUnknownAndFallback(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			f := newB4Fixture(t, driver)
			s, c, ctx := f.services[0], f.clients[0], f.ctx
			rec := f.enabled(t)
			owner := c.User.Create().SetUsername("no-fallback").SetActive(true).SaveX(ctx)
			identity := c.ExternalIdentity.Create().SetUserID(owner.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://idp.example.test").SetSubject("owner-subject").SaveX(ctx)
			binding := c.LoginBinding.Create().SetUserID(owner.ID).SetConnectionID(rec.ID).SetExternalIdentityID(identity.ID).SetLastVerifiedAt(time.Now().UTC()).SetVerifiedAuthGeneration(1).SetVerifiedLoginPolicyRevision(1).SaveX(ctx)
			mapping := departmentMapping()
			restricted := b4Policy()
			restricted.LoginRules = departmentRule()
			changes := AuthConnectionChanges{Policy: &restricted, AttributeMappings: &mapping}
			preview, err := s.PreviewConnectionRules(ctx, f.admin.ID, rec.ID, AuthRulesPreviewInput{ExpectedRevision: rec.AggregateRevision, Changes: &changes, Sources: map[string]json.RawMessage{"oidc_id_token": json.RawMessage(`{"department":"engineering"}`)}})
			if err != nil || preview.Rules.Login.Status != "pass" {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
			// Attribute/protocol-affecting candidate changes consume a fresh real
			// test, and the simulated result must never become stored evidence.
			result := f.test(t, rec, &changes)
			impact, err := s.ImpactConnection(ctx, f.admin.ID, rec.ID, AuthConnectionCandidateInput{ExpectedRevision: rec.AggregateRevision, Changes: &changes})
			if err != nil || impact.CanSave || len(impact.UnknownUsers) != 1 {
				t.Fatalf("simulated result became evidence: %+v %v", impact, err)
			}
			savedID, savedRevision := rec.ID, rec.AggregateRevision
			rec, err = s.UpdateConnection(ctx, f.admin.ID, rec.ID, UpdateAuthConnectionInput{AuthConnectionChanges: changes, ExpectedRevision: rec.AggregateRevision, TestID: result.TestID, Verifier: b4Verifier})
			var blocked *AuthConnectionError
			if !errors.As(err, &blocked) || blocked.Code != "fallback-required" || blocked.Impact == nil || len(blocked.Impact.UnknownUsers) != 1 {
				t.Fatalf("unsafe save without fallback: %v", err)
			}
			// Establish a usable local credential and prove actual control by logging in.
			hashed, err := hashPassword("verified-password")
			if err != nil {
				t.Fatal(err)
			}
			c.User.UpdateOneID(owner.ID).SetPasswordHash(hashed).ExecX(ctx)
			methods, err := s.LoginMethods(context.Background(), owner.ID)
			if err != nil || !methods.Password.CanLogin || methods.Password.Verified {
				t.Fatalf("password existence confused with verification: %+v %v", methods.Password, err)
			}
			if _, err = s.auth.Login(ctx, LoginInput{Identifier: owner.Username, Password: "verified-password"}); err != nil {
				t.Fatal(err)
			}
			methods, err = s.LoginMethods(context.Background(), owner.ID)
			if err != nil || !methods.Password.Verified {
				t.Fatalf("successful password login not reflected in methods: %v", err)
			}
			// The same proof now saves the restricted configuration with a verified
			// fallback; login rules bump the login policy revision, not evidence.
			rec, err = s.UpdateConnection(ctx, f.admin.ID, savedID, UpdateAuthConnectionInput{AuthConnectionChanges: changes, ExpectedRevision: savedRevision, TestID: result.TestID, Verifier: b4Verifier})
			if err != nil {
				t.Fatal(err)
			}
			if rec.Configuration.LoginPolicyRevision != 2 {
				t.Fatal("login version not incremented")
			}
			if c.LoginBinding.GetX(ctx, binding.ID).VerifiedLoginPolicyRevision != 1 {
				t.Fatal("save fabricated current evidence")
			}
			// An applied test cannot authorize a different revision.
			var reuseErr error
			var conflict *AuthConnectionError
			_, reuseErr = s.UpdateConnection(ctx, f.admin.ID, rec.ID, UpdateAuthConnectionInput{AuthConnectionChanges: changes, ExpectedRevision: rec.AggregateRevision, TestID: result.TestID, Verifier: b4Verifier})
			if !errors.As(reuseErr, &conflict) || conflict.Code != "state-conflict" {
				t.Fatalf("applied test reused for a new revision: %v", reuseErr)
			}
		})
	}
}

func TestAuthRulesOnlySignupChangePreservesSession(t *testing.T) {
	f := newB4Fixture(t, dialect.SQLite)
	s, c, ctx := f.services[0], f.clients[0], f.ctx
	rec := f.configured(t)
	// Registering the attribute map is itself a protocol-affecting save.
	mapping := departmentMapping()
	mappingChanges := AuthConnectionChanges{AttributeMappings: &mapping}
	proof := f.test(t, rec, &mappingChanges)
	rec, err := s.UpdateConnection(ctx, f.admin.ID, rec.ID, UpdateAuthConnectionInput{AuthConnectionChanges: mappingChanges, ExpectedRevision: rec.AggregateRevision, TestID: proof.TestID, Verifier: b4Verifier})
	if err != nil {
		t.Fatal(err)
	}
	rec, err = s.EnableConnection(ctx, f.admin.ID, rec.ID, AuthConnectionActionInput{ExpectedRevision: rec.AggregateRevision, TestID: proof.TestID, Verifier: b4Verifier})
	if err != nil {
		t.Fatal(err)
	}
	chain := c.RefreshSession.Create().SetUserID(f.admin.ID).SetAuthVersion(f.admin.AuthVersion).SetConnectionID(rec.ID).SetConnectionGeneration(1).SetLoginPolicyRevision(1).SetExpiresAt(time.Now().Add(time.Hour)).SaveX(ctx)
	// Signup-only rule changes never touch the login policy revision.
	signup := b4Policy()
	signup.SignupRules = departmentRule()
	rec, err = s.UpdateConnection(ctx, f.admin.ID, rec.ID, UpdateAuthConnectionInput{AuthConnectionChanges: AuthConnectionChanges{Policy: &signup}, ExpectedRevision: rec.AggregateRevision})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Configuration.LoginPolicyRevision != 1 || c.RefreshSession.GetX(ctx, chain.ID).RevokedAt != nil {
		t.Fatal("signup-only rule revoked login")
	}
}
