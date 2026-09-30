package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

const accountTestPassword = "original-password"

func accountTestServer(t *testing.T) (*Server, *ent.Client, *ent.User) {
	t.Helper()
	s, client, account := authTestServer(t)
	client.SystemSetting.Create().SetKey(service.SettingRegistrationEnabled).SetValue("true").SaveX(context.Background())
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(accountTestPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	account, err = client.User.UpdateOne(account).SetPasswordHash(string(passwordHash)).SetDisplayName("Original Name").Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.authService = service.NewAuthService(client, service.AuthConfig{
		Secret: []byte(authTestSecret), Issuer: authTestIssuer,
		AccessTokenTTL: time.Hour, RefreshTokenTTL: 24 * time.Hour,
	}, service.NewSettingsService(client))
	s.userService = service.NewUserService(client, s.authService)
	return s, client, account
}

func accountRequest(router http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func accountJSON(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func accountAssertStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, want, w.Body.String())
	}
}

func accountAssertProblem(t *testing.T, w *httptest.ResponseRecorder, want int, urn string) {
	t.Helper()
	accountAssertStatus(t, w, want)
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("content type = %q", w.Header().Get("Content-Type"))
	}
	var problem problemDetails
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Status != want || (urn != "" && problem.Type != urn) {
		t.Fatalf("unexpected problem: %+v; want status %d, type %q", problem, want, urn)
	}
}

func accountLogin(t *testing.T, router http.Handler, username, password string) authSessionResponse {
	t.Helper()
	w := accountRequest(router, http.MethodPost, "/auth/login", "", accountJSON(t, map[string]string{"username": username, "password": password}))
	accountAssertStatus(t, w, http.StatusOK)
	var session authSessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return session
}

func accountAssertUnchanged(t *testing.T, client *ent.Client, before *ent.User) {
	t.Helper()
	after, err := client.User.Get(context.Background(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.DisplayName != before.DisplayName || after.Email != before.Email || after.PasswordHash != before.PasswordHash || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("account changed on rejected or empty update: before=%+v, after=%+v", before, after)
	}
}

func TestRouter_AccountProfilePartialUpdates(t *testing.T) {
	cases := []struct {
		name, body, displayName, email string
	}{
		{"display name only", `{"display_name":"  New Name  "}`, "New Name", "test@test.com"},
		{"email only", `{"email":"  NEW@Example.COM  "}`, "Original Name", "new@example.com"},
		{"both fields", `{"display_name":" New ","email":"NEW@Example.COM"}`, "New", "new@example.com"},
		{"clear display name", `{"display_name":""}`, "", "test@test.com"},
		{"whitespace display name", `{"display_name":" \t "}`, "", "test@test.com"},
		{"empty object", `{}`, "Original Name", "test@test.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, client, account := accountTestServer(t)
			token := authTestToken(t, account, time.Now().Add(time.Hour), []byte(authTestSecret))
			w := accountRequest(s.newRouter(), http.MethodPut, "/users/me", token, tc.body)
			accountAssertStatus(t, w, http.StatusOK)
			var response userResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.DisplayName != tc.displayName || response.Email != tc.email || response.Username != account.Username || response.Role != account.Role || response.Active != account.Active {
				t.Fatalf("unexpected updated user: %+v", response)
			}
			stored, err := client.User.Get(context.Background(), account.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.DisplayName != tc.displayName || stored.Email != tc.email {
				t.Fatalf("update was not persisted: %+v", stored)
			}
			if tc.body == `{}` {
				accountAssertUnchanged(t, client, account)
			}
		})
	}
}

func TestRouter_AccountProfileRejectsInvalidUpdatesAtomically(t *testing.T) {
	s, client, account := accountTestServer(t)
	token := authTestToken(t, account, time.Now().Add(time.Hour), []byte(authTestSecret))
	router := s.newRouter()
	cases := map[string]string{
		"empty body": "", "null object": `null`, "array": `[]`, "string": `"profile"`,
		"malformed": `{"email":`, "trailing object": `{} {}`, "trailing null": `{} null`, "trailing garbage": `{} x`,
		"display null": `{"display_name":null}`, "email null": `{"email":null}`,
		"duplicate field":               `{"display_name":"First","display_name":"Second"}`,
		"null overwritten by duplicate": `{"email":null,"email":"new@example.com"}`,
		"display number":                `{"display_name":123}`, "email bool": `{"email":true}`, "email array": `{"email":[]}`,
		"email object": `{"email":{}}`, "username readonly": `{"username":"other"}`, "role readonly": `{"role":"admin"}`,
		"active readonly": `{"active":false}`, "unknown field": `{"other":"value"}`, "wrong case": `{"Email":"new@example.com"}`,
		"empty email":         `{"display_name":"Must Not Save","email":""}`,
		"whitespace email":    `{"display_name":"Must Not Save","email":" \t "}`,
		"bare at":             `{"display_name":"Must Not Save","email":"@"}`,
		"internal whitespace": `{"display_name":"Must Not Save","email":"first last@example.com"}`,
		"unicode whitespace":  `{"display_name":"Must Not Save","email":"first\u00a0last@example.com"}`,
		"display address":     `{"display_name":"Must Not Save","email":"Name <name@example.com>"}`,
		"wrapped address":     `{"display_name":"Must Not Save","email":"<name@example.com>"}`,
		"address comment":     `{"display_name":"Must Not Save","email":"name@example.com (Name)"}`,
		"multiple addresses":  `{"display_name":"Must Not Save","email":"one@example.com,two@example.com"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			w := accountRequest(router, http.MethodPut, "/users/me", token, body)
			accountAssertProblem(t, w, http.StatusBadRequest, "")
			accountAssertUnchanged(t, client, account)
		})
	}
	if _, err := client.User.Create().SetUsername("other").SetEmail("other@example.com").SetPasswordHash(account.PasswordHash).Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := accountRequest(router, http.MethodPut, "/users/me", token, `{"display_name":"Must Not Save","email":" OTHER@EXAMPLE.COM "}`)
	accountAssertProblem(t, w, http.StatusConflict, "urn:linguaflow:conflict")
	accountAssertUnchanged(t, client, account)
}

func TestRouter_AccountPasswordRequestValidation(t *testing.T) {
	s, client, account := accountTestServer(t)
	token := authTestToken(t, account, time.Now().Add(time.Hour), []byte(authTestSecret))
	router := s.newRouter()
	cases := map[string]string{
		"empty body": "", "null object": `null`, "array": `[]`, "empty object": `{}`,
		"missing current": `{"new_password":"new-password"}`, "missing new": `{"current_password":"original-password"}`,
		"empty current":     `{"current_password":"","new_password":"new-password"}`,
		"empty new":         `{"current_password":"original-password","new_password":""}`,
		"null current":      `{"current_password":null,"new_password":"new-password"}`,
		"null new":          `{"current_password":"original-password","new_password":null}`,
		"current bool":      `{"current_password":true,"new_password":"new-password"}`,
		"duplicate current": `{"current_password":"wrong","current_password":"original-password","new_password":"new-password"}`,
		"new number":        `{"current_password":"original-password","new_password":12345678}`,
		"new object":        `{"current_password":"original-password","new_password":{}}`,
		"unknown field":     `{"current_password":"original-password","new_password":"new-password","confirm_password":"new-password"}`,
		"trailing object":   `{"current_password":"original-password","new_password":"new-password"} {}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			w := accountRequest(router, http.MethodPut, "/users/me/password", token, body)
			accountAssertProblem(t, w, http.StatusBadRequest, "")
			var problem problemDetails
			if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if problem.Type == "urn:linguaflow:current-password-mismatch" {
				t.Fatal("request validation must not be classified as a password mismatch")
			}
			accountAssertUnchanged(t, client, account)
		})
	}
	w := accountRequest(router, http.MethodPut, "/users/me/password", token, `{"current_password":"wrong-password","new_password":"new-password"}`)
	accountAssertProblem(t, w, http.StatusBadRequest, "urn:linguaflow:current-password-mismatch")
	accountAssertUnchanged(t, client, account)
	accountAssertStatus(t, accountRequest(router, http.MethodGet, "/users/me", token, ""), http.StatusOK)
}

func TestRouter_AccountNewPasswordBoundaries(t *testing.T) {
	cases := []struct {
		name, password string
		valid          bool
	}{
		{"seven ASCII", "1234567", false}, {"eight ASCII", "12345678", true},
		{"72 bytes", strings.Repeat("a", 72), true}, {"73 bytes", strings.Repeat("a", 73), false},
		{"seven Chinese", strings.Repeat("中", 7), false}, {"eight Chinese", strings.Repeat("中", 8), true},
		{"seven emoji", strings.Repeat("😀", 7), false}, {"eight emoji", strings.Repeat("😀", 8), true},
		{"72 Chinese bytes", strings.Repeat("中", 24), true}, {"73 Chinese bytes", strings.Repeat("中", 24) + "a", false},
		{"72 emoji bytes", strings.Repeat("😀", 18), true}, {"73 emoji bytes", strings.Repeat("😀", 18) + "a", false},
		{"surrounding spaces", " 123456 ", true}, {"only spaces", strings.Repeat(" ", 8), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, client, account := accountTestServer(t)
			router := s.newRouter()
			token := authTestToken(t, account, time.Now().Add(time.Hour), []byte(authTestSecret))
			register := accountRequest(router, http.MethodPost, "/auth/register", "", accountJSON(t, map[string]string{
				"username": "registered", "email": "registered@example.com", "password": tc.password,
			}))
			change := accountRequest(router, http.MethodPut, "/users/me/password", token, accountJSON(t, map[string]string{
				"current_password": accountTestPassword, "new_password": tc.password,
			}))
			if !tc.valid {
				accountAssertProblem(t, register, http.StatusBadRequest, "urn:linguaflow:invalid-input")
				accountAssertProblem(t, change, http.StatusBadRequest, "urn:linguaflow:invalid-input")
				accountAssertUnchanged(t, client, account)
				return
			}
			accountAssertStatus(t, register, http.StatusCreated)
			accountAssertStatus(t, change, http.StatusNoContent)
			accountLogin(t, router, "registered", tc.password)
			accountLogin(t, router, account.Username, tc.password)
			if strings.TrimSpace(tc.password) != tc.password {
				w := accountRequest(router, http.MethodPost, "/auth/login", "", accountJSON(t, map[string]string{
					"username": account.Username, "password": strings.TrimSpace(tc.password),
				}))
				accountAssertProblem(t, w, http.StatusUnauthorized, "urn:linguaflow:invalid-credentials")
			}
		})
	}
}

func TestRouter_AccountLegacyPasswordChangePreservesSessions(t *testing.T) {
	s, client, account := accountTestServer(t)
	legacyPassword := "旧密码"
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(legacyPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.User.UpdateOne(account).SetPasswordHash(string(passwordHash)).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	router := s.newRouter()
	session := accountLogin(t, router, account.Username, legacyPassword)
	const newPassword = " different-password "
	w := accountRequest(router, http.MethodPut, "/users/me/password", session.AccessToken, accountJSON(t, map[string]string{
		"current_password": legacyPassword, "new_password": newPassword,
	}))
	accountAssertStatus(t, w, http.StatusNoContent)
	if w.Body.Len() != 0 {
		t.Fatalf("204 response has a body: %s", w.Body.String())
	}
	accountAssertStatus(t, accountRequest(router, http.MethodGet, "/users/me", session.AccessToken, ""), http.StatusOK)
	oldLogin := accountRequest(router, http.MethodPost, "/auth/login", "", accountJSON(t, map[string]string{"username": account.Username, "password": legacyPassword}))
	accountAssertProblem(t, oldLogin, http.StatusUnauthorized, "urn:linguaflow:invalid-credentials")
	accountLogin(t, router, account.Username, newPassword)
	refresh := accountRequest(router, http.MethodPost, "/auth/refresh", "", accountJSON(t, map[string]string{"refresh_token": session.RefreshToken}))
	accountAssertStatus(t, refresh, http.StatusOK)
}

func TestRouter_AccountLogoutOwnershipAndIdempotency(t *testing.T) {
	s, client, account := accountTestServer(t)
	ctx := context.Background()
	other, err := client.User.Create().SetUsername("other").SetEmail("other@example.com").SetPasswordHash(account.PasswordHash).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	createRefresh := func(raw string, ownerID int, revoked bool) *ent.RefreshToken {
		t.Helper()
		create := client.RefreshToken.Create().SetTokenHash(hash.Full(raw)).SetUserID(ownerID).SetExpiresAt(time.Now().Add(time.Hour))
		if revoked {
			create.SetRevokedAt(time.Now().Add(-time.Minute))
		}
		record, err := create.Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	owned := createRefresh("owned-token", account.ID, false)
	otherSession := createRefresh("other-session-token", account.ID, false)
	foreign := createRefresh("foreign-token", other.ID, false)
	foreignRevoked := createRefresh("foreign-revoked-token", other.ID, true)
	token := authTestToken(t, account, time.Now().Add(time.Hour), []byte(authTestSecret))
	router := s.newRouter()
	for _, raw := range []string{"foreign-token", "foreign-revoked-token"} {
		w := accountRequest(router, http.MethodPost, "/auth/logout", token, accountJSON(t, map[string]string{"refresh_token": raw}))
		accountAssertProblem(t, w, http.StatusForbidden, "urn:linguaflow:forbidden")
	}
	foreignAfter, err := client.RefreshToken.Get(ctx, foreign.ID)
	if err != nil || foreignAfter.RevokedAt != nil {
		t.Fatalf("foreign token was changed: record=%+v, error=%v", foreignAfter, err)
	}
	foreignRevokedAfter, err := client.RefreshToken.Get(ctx, foreignRevoked.ID)
	if err != nil || foreignRevokedAfter.RevokedAt == nil || !foreignRevokedAfter.RevokedAt.Equal(*foreignRevoked.RevokedAt) {
		t.Fatalf("foreign revoked token was changed: record=%+v, error=%v", foreignRevokedAfter, err)
	}
	w := accountRequest(router, http.MethodPost, "/auth/logout", token, `{"refresh_token":"unknown-token"}`)
	accountAssertProblem(t, w, http.StatusUnauthorized, "urn:linguaflow:token-invalid")
	for i := 0; i < 2; i++ {
		w = accountRequest(router, http.MethodPost, "/auth/logout", token, `{"refresh_token":"owned-token"}`)
		accountAssertStatus(t, w, http.StatusNoContent)
		if w.Body.Len() != 0 {
			t.Fatalf("logout 204 response has a body: %s", w.Body.String())
		}
	}
	ownedAfter, err := client.RefreshToken.Get(ctx, owned.ID)
	if err != nil || ownedAfter.RevokedAt == nil {
		t.Fatalf("own token was not revoked: record=%+v, error=%v", ownedAfter, err)
	}
	otherSessionAfter, err := client.RefreshToken.Get(ctx, otherSession.ID)
	if err != nil || otherSessionAfter.RevokedAt != nil {
		t.Fatalf("other session token was changed: record=%+v, error=%v", otherSessionAfter, err)
	}
	accountAssertStatus(t, accountRequest(router, http.MethodGet, "/users/me", token, ""), http.StatusOK)
	accountAssertStatus(t, accountRequest(router, http.MethodPost, "/auth/refresh", "", `{"refresh_token":"other-session-token"}`), http.StatusOK)
	accountAssertProblem(t, accountRequest(router, http.MethodPost, "/auth/refresh", "", `{"refresh_token":"owned-token"}`), http.StatusUnauthorized, "urn:linguaflow:refresh-token-revoked")
}

func TestRouter_AccountAuthenticationAndLocalMode(t *testing.T) {
	s, client, account := accountTestServer(t)
	refresh, err := client.RefreshToken.Create().SetTokenHash(hash.Full("test-token")).SetUserID(account.ID).SetExpiresAt(time.Now().Add(time.Hour)).Save(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	router := s.newRouter()
	paths := []struct{ method, path, body string }{
		{http.MethodGet, "/users/me", ""},
		{http.MethodPut, "/users/me", `{"display_name":"Denied"}`},
		{http.MethodPut, "/users/me/password", `{"current_password":"original-password","new_password":"new-password"}`},
		{http.MethodPost, "/auth/logout", `{"refresh_token":"test-token"}`},
	}
	expired := authTestToken(t, account, time.Now().Add(-time.Hour), []byte(authTestSecret))
	for _, path := range paths {
		accountAssertProblem(t, accountRequest(router, path.method, path.path, "", path.body), http.StatusUnauthorized, "urn:linguaflow:token-missing")
		accountAssertProblem(t, accountRequest(router, path.method, path.path, expired, path.body), http.StatusUnauthorized, "urn:linguaflow:token-expired")
	}
	accountAssertUnchanged(t, client, account)
	refreshAfter, err := client.RefreshToken.Get(context.Background(), refresh.ID)
	if err != nil || refreshAfter.RevokedAt != nil {
		t.Fatalf("unauthenticated request changed the refresh token: record=%+v, error=%v", refreshAfter, err)
	}
	s.mode, s.localUser = config.ModeLocal, account
	localRouter := s.newRouter()
	get := accountRequest(localRouter, http.MethodGet, "/users/me", "", "")
	accountAssertStatus(t, get, http.StatusOK)
	var localUser userResponse
	if err := json.Unmarshal(get.Body.Bytes(), &localUser); err != nil {
		t.Fatal(err)
	}
	if localUser.ID != account.ID {
		t.Fatalf("local identity = %d, want %d", localUser.ID, account.ID)
	}
	accountAssertStatus(t, accountRequest(localRouter, http.MethodPut, "/users/me", "", `{"display_name":"Local Name"}`), http.StatusOK)
	stored, err := client.User.Get(context.Background(), account.ID)
	if err != nil || stored.DisplayName != "Local Name" {
		t.Fatalf("local profile update failed: account=%+v, error=%v", stored, err)
	}
	accountAssertStatus(t, accountRequest(localRouter, http.MethodPut, "/users/me/password", "", paths[2].body), http.StatusNoContent)
}
