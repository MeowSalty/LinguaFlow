package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestRouter_AuthOAuthConfigurationContract(t *testing.T) {
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
	configBody := `{"authorization_endpoint":"https://oauth.test/authorize","token_endpoint":"https://oauth.test/token","profile_endpoint":"https://oauth.test/user","client_id":"client","subject_mapping":{"pointer":"/id","type":"integer"}}`
	valid := `{"kind":"generic_oauth2","display_name":"OAuth","oauth2":` + configBody + `,"client_secret":"private-oauth-test-secret"}`
	for _, body := range []string{
		strings.Replace(valid, `"oauth2":`, `"oidc":{},"oauth2":`, 1),
		strings.Replace(valid, `"generic_oauth2"`, `"oidc"`, 1),
		strings.Replace(valid, `"integer"`, `"number"`, 1),
		strings.Replace(valid, `"/id"`, `"/bad~3escape"`, 1),
		strings.Replace(valid, `https://oauth.test/user`, `https://oauth.test/user?access_token=secret`, 1),
		strings.Replace(valid, `"display_name"`, `"identity_authority_id":"https://github.com","display_name"`, 1),
	} {
		accountAssertStatus(t, accountRequest(router, http.MethodPost, "/admin/auth-connections", session.AccessToken, body), 400)
	}
	if c.AuthConnection.Query().CountX(t.Context()) != 1 {
		t.Fatal("invalid configuration created a connection")
	}
	created := accountRequest(router, http.MethodPost, "/admin/auth-connections", session.AccessToken, valid)
	accountAssertStatus(t, created, 201)
	var rec service.AuthConnectionRecord
	if err = json.Unmarshal(created.Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.IdentityAuthorityID == nil || *rec.IdentityAuthorityID == "" || rec.Draft == nil || rec.Draft.OAuth2 == nil || rec.Draft.OIDC != nil || !rec.HasClientSecret || rec.Draft.Policy.Enabled {
		t.Fatalf("incorrect OAuth connection response: %s", created.Body.String())
	}
	if strings.Contains(created.Body.String(), "private-oauth-test-secret") {
		t.Fatal("client secret was returned")
	}
	path := "/admin/auth-connections/" + rec.ID
	catalog := accountRequest(router, http.MethodGet, path+"/attributes?expected_revision=1", session.AccessToken, "")
	accountAssertStatus(t, catalog, 200)
	if strings.Contains(catalog.Body.String(), "oidc_id_token") || strings.Contains(catalog.Body.String(), "external.email") {
		t.Fatal("generic catalog claims OIDC default attributes")
	}
	bad := `{"expected_revision":1,"attribute_mappings":[{"field_id":"custom.name","label":"Name","type":"string","source":"oidc_id_token","pointer":"/name"}]}`
	accountAssertStatus(t, accountRequest(router, http.MethodPatch, path, session.AccessToken, bad), 400)
	good := strings.Replace(bad, "oidc_id_token", "oauth2_profile", 1)
	accountAssertStatus(t, accountRequest(router, http.MethodPatch, path, session.AccessToken, good), 200)
	preview := `{"expected_revision":2,"sources":{"oauth2_profile":{"name":"sample","email_verified":true,"auth_time":1234}}}`
	w := accountRequest(router, http.MethodPost, path+"/preview", session.AccessToken, preview)
	accountAssertStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"simulated":true`) || strings.Contains(w.Body.String(), "email_verified") || strings.Contains(w.Body.String(), "auth_time") {
		t.Fatal("preview promoted unrelated claims")
	}
	accountAssertStatus(t, accountRequest(router, http.MethodPost, path+"/publish", session.AccessToken, `{"expected_revision":2}`), 409)
}
