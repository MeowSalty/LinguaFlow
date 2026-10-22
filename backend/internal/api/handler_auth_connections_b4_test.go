package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

const b4APIVerifier = "api-test-verifier-0123456789012345678901234567890123456789"

func TestRouter_AuthConnectionSpecificationMatchesIncompleteConfiguration(t *testing.T) {
	s, _, token := b4ConnectionServer(t)
	router := s.newRouter()
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	validate := func(name string, value any) {
		t.Helper()
		if err := spec.Components.Schemas[name].Value.VisitJSON(value); err != nil {
			t.Fatalf("%s schema rejected the live contract: %v", name, err)
		}
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var value map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, kind := range []string{"oidc", "generic_oauth2"} {
		payload := map[string]any{"kind": kind, "display_name": "Minimal"}
		validate("CreateAuthConnectionRequest", payload)
		body, _ := json.Marshal(payload)
		created := accountRequest(router, http.MethodPost, "/admin/auth-connections", token, string(body))
		accountAssertStatus(t, created, http.StatusCreated)
		record := decode(created)
		validate("AuthConnectionRecord", record)
		id := record["id"].(string)
		partial := map[string]any{"expected_revision": float64(1)}
		if kind == "oidc" {
			partial["oidc"] = map[string]any{"issuer": "https://idp.example.test"}
		} else {
			partial["oauth2"] = map[string]any{"client_id": "partial-client"}
		}
		validate("UpdateAuthConnectionRequest", partial)
		body, _ = json.Marshal(partial)
		updated := accountRequest(router, http.MethodPatch, "/admin/auth-connections/"+id, token, string(body))
		accountAssertStatus(t, updated, http.StatusOK)
		validate("AuthConnectionRecord", decode(updated))
		candidate := map[string]any{"expected_revision": float64(2), "changes": map[string]any{"client_secret": nil}}
		validate("AuthConnectionCandidateRequest", candidate)
		body, _ = json.Marshal(candidate)
		checked := accountRequest(router, http.MethodPost, "/admin/auth-connections/"+id+"/check", token, string(body))
		accountAssertStatus(t, checked, http.StatusOK)
		missingSwitches := `{"expected_revision":2,"policy":{"login_rules":{"mode":"unrestricted"},"signup_rules":{"mode":"unrestricted"},"link_rules":{"mode":"unrestricted"}}}`
		accountAssertStatus(t, accountRequest(router, http.MethodPatch, "/admin/auth-connections/"+id, token, missingSwitches), http.StatusBadRequest)
	}
	password := accountRequest(router, http.MethodGet, "/admin/auth-connections/password", token, "")
	accountAssertStatus(t, password, http.StatusNotFound)
}

// b4ConnectionProtocol answers discovery locally so the HTTP contract tests
// never contact a real provider; the browser exchange itself stays inside the
// service tests.
type b4ConnectionProtocol struct{}

func (b4ConnectionProtocol) Check(context.Context, authoidc.Config) (authoidc.ProviderMetadata, error) {
	return authoidc.ProviderMetadata{Issuer: "https://idp.example.test", SupportsPKCES256: true}, nil
}

func (b4ConnectionProtocol) BuildAuthorization(_ context.Context, _ authoidc.Config, req authoidc.AuthorizationRequest) (authoidc.Authorization, error) {
	if req.State == "" || req.RedirectURL == "" {
		return authoidc.Authorization{}, authoidc.ErrInvalidConfig
	}
	return authoidc.Authorization{URL: "https://idp.example.test/authorize?state=" + req.State, CodeChallenge: "b4-stub"}, nil
}

func (b4ConnectionProtocol) ExchangeAndVerify(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
	return authoidc.Identity{}, errors.New("not used")
}

func b4ConnectionServer(t *testing.T) (*Server, *ent.Client, string) {
	t.Helper()
	s, c, admin := accountTestServer(t)
	admin = c.User.UpdateOneID(admin.ID).SetRole(service.SystemRoleAdmin).SaveX(t.Context())
	keys, _, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	s.externalAuth, err = service.NewExternalAuthService(c, s.authService, keys, config.AuthDeploymentConfig{
		PublicBaseURL:  "https://lingua.test",
		CompletionURLs: []string{"https://lingua.test/complete"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.externalAuth.SetOIDCProtocol(b4ConnectionProtocol{})
	router := s.newRouter()
	session := accountLogin(t, router, admin.Username, accountTestPassword)
	return s, c, session.AccessToken
}

func b4Challenge() string {
	sum := sha256.Sum256([]byte(b4APIVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func b4TestInput(revision int64) string {
	return fmt.Sprintf(`{"expected_revision":%d,"browser_challenge":%q,"completion_url":"https://lingua.test/complete","return_to":"/"}`, revision, b4Challenge())
}

func b4EnableInput(revision int64, testID string) string {
	if testID == "" {
		return fmt.Sprintf(`{"expected_revision":%d}`, revision)
	}
	return fmt.Sprintf(`{"expected_revision":%d,"test_id":%q,"verifier":%q}`, revision, testID, b4APIVerifier)
}

func b4AssertTypedConflict(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	accountAssertStatus(t, w, http.StatusConflict)
	var problem struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Type != "urn:linguaflow:auth-connection-"+want {
		t.Fatalf("problem type: %s", problem.Type)
	}
}

func TestRouter_AuthConnectionLifecycleContract(t *testing.T) {
	s, c, token := b4ConnectionServer(t)
	router := s.newRouter()
	created := accountRequest(router, http.MethodPost, "/admin/auth-connections", token, `{"kind":"oidc","display_name":"B4","oidc":{"issuer":"https://idp.example.test","client_id":"client"},"client_secret":"private-b4-secret"}`)
	accountAssertStatus(t, created, http.StatusCreated)
	var rec service.AuthConnectionRecord
	if err := json.Unmarshal(created.Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.State != "configuring" || rec.AggregateRevision != 1 || rec.Archived || rec.IdentityAuthorityID != nil {
		t.Fatalf("created record: %+v", rec)
	}
	if rec.Configuration == nil || !rec.Configuration.HasClientSecret || rec.Configuration.LoginPolicyRevision != 1 {
		t.Fatalf("created snapshot: %+v", rec.Configuration)
	}
	if !rec.Setup.ConfigurationComplete || rec.Setup.CallbackURL == nil || *rec.Setup.CallbackURL != "https://lingua.test/api/v1/auth/external/"+rec.ID+"/callback" {
		t.Fatalf("created setup: %+v", rec.Setup)
	}
	if len(rec.Setup.CompletionURLs) == 0 || rec.Setup.Protocol.ScopeGuidance == "" || len(rec.Setup.Issues) != 0 {
		t.Fatalf("created setup details: %+v", rec.Setup)
	}
	if !rec.Capabilities.CanTest || rec.Capabilities.CanLogin || rec.Capabilities.CanSignup || rec.Capabilities.CanLink {
		t.Fatalf("created capabilities: %+v", rec.Capabilities)
	}
	if rec.Validation.AppliedTestID != "" || rec.Validation.TestedAt != nil {
		t.Fatalf("created validation: %+v", rec.Validation)
	}
	path := "/admin/auth-connections/" + rec.ID

	// Candidate check, attributes and preview are pure local evaluations.
	check := accountRequest(router, http.MethodPost, path+"/check", token, `{"expected_revision":1}`)
	accountAssertStatus(t, check, http.StatusOK)
	var inspected service.AuthConnectionCheckResult
	if err := json.Unmarshal(check.Body.Bytes(), &inspected); err != nil {
		t.Fatal(err)
	}
	if !inspected.Valid || inspected.Revision != 1 || inspected.FreshSupported || len(inspected.Issues) != 0 {
		t.Fatalf("check: %+v", inspected)
	}
	catalog := accountRequest(router, http.MethodPost, path+"/attributes", token, `{"expected_revision":1,"purpose":"signup","changes":{"policy":{"login_enabled":true,"signup_enabled":true,"link_enabled":true,"signup":{"email_requirement":"optional","password_requirement":"none"},"login_rules":{"mode":"unrestricted"},"signup_rules":{"mode":"unrestricted"},"link_rules":{"mode":"unrestricted"}}}}`)
	accountAssertStatus(t, catalog, http.StatusOK)
	preview := accountRequest(router, http.MethodPost, path+"/preview", token, `{"expected_revision":1,"sources":{"oidc_id_token":{"sub":"simulated"}}}`)
	accountAssertStatus(t, preview, http.StatusOK)

	// Enabling consumes a real completed connection test; nothing is bypassed.
	b4AssertTypedConflict(t, accountRequest(router, http.MethodPost, path+"/enable", token, b4EnableInput(1, "")), "test-required")
	b4AssertTypedConflict(t, accountRequest(router, http.MethodPost, path+"/enable", token, b4EnableInput(2, "")), "revision-conflict")

	// Starting a test binds the current admin session and browser proof and
	// answers with the transaction identity used by later lifecycle actions.
	start := accountRequest(router, http.MethodPost, path+"/test", token, b4TestInput(1))
	accountAssertStatus(t, start, http.StatusOK)
	var begun struct {
		TransactionID string `json:"transaction_id"`
		TestID        string `json:"test_id"`
		ExpiresAt     string `json:"expires_at"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &begun); err != nil {
		t.Fatal(err)
	}
	if begun.TransactionID == "" || begun.TestID != begun.TransactionID {
		t.Fatalf("test start: %+v", begun)
	}
	deadline, err := time.Parse(time.RFC3339, begun.ExpiresAt)
	if err != nil || !deadline.After(time.Now()) || deadline.After(time.Now().Add(12*time.Minute)) {
		t.Fatalf("test deadline: %s %v", begun.ExpiresAt, err)
	}
	// The stored proof stays uncompleted, so enabling it is still refused.
	b4AssertTypedConflict(t, accountRequest(router, http.MethodPost, path+"/enable", token, b4EnableInput(1, begun.TransactionID)), "test-required")
	// A verifier that does not match the browser proof of the test mismatches.
	mismatch := strings.Replace(b4EnableInput(1, begun.TransactionID), b4APIVerifier, strings.Repeat("W", 43), 1)
	b4AssertTypedConflict(t, accountRequest(router, http.MethodPost, path+"/enable", token, mismatch), "test-mismatch")

	before := c.AuthConnection.GetX(t.Context(), rec.ID)
	disabled := accountRequest(router, http.MethodPost, path+"/disable", token, `{"expected_revision":1}`)
	accountAssertStatus(t, disabled, http.StatusOK)
	var off service.AuthConnectionRecord
	if err := json.Unmarshal(disabled.Body.Bytes(), &off); err != nil {
		t.Fatal(err)
	}
	if off.State != "disabled" || off.AggregateRevision != 2 || off.Capabilities.CanTest {
		t.Fatalf("disabled record: %+v", off)
	}
	after := c.AuthConnection.GetX(t.Context(), rec.ID)
	if after.AuthGeneration != before.AuthGeneration+1 {
		t.Fatal("stopping formal use did not advance the revocation generation")
	}
	// No new external request can start for a disabled connection.
	b4AssertTypedConflict(t, accountRequest(router, http.MethodPost, path+"/test", token, b4TestInput(2)), "state-conflict")
	// Local reads and simulated previews keep working without any network.
	accountAssertStatus(t, accountRequest(router, http.MethodPost, path+"/check", token, `{"expected_revision":2}`), http.StatusOK)
	accountAssertStatus(t, accountRequest(router, http.MethodPost, path+"/preview", token, `{"expected_revision":2,"sources":{"oidc_id_token":{"sub":"simulated"}}}`), http.StatusOK)
	// Only an explicit reconfigure step reopens testing.
	reconfigured := accountRequest(router, http.MethodPost, path+"/configure", token, `{"expected_revision":2}`)
	accountAssertStatus(t, reconfigured, http.StatusOK)
	var back service.AuthConnectionRecord
	if err := json.Unmarshal(reconfigured.Body.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.State != "configuring" || back.AggregateRevision != 3 || !back.Capabilities.CanTest {
		t.Fatalf("reconfigured record: %+v", back)
	}
	final := accountRequest(router, http.MethodGet, path, token, "")
	accountAssertStatus(t, final, http.StatusOK)
	var current service.AuthConnectionRecord
	if err := json.Unmarshal(final.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.State != "configuring" || current.Capabilities.CanLogin {
		t.Fatalf("current record: %+v", current)
	}
	if accountRequest(router, http.MethodPost, path+"/publish", token, `{"expected_revision":3}`).Code != http.StatusNotFound {
		t.Fatal("publish endpoint must stay retired")
	}
}
