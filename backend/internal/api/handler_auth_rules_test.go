package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestRouter_AuthRulesAdministratorPreview(t *testing.T) {
	s, c, admin := accountTestServer(t)
	admin = c.User.UpdateOneID(admin.ID).SetRole(service.SystemRoleAdmin).SaveX(t.Context())
	keys, _, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	s.externalAuth, err = service.NewExternalAuthService(c, s.authService, keys, config.AuthDeploymentConfig{})
	if err != nil {
		t.Fatal(err)
	}
	router := s.newRouter()
	session := accountLogin(t, router, admin.Username, accountTestPassword)
	created := accountRequest(router, http.MethodPost, "/admin/auth-connections", session.AccessToken, `{"kind":"oidc","display_name":"Rules","oidc":{"issuer":"https://id.example.test","client_id":"client"},"client_secret":"private-api-test-secret"}`)
	accountAssertStatus(t, created, http.StatusCreated)
	var rec service.AuthConnectionRecord
	if err = json.Unmarshal(created.Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	path := "/admin/auth-connections/" + rec.ID
	body := `{"expected_revision":1,"attribute_mappings":[{"field_id":"custom.department","label":"Department","type":"string","source":"oidc_id_token","pointer":"/department"}],"policy":{"login_enabled":true,"signup_enabled":true,"link_enabled":true,"login_rules":{"mode":"conditions","match":"all","conditions":[{"field_id":"custom.department","operator":"eq","value":"engineering"}]},"signup_rules":{"mode":"unrestricted"},"link_rules":{"mode":"unrestricted"}}}`
	accountAssertStatus(t, accountRequest(router, http.MethodPatch, path, session.AccessToken, body), http.StatusOK)
	catalog := accountRequest(router, http.MethodGet, path+"/attributes?expected_revision=2", session.AccessToken, "")
	accountAssertStatus(t, catalog, 200)
	if !strings.Contains(catalog.Body.String(), "custom.department") || strings.Contains(catalog.Body.String(), "private-api-test-secret") {
		t.Fatal("catalog contract/privacy")
	}
	sample := `{"expected_revision":2,"sources":{"oidc_id_token":{"sub":"simulated-user","department":"engineering","unrelated":"private-unmapped-value"}}}`
	countUsers, countBindings, countSessions := c.User.Query().CountX(t.Context()), c.LoginBinding.Query().CountX(t.Context()), c.RefreshSession.Query().CountX(t.Context())
	preview := accountRequest(router, http.MethodPost, path+"/preview", session.AccessToken, sample)
	accountAssertStatus(t, preview, 200)
	var got service.AuthRulesPreview
	if err = json.Unmarshal(preview.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Simulated || got.Rules.Login.Status != "pass" || got.Revision != 2 {
		t.Fatalf("preview: %+v", got)
	}
	if preview.Header().Get("Cache-Control") != "no-store" || strings.Contains(preview.Body.String(), "private-unmapped-value") {
		t.Fatal("preview leaked unrelated data")
	}
	if c.User.Query().CountX(t.Context()) != countUsers || c.LoginBinding.Query().CountX(t.Context()) != countBindings || c.RefreshSession.Query().CountX(t.Context()) != countSessions || c.AuthTransaction.Query().CountX(t.Context()) != 0 {
		t.Fatal("simulation became authentication state")
	}
	accountAssertStatus(t, accountRequest(router, http.MethodPost, path+"/preview", "", sample), 401)
	other := c.User.Create().SetUsername("preview-other").SetPasswordHash(admin.PasswordHash).SaveX(t.Context())
	otherSession := accountLogin(t, router, other.Username, accountTestPassword)
	accountAssertStatus(t, accountRequest(router, http.MethodPost, path+"/preview", otherSession.AccessToken, sample), 403)
	accountAssertStatus(t, accountRequest(router, http.MethodPost, path+"/preview", session.AccessToken, strings.Replace(sample, `"expected_revision":2`, `"expected_revision":1`, 1)), 409)
	for _, bad := range []string{`{"expected_revision":2,"sources":{"oidc_id_token":{"department":"a","department":"b"}}}`, `{"expected_revision":2,"sources":{"browser":{"department":"engineering"}}}`, `{"expected_revision":2,"sources":{"oidc_id_token":[]}}`} {
		accountAssertStatus(t, accountRequest(router, http.MethodPost, path+"/preview", session.AccessToken, bad), 400)
	}
}

func TestAuthenticationRuleErrorsArePrivate(t *testing.T) {
	s, _, _ := accountTestServer(t)
	for _, tc := range []struct {
		err    error
		status int
	}{{service.ErrAuthenticationRuleDenied, 403}, {service.ErrExternalUnavailable, 503}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/external/exchange", nil)
		s.writeExternalError(w, r, fmt.Errorf("private-department-value: %w", tc.err))
		accountAssertStatus(t, w, tc.status)
		if strings.Contains(w.Body.String(), "private-department-value") {
			t.Fatal("ordinary authentication response leaked evidence")
		}
	}
}
