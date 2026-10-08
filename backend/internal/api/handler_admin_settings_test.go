package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestAdminSettingsBooleanContract(t *testing.T) {
	s, c, u := authTestServer(t)
	ctx := context.Background()
	u = c.User.UpdateOneID(u.ID).SetRole(service.SystemRoleAdmin).SaveX(ctx)
	c.SystemSetting.Create().SetKey(service.SettingRegistrationEnabled).SetValue("true").SaveX(ctx)
	c.SystemSetting.Create().SetKey(service.SettingTaskRetention).SetValue(`{"enabled":false,"retention_days":30,"revision":1}`).SaveX(ctx)
	s.settingsService = service.NewSettingsService(c)
	for _, body := range []string{`{}`, `null`, `{"settings":null}`, `{"settings":{}}`, `{"settings":{"registration_enabled":null}}`, `{"settings":{"registration_enabled":"false"}}`, `{"settings":{"registration_enabled":false,"auto_admin":true}}`, `{"settings":{"registration_enabled":false},"extra":1}`, `{"settings":{"registration_enabled":false}} {}`} {
		w := httptest.NewRecorder()
		r := withAuthUser(httptest.NewRequest(http.MethodPatch, "/admin/settings", strings.NewReader(body)), u)
		s.handleAdminUpdateSettings(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body=%s status=%d", body, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r := withAuthUser(httptest.NewRequest(http.MethodPatch, "/admin/settings", strings.NewReader(`{"settings":{"registration_enabled":false}}`)), u)
	s.handleAdminUpdateSettings(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"registration_enabled":false`) {
		t.Fatalf("response=%d %s", w.Code, w.Body.String())
	}
	if c.ActivityLog.Query().CountX(ctx) != 1 {
		t.Fatal("settings audit missing or invalid request committed")
	}
}

func TestRegisterSettingsFailureReturnsUnavailable(t *testing.T) {
	s, _, _ := authTestServer(t)
	w := httptest.NewRecorder()
	s.handleRegister(w, httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(`{"username":"new","password":"password-123","email":"new@test.invalid"}`)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d response=%s", w.Code, w.Body.String())
	}
}

func TestInitializationReadinessTracksPolicyAndMode(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	db, client, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db, entClient: client, serverCfg: cfg, mode: config.ModeServer}
	s.ready.Store(true)
	request := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleReady(w, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
		if w.Code != want {
			t.Fatalf("readiness=%d want=%d body=%s", w.Code, want, w.Body.String())
		}
	}
	request(http.StatusServiceUnavailable)
	if _, err := service.NewInitializationService(client).Initialize(ctx, config.ModeServer, config.BootstrapInput{Admin: &config.BootstrapAdmin{Username: "ready-admin", Email: "ready@test.invalid", Password: "ready-password"}}); err != nil {
		t.Fatal(err)
	}
	request(http.StatusOK)
	s.mode = config.ModeLocal
	request(http.StatusServiceUnavailable)
	s.mode = config.ModeServer
	client.SystemSetting.Delete().ExecX(ctx)
	request(http.StatusServiceUnavailable)
	client.SystemSetting.Create().SetKey(service.SettingRegistrationEnabled).SetValue("false").SaveX(ctx)
	request(http.StatusOK)
	client.InstanceInitialization.Delete().ExecX(ctx)
	request(http.StatusServiceUnavailable)
	if client.User.Query().CountX(ctx) != 1 || client.InstanceInitialization.Query().CountX(ctx) != 0 {
		t.Fatal("readiness repaired initialization state")
	}
}
