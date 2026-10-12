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
			c.AuthConnection.UpdateOneID("flow-oidc").SetPublishedRevision(2).SetAggregateRevision(2).ExecX(ctx)
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

func TestAuthRulesPublicationUnknownAndFallback(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			services, clients, admin := connectionTestServices(t, driver)
			s, c := services[0], clients[0]
			ctx := WithExternalActor(context.Background(), admin.ID, admin.AuthVersion)
			rec, err := s.CreateConnection(ctx, admin.ID, CreateAuthConnectionInput{Kind: "oidc", DisplayName: "Rules", OIDC: OIDCConnectionInput{Issuer: "https://idp.example.test", ClientID: "rule-client"}, ClientSecret: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			validateConnectionCandidate(t, s, c, admin.ID, rec.ID, *rec.DraftRevision)
			policy := defaultExternalPolicy()
			policy.Enabled = true
			policy.LoginEnabled = true
			policy.SignupEnabled = true
			policy.LinkEnabled = true
			rec, err = s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, Policy: &policy})
			if err != nil {
				t.Fatal(err)
			}
			rec, err = s.PublishConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision)
			if err != nil {
				t.Fatal(err)
			}
			owner := c.User.Create().SetUsername("no-fallback").SetActive(true).SaveX(ctx)
			identity := c.ExternalIdentity.Create().SetUserID(owner.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://idp.example.test").SetSubject("owner-subject").SaveX(ctx)
			binding := c.LoginBinding.Create().SetUserID(owner.ID).SetConnectionID(rec.ID).SetExternalIdentityID(identity.ID).SetLastVerifiedAt(time.Now().UTC()).SetVerifiedAuthGeneration(1).SetVerifiedLoginPolicyRevision(1).SaveX(ctx)
			mapping := departmentMapping()
			policy.LoginRules = departmentRule()
			rec, err = s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, Policy: &policy, AttributeMappings: &mapping})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := s.PreviewConnectionRules(ctx, admin.ID, rec.ID, AuthRulesPreviewInput{ExpectedRevision: rec.AggregateRevision, Sources: map[string]json.RawMessage{"oidc_id_token": json.RawMessage(`{"department":"engineering"}`)}})
			if err != nil || preview.Rules.Login.Status != "pass" {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
			impact, err := s.ImpactConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision)
			if err != nil || impact.CanPublish || len(impact.UnknownUsers) != 1 {
				t.Fatalf("simulated result became evidence: %+v %v", impact, err)
			}
			if _, err = s.PublishConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision); !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatalf("unsafe publication: %v", err)
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
			if _, err = s.PublishConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision); !errors.Is(err, ErrAuthConnectionConflict) {
				t.Fatalf("unverified password must not permit publication: %v", err)
			}
			if _, err = s.auth.Login(ctx, LoginInput{Identifier: owner.Username, Password: "verified-password"}); err != nil {
				t.Fatal(err)
			}
			methods, err = s.LoginMethods(context.Background(), owner.ID)
			if err != nil || !methods.Password.Verified {
				t.Fatalf("successful password login not reflected in methods: %v", err)
			}
			rec, err = s.PublishConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Published.LoginPolicyRevision != 2 {
				t.Fatal("login version not incremented")
			}
			if c.LoginBinding.GetX(ctx, binding.ID).VerifiedLoginPolicyRevision != 1 {
				t.Fatal("publication fabricated current evidence")
			}
		})
	}
}

func TestAuthRulesOnlySignupChangePreservesSession(t *testing.T) {
	services, clients, admin := connectionTestServices(t, dialect.SQLite)
	s, c := services[0], clients[0]
	ctx := WithExternalActor(context.Background(), admin.ID, admin.AuthVersion)
	rec, err := s.CreateConnection(ctx, admin.ID, CreateAuthConnectionInput{Kind: "oidc", DisplayName: "Rules", OIDC: OIDCConnectionInput{Issuer: "https://idp.example.test", ClientID: "client"}, ClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	validateConnectionCandidate(t, s, c, admin.ID, rec.ID, 1)
	policy := defaultExternalPolicy()
	policy.Enabled = true
	policy.LoginEnabled = true
	rec, err = s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: 1, Policy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	rec, err = s.PublishConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision)
	if err != nil {
		t.Fatal(err)
	}
	chain := c.RefreshSession.Create().SetUserID(admin.ID).SetAuthVersion(admin.AuthVersion).SetConnectionID(rec.ID).SetConnectionGeneration(1).SetLoginPolicyRevision(1).SetExpiresAt(time.Now().Add(time.Hour)).SaveX(ctx)
	fields := departmentMapping()
	policy.SignupRules = departmentRule()
	rec, err = s.UpdateConnection(ctx, admin.ID, rec.ID, UpdateAuthConnectionInput{ExpectedRevision: rec.AggregateRevision, Policy: &policy, AttributeMappings: &fields})
	if err != nil {
		t.Fatal(err)
	}
	rec, err = s.PublishConnection(ctx, admin.ID, rec.ID, rec.AggregateRevision)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Published.LoginPolicyRevision != 1 || c.RefreshSession.GetX(ctx, chain.ID).RevokedAt != nil {
		t.Fatal("signup-only rule revoked login")
	}

}
