package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestRouter_LoginAcceptsOnlyIdentifierAndPassword(t *testing.T) {
	s, client, account := accountTestServer(t)
	router := s.newRouter()
	invalid := []string{
		`{}`, `null`, `[]`, `{"identifier":"testuser"}`, `{"password":"original-password"}`,
		`{"username":"testuser","password":"original-password"}`,
		`{"identifier":"testuser","password":"original-password","username":"testuser"}`,
		`{"identifier":"testuser","password":"original-password","identifier_type":"username"}`,
		`{"Identifier":"testuser","password":"original-password"}`,
		`{"identifier":null,"password":"original-password"}`, `{"identifier":123,"password":"original-password"}`,
		`{"identifier":"testuser","password":null}`, `{"identifier":"testuser","password":true}`,
		`{"identifier":"testuser","identifier":"other","password":"original-password"}`,
		`{"identifier":"testuser","password":"wrong","password":"original-password"}`,
		`{"identifier":"testuser","password":"original-password"} {}`,
	}
	for _, body := range invalid {
		t.Run(body, func(t *testing.T) {
			accountAssertProblem(t, accountRequest(router, http.MethodPost, "/auth/login", "", body), http.StatusBadRequest, "")
		})
	}
	for _, identifier := range []string{"", "unknown-user", "a@b", account.Email, "invalid..name", "用户"} {
		w := accountRequest(router, http.MethodPost, "/auth/login", "", accountJSON(t, map[string]string{"identifier": identifier, "password": accountTestPassword}))
		accountAssertProblem(t, w, http.StatusUnauthorized, "urn:linguaflow:invalid-credentials")
	}
	accountLogin(t, router, " TESTUSER ", accountTestPassword)
	client.User.UpdateOne(account).SetActive(false).ExecX(t.Context())
	w := accountRequest(router, http.MethodPost, "/auth/login", "", accountJSON(t, map[string]string{"identifier": account.Username, "password": accountTestPassword}))
	accountAssertProblem(t, w, http.StatusUnauthorized, "urn:linguaflow:invalid-credentials")
}

func TestRouter_RegisterNullableEmailAndDuplicateUnverifiedAddresses(t *testing.T) {
	s, _, _ := accountTestServer(t)
	router := s.newRouter()
	for index, email := range []string{"", `,"email":null`, `,"email":""`, `,"email":"shared@example.com"`, `,"email":"shared@example.com"`} {
		name := []string{"without-email", "null-email", "empty-email", "shared-one", "shared-two"}[index]
		body := `{"username":"` + name + `","password":"registration-password"` + email + `}`
		w := accountRequest(router, http.MethodPost, "/auth/register", "", body)
		accountAssertStatus(t, w, http.StatusCreated)
		var session authSessionResponse
		if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
		if index < 3 && session.User.Email != nil {
			t.Fatalf("unset email must be null: %s", w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("session response must not be cached")
		}
	}
}

func TestRouter_AccountInputDiagnostics(t *testing.T) {
	s, client, account := accountTestServer(t)
	admin := client.User.Create().SetUsername("diagnostic-admin").SetRole(service.SystemRoleAdmin).SetPasswordHash(account.PasswordHash).SaveX(t.Context())
	adminToken := authTestToken(t, admin, time.Now().Add(time.Hour), []byte(authTestSecret))
	for _, path := range []string{"/auth/register", "/admin/users"} {
		t.Run(path, func(t *testing.T) {
			for _, tc := range []struct{ field, value string }{
				{"username", "invalid..username"},
				{"email", "invalid-email"},
			} {
				body := map[string]string{"username": "diagnostic-user", "password": "diagnostic-password"}
				body[tc.field] = tc.value
				w := accountRequest(s.newRouter(), http.MethodPost, path, adminToken, accountJSON(t, body))
				accountAssertProblem(t, w, http.StatusBadRequest, "urn:linguaflow:invalid-input")
				var problem problemDetails
				if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(problem.Detail, tc.field+": ") || strings.Contains(problem.Detail, tc.value) || strings.Contains(problem.Detail, body["password"]) {
					t.Fatalf("expected safe field diagnostic: %s", w.Body.String())
				}
			}
			w := accountRequest(s.newRouter(), http.MethodPost, path, adminToken, `{"username":" TESTUSER ","password":"diagnostic-password"}`)
			accountAssertProblem(t, w, http.StatusConflict, "urn:linguaflow:conflict")
			if !strings.Contains(w.Body.String(), "用户名不可用") {
				t.Fatalf("unexpected username conflict: %s", w.Body.String())
			}
		})
	}
}

func TestRouter_AuthCapabilitiesFailClosedAndLocalIsIndependent(t *testing.T) {
	s, client, account := accountTestServer(t)
	get := func() *service.AuthCapabilities {
		t.Helper()
		w := accountRequest(s.newRouter(), http.MethodGet, "/auth/capabilities", "", "")
		accountAssertStatus(t, w, http.StatusOK)
		var capabilities service.AuthCapabilities
		if err := json.Unmarshal(w.Body.Bytes(), &capabilities); err != nil {
			t.Fatal(err)
		}
		if capabilities.Connections == nil || len(capabilities.Connections) != 0 || capabilities.EmailVerificationEnabled || capabilities.PasswordResetEnabled || capabilities.Password.EmailLoginEnabled {
			t.Fatalf("unimplemented capabilities leaked: %s", w.Body.String())
		}
		return &capabilities
	}
	server := get()
	if server.Mode != "server" || !server.Password.CanLogin || !server.Password.CanSignup || !server.Password.UsernameLoginEnabled {
		t.Fatalf("wrong server capabilities: %+v", server)
	}
	client.AuthChannelPolicy.Delete().ExecX(t.Context())
	accountAssertProblem(t, accountRequest(s.newRouter(), http.MethodGet, "/auth/capabilities", "", ""), http.StatusServiceUnavailable, "urn:linguaflow:settings-unavailable")
	accountAssertProblem(t, accountRequest(s.newRouter(), http.MethodPost, "/auth/login", "", `{"identifier":"testuser","password":"original-password"}`), http.StatusServiceUnavailable, "urn:linguaflow:settings-unavailable")
	s.mode, s.localUser = config.ModeLocal, account
	local := get()
	if local.Mode != "local" || local.Password.CanLogin || local.Password.CanSignup || local.Password.UsernameLoginEnabled {
		t.Fatalf("wrong local capabilities: %+v", local)
	}
}

func TestRouter_VerifiedEmailCannotBeChangedByProfileOrAdministrator(t *testing.T) {
	s, client, account := accountTestServer(t)
	client.User.UpdateOne(account).SetEmailVerifiedAt(time.Now()).SetVerifiedEmailKey(account.Email).SetEmailVerificationSource("mail").ExecX(t.Context())
	token := authTestToken(t, account, time.Now().Add(time.Hour), []byte(authTestSecret))
	for _, body := range []string{`{"email":null}`, `{"email":"replacement@example.com"}`} {
		accountAssertProblem(t, accountRequest(s.newRouter(), http.MethodPut, "/users/me", token, body), http.StatusConflict, "urn:linguaflow:account-conflict")
	}
	admin := client.User.Create().SetUsername("test-admin").SetRole(service.SystemRoleAdmin).SetPasswordHash(account.PasswordHash).SaveX(t.Context())
	adminToken := authTestToken(t, admin, time.Now().Add(time.Hour), []byte(authTestSecret))
	for _, body := range []string{`{"email":null}`, `{"email":"replacement@example.com"}`} {
		accountAssertProblem(t, accountRequest(s.newRouter(), http.MethodPatch, "/admin/users/"+strconv.Itoa(account.ID), adminToken, body), http.StatusConflict, "urn:linguaflow:account-conflict")
	}
}

func TestRouter_AdminNullableEmailAndPasswordReset(t *testing.T) {
	s, client, account := accountTestServer(t)
	admin := client.User.Create().SetUsername("account-admin").SetRole(service.SystemRoleAdmin).SetPasswordHash(account.PasswordHash).SaveX(t.Context())
	adminToken := authTestToken(t, admin, time.Now().Add(time.Hour), []byte(authTestSecret))
	router := s.newRouter()
	create := accountRequest(router, http.MethodPost, "/admin/users", adminToken, `{"username":"managed-user","password":"managed-password","email":null}`)
	accountAssertStatus(t, create, http.StatusCreated)
	var managed userResponse
	if err := json.Unmarshal(create.Body.Bytes(), &managed); err != nil {
		t.Fatal(err)
	}
	if managed.Email != nil {
		t.Fatalf("administrator-created empty email is not null: %s", create.Body.String())
	}
	path := "/admin/users/" + strconv.Itoa(managed.ID)
	for _, body := range []string{`{"email":"shared@example.com"}`, `{"display_name":"Managed"}`, `{"email":null}`} {
		accountAssertStatus(t, accountRequest(router, http.MethodPatch, path, adminToken, body), http.StatusOK)
	}
	stored := client.User.GetX(t.Context(), managed.ID)
	if stored.Email != "" || stored.DisplayName != "Managed" {
		t.Fatalf("administrator email omission/clear failed: %+v", stored)
	}
	for _, body := range []string{`{"email":null,"email":"duplicate@example.com"}`, `{"active":null}`, `{"username":"renamed"}`} {
		accountAssertProblem(t, accountRequest(router, http.MethodPatch, path, adminToken, body), http.StatusBadRequest, "")
	}
	session := accountLogin(t, router, account.Username, accountTestPassword)
	resetPath := "/admin/users/" + strconv.Itoa(account.ID) + "/password"
	accountAssertStatus(t, accountRequest(router, http.MethodPut, resetPath, adminToken, `{"new_password":"administrator-reset"}`), http.StatusNoContent)
	accountAssertProblem(t, accountRequest(router, http.MethodGet, "/users/me", session.AccessToken, ""), http.StatusUnauthorized, "urn:linguaflow:token-invalid")
	accountAssertProblem(t, accountRequest(router, http.MethodPost, "/auth/refresh", "", accountJSON(t, map[string]string{"refresh_token": session.RefreshToken})), http.StatusUnauthorized, "")
	accountLogin(t, router, account.Username, "administrator-reset")
}
