package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func b5ReadLocal(t *testing.T, router http.Handler, token string) service.LocalAccountRecord {
	t.Helper()
	w := accountRequest(router, http.MethodGet, "/admin/local-account", token, "")
	accountAssertStatus(t, w, http.StatusOK)
	var record service.LocalAccountRecord
	if err := json.Unmarshal(w.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestRouter_AuthenticationB5LocalPolicyContract(t *testing.T) {
	s, c, token := b4ConnectionServer(t)
	router := s.newRouter()
	current := b5ReadLocal(t, router, token)
	if current.Revision != 1 || !current.Policy.Signup.Enabled || current.Policy.Signup.EmailRequirement != "optional" {
		t.Fatal("local default contract")
	}
	accountAssertStatus(t, accountRequest(router, http.MethodGet, "/admin/local-account", "", ""), http.StatusUnauthorized)
	user := c.User.Create().SetUsername("b5-normal-user").SetRole(service.SystemRoleUser).SaveX(t.Context())
	userToken := authTestToken(t, user, time.Now().Add(time.Hour), []byte(authTestSecret))
	accountAssertStatus(t, accountRequest(router, http.MethodGet, "/admin/local-account", userToken, ""), http.StatusForbidden)
	for _, tc := range []struct{ method, path, body string }{{http.MethodGet, "/admin/auth-connections/password", ""}, {http.MethodPatch, "/admin/auth-connections/password", `{"expected_revision":1}`}, {http.MethodPost, "/admin/auth-connections/password/test", `{"expected_revision":1,"browser_challenge":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","completion_url":"https://lingua.test/complete"}`}} {
		accountAssertStatus(t, accountRequest(router, tc.method, tc.path, token, tc.body), http.StatusNotFound)
	}
	listed := accountRequest(router, http.MethodGet, "/admin/auth-connections", token, "")
	accountAssertStatus(t, listed, http.StatusOK)
	if strings.Contains(listed.Body.String(), `"kind":"password"`) {
		t.Fatal("external list exposed internal password source")
	}
	noop := accountJSON(t, service.LocalAccountPatchInput{ExpectedRevision: current.Revision, Policy: current.Policy})
	accountAssertStatus(t, accountRequest(router, http.MethodPatch, "/admin/local-account", token, noop), http.StatusOK)
	if b5ReadLocal(t, router, token).Revision != current.Revision {
		t.Fatal("no-op incremented policy revision")
	}
	for _, body := range []string{`{}`, `{"expected_revision":1}`, `{"expected_revision":1,"policy":{}}`, strings.Replace(noop, `"password_login_enabled":true,`, "", 1), strings.Replace(noop, `"email_requirement":"optional"`, `"email_requirement":"unsupported"`, 1), strings.Replace(noop, `"email_login_enabled":false`, `"email_login_enabled":true`, 1), strings.Replace(noop, `"policy":{`, `"unknown":true,"policy":{`, 1)} {
		accountAssertStatus(t, accountRequest(router, http.MethodPatch, "/admin/local-account", token, body), http.StatusBadRequest)
	}
	stale := accountJSON(t, service.LocalAccountPatchInput{ExpectedRevision: 2, Policy: current.Policy})
	accountAssertStatus(t, accountRequest(router, http.MethodPatch, "/admin/local-account", token, stale), http.StatusConflict)
	catalog := accountRequest(router, http.MethodGet, "/admin/local-account/attributes?expected_revision=1", token, "")
	accountAssertStatus(t, catalog, http.StatusOK)
	if !strings.Contains(catalog.Body.String(), "registration.email_domain") || strings.Contains(catalog.Body.String(), "external.") {
		t.Fatal("local catalogue violated fixed signup purpose")
	}
	if !strings.Contains(catalog.Body.String(), "allowed_operators") {
		t.Fatal("catalogue missing field operators")
	}
	preview := accountRequest(router, http.MethodPost, "/admin/local-account/preview", token, `{"expected_revision":1,"registration":{"email":null}}`)
	accountAssertStatus(t, preview, http.StatusOK)
	var body map[string]any
	if err := json.Unmarshal(preview.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["simulated"] != true || body["signup"].(map[string]any)["status"] != "pass" {
		t.Fatal("preview did not evaluate local signup")
	}
}

func TestRouter_AuthenticationB5LocalEmailMatrix(t *testing.T) {
	for _, requirement := range []string{"optional", "required"} {
		t.Run(requirement, func(t *testing.T) {
			s, c, token := b4ConnectionServer(t)
			router := s.newRouter()
			current := b5ReadLocal(t, router, token)
			policy := current.Policy
			policy.Signup.EmailRequirement = requirement
			accountAssertStatus(t, accountRequest(router, http.MethodPatch, "/admin/local-account", token, accountJSON(t, service.LocalAccountPatchInput{ExpectedRevision: current.Revision, Policy: policy})), http.StatusOK)
			for i, email := range []string{"", `,"email":null`, `,"email":""`, `,"email":"invalid"`, `,"email":"valid@example.com"`} {
				want := http.StatusCreated
				if i == 3 || requirement == "required" && i < 3 {
					want = http.StatusBadRequest
				}
				payload := fmt.Sprintf(`{"username":"http-email-%s-%d","password":"password-123"%s}`, requirement, i, email)
				before := c.User.Query().CountX(t.Context())
				w := accountRequest(router, http.MethodPost, "/auth/register", "", payload)
				accountAssertStatus(t, w, want)
				if want == http.StatusBadRequest && c.User.Query().CountX(t.Context()) != before {
					t.Fatal("invalid email left an account")
				}
			}
		})
	}
}

func TestRouter_AuthenticationB5DomainRuleBeforeAny(t *testing.T) {
	s, _, token := b4ConnectionServer(t)
	router := s.newRouter()
	current := b5ReadLocal(t, router, token)
	policy := current.Policy
	policy.Signup.EmailRequirement = "required"
	policyJSON := accountJSON(t, policy)
	policyJSON = strings.Replace(policyJSON, `{"mode":"unrestricted"}`, `{"mode":"conditions","match":"any","conditions":[{"field_id":"registration.email_domain","operator":"eq","value":"example.com"},{"field_id":"registration.email","operator":"ne","value":"other@example.org"}]}`, 1)
	accountAssertStatus(t, accountRequest(router, http.MethodPatch, "/admin/local-account", token, `{"expected_revision":1,"policy":`+policyJSON+`}`), http.StatusOK)
	for _, email := range []string{`null`, `""`, `"a@[127.0.0.1]"`, `"invalid"`} {
		accountAssertStatus(t, accountRequest(router, http.MethodPost, "/auth/register", "", `{"username":"http-any-b5","password":"password-123","email":`+email+`}`), http.StatusBadRequest)
	}
	accountAssertStatus(t, accountRequest(router, http.MethodPost, "/auth/register", "", `{"username":"http-any-denied","password":"password-123","email":"other@example.org"}`), http.StatusForbidden)
}

func TestRouter_AuthenticationB5PurposeCatalogAndReadOnlyMetadata(t *testing.T) {
	s, c, token := b4ConnectionServer(t)
	router := s.newRouter()
	created := accountRequest(router, http.MethodPost, "/admin/auth-connections", token, `{"kind":"oidc","display_name":"B5 purpose catalogue"}`)
	accountAssertStatus(t, created, http.StatusCreated)
	var record service.AuthConnectionRecord
	if err := json.Unmarshal(created.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	path := "/admin/auth-connections/" + record.ID
	for _, purpose := range []string{"login", "signup", "link"} {
		catalog := accountRequest(router, http.MethodGet, path+"/attributes?expected_revision=1&purpose="+purpose, token, "")
		accountAssertStatus(t, catalog, http.StatusOK)
		if strings.Contains(catalog.Body.String(), `"field_id":"registration.email"`) != (purpose == "signup") {
			t.Fatal("registration evidence escaped signup purpose")
		}
		if !strings.Contains(catalog.Body.String(), `"derived_from":["external.email"]`) || !strings.Contains(catalog.Body.String(), `"normalization_version"`) || !strings.Contains(catalog.Body.String(), `"trust_pair"`) {
			t.Fatal("catalogue omitted program-defined dependency metadata")
		}
	}
	for _, purpose := range []string{"", "reauthenticate", "unknown"} {
		accountAssertStatus(t, accountRequest(router, http.MethodGet, path+"/attributes?expected_revision=1&purpose="+purpose, token, ""), http.StatusBadRequest)
	}
	accountAssertStatus(t, accountRequest(router, http.MethodPost, path+"/attributes", token, `{"expected_revision":1,"purpose":"signup"}`), http.StatusOK)
	accountAssertStatus(t, accountRequest(router, http.MethodPost, path+"/attributes", token, `{"expected_revision":1}`), http.StatusBadRequest)
	for _, metadata := range []string{`"semantic_format":"email_domain"`, `"normalization_version":"trusted"`, `"derived_from":["external.email"]`, `"allowed_operators":["contains"]`, `"trust_pair":{"peer_field_id":"external.email"}`} {
		mapping := `{"expected_revision":1,"attribute_mappings":[{"field_id":"custom.assertion","label":"Assertion","type":"boolean","source":"oidc_id_token","pointer":"/claim",` + metadata + `}]}`
		accountAssertStatus(t, accountRequest(router, http.MethodPatch, path, token, mapping), http.StatusBadRequest)
		if c.AuthConnection.GetX(t.Context(), record.ID).AggregateRevision != 1 {
			t.Fatal("rejected mapping changed the connection")
		}
	}
	policy := record.Configuration.Policy
	policy.LoginRules.Mode, policy.LoginRules.Match = "conditions", "all"
	policyBody := accountJSON(t, policy)
	policyBody = strings.Replace(policyBody, `"login_rules":{"mode":"conditions","match":"all"}`, `"login_rules":{"mode":"conditions","match":"all","conditions":[{"field_id":"registration.email","operator":"eq","value":"alice@example.com"}]}`, 1)
	accountAssertStatus(t, accountRequest(router, http.MethodPatch, path, token, `{"expected_revision":1,"policy":`+policyBody+`}`), http.StatusBadRequest)
}

func TestRouter_AuthenticationB5CapabilitiesMatchSchema(t *testing.T) {
	s, _, token := b4ConnectionServer(t)
	router := s.newRouter()
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	local := accountRequest(router, http.MethodGet, "/admin/local-account", token, "")
	accountAssertStatus(t, local, http.StatusOK)
	var record map[string]any
	if err := json.Unmarshal(local.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if err := spec.Components.Schemas["LocalAccountCapabilities"].Value.VisitJSON(record["capabilities"]); err != nil {
		t.Fatal("generated capabilities schema rejected live response", err)
	}
	capabilities := accountRequest(router, http.MethodGet, "/auth/capabilities", "", "")
	accountAssertStatus(t, capabilities, http.StatusOK)
	var public map[string]any
	if err := json.Unmarshal(capabilities.Body.Bytes(), &public); err != nil {
		t.Fatal(err)
	}
	if public["password"] != nil || public["local_account"] == nil {
		t.Fatal("public capabilities retained replaced password object")
	}
	if err := spec.Components.Schemas["LocalAccountCapabilities"].Value.VisitJSON(public["local_account"]); err != nil {
		t.Fatal("public local policy violated the generated contract", err)
	}
}
