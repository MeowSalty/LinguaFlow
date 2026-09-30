package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/go-chi/chi/v5"
)

func credentialAPITestServer(t *testing.T) (*Server, *ent.Client, *ent.User) {
	t.Helper()
	s, client, u := newTestServer(t)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	ring, err := credential.ParseKeyring([]byte(`{"version":1,"active_key_id":"test","keys":{"test":"` + key + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	users := service.NewUserService(client, nil)
	s.userService = users
	s.backendSvc = service.NewBackendService(client, users, nil)
	s.backendSvc.SetCredentials(service.NewCredentialService(client, ring, users))
	return s, client, u
}
func credentialAPIRequest(t *testing.T, u *ent.User, method, path, body string, params map[string]int) *http.Request {
	t.Helper()
	r := withAuthUser(httptest.NewRequest(method, path, strings.NewReader(body)), u)
	r.Header.Set("Content-Type", "application/json")
	if len(params) > 0 {
		rc := chi.NewRouteContext()
		for k, v := range params {
			rc.URLParams.Add(k, strconv.Itoa(v))
		}
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
	}
	return r
}
func TestBackendAPIStoresSecretSeparatelyAndNeverReturnsIt(t *testing.T) {
	s, client, u := credentialAPITestServer(t)
	w := httptest.NewRecorder()
	s.handleCreateUserBackend(w, credentialAPIRequest(t, u, "POST", "/backends", `{"name":"one","type":"openai","options":{"type":"openai","model":"test"},"secret":"highly-sensitive-test-secret"}`, nil))
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "highly-sensitive") || strings.Contains(w.Body.String(), "api_key") {
		t.Fatal("backend response leaked secret")
	}
	var body backendResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Credential.Valid() || !body.HasSecret {
		t.Fatal("no credential metadata")
	}
	stored := client.Backend.GetX(context.Background(), body.ID)
	if _, ok := stored.Options["api_key"]; ok {
		t.Fatal("secret stored in backend")
	}
	w = httptest.NewRecorder()
	s.handleListUserBackends(w, credentialAPIRequest(t, u, "GET", "/backends", "", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "highly-sensitive") {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleUpdateUserBackend(w, credentialAPIRequest(t, u, "PUT", "/backends/1", `{"name":"renamed","type":"openai","options":{"type":"openai","model":"test"}}`, map[string]int{"backendId": body.ID}))
	if w.Code != 200 {
		t.Fatalf("update without secret: %d %s", w.Code, w.Body.String())
	}
	if client.Credential.Query().CountX(context.Background()) != 1 {
		t.Fatal("ordinary edit replaced credential")
	}
}
func TestBackendAPIRejectsLegacySecretAndConflictingInputs(t *testing.T) {
	s, _, u := credentialAPITestServer(t)
	for _, body := range []string{
		`{"name":"one","type":"openai","options":{"type":"openai","model":"test","api_key":"secret"}}`,
		`{"name":"one","type":"openai","options":{"type":"openai","model":"test"},"secret":"secret","credential_id":1}`,
		`{"name":"one","type":"openai","options":{"type":"openai","model":"test"},"secret":""}`,
	} {
		w := httptest.NewRecorder()
		s.handleCreateUserBackend(w, credentialAPIRequest(t, u, "POST", "/backends", body, nil))
		if w.Code != 400 {
			t.Fatalf("bad request accepted: %d %s", w.Code, w.Body.String())
		}
	}
}
func TestCredentialAPIEnforcesOwnerAndVersionLifecycle(t *testing.T) {
	s, client, u := credentialAPITestServer(t)
	w := httptest.NewRecorder()
	s.handleCreateCredential(w, credentialAPIRequest(t, u, "POST", "/credentials", `{"provider":"google","secret":"first-secret"}`, nil))
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var record service.CredentialRecord
	if err := json.Unmarshal(w.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	params := map[string]int{"credentialId": record.ID}
	other := client.User.Create().SetUsername("other").SetEmail("other@example.test").SetPasswordHash("unused").SaveX(context.Background())
	w = httptest.NewRecorder()
	s.handleRotateCredential(w, credentialAPIRequest(t, other, "POST", "/credentials/1/versions", `{"secret":"stolen"}`, params))
	if w.Code != 403 {
		t.Fatalf("cross-user mutation: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.handleRotateCredential(w, credentialAPIRequest(t, u, "POST", "/credentials/1/versions", `{"secret":"second-secret"}`, params))
	if w.Code != 201 {
		t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleCredentialVersions(w, credentialAPIRequest(t, u, "GET", "/credentials/1/versions", "", params))
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret") || !strings.Contains(w.Body.String(), `"version":2`) {
		t.Fatalf("versions: %d %s", w.Code, w.Body.String())
	}
	params["version"] = 1
	w = httptest.NewRecorder()
	s.handleRevokeCredential(w, credentialAPIRequest(t, u, "POST", "/credentials/1/versions/1/revoke", "", params))
	if w.Code != 204 {
		t.Fatalf("revoke: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.handleCollectCredentials(w, credentialAPIRequest(t, u, "POST", "/credentials/1/collect", "", params))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"deleted_versions":1`) {
		t.Fatalf("collect: %d %s", w.Code, w.Body.String())
	}
}
