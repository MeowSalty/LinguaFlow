package v013

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/instanceinitialization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func migrateLegacyTestTransaction(ctx context.Context, client *ent.Client, keys *credential.Keyring) (Report, error) {
	tx, err := client.Tx(ctx)
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()
	report, err := Convert(ctx, tx.Client(), keys, config.ModeServer)
	if err != nil {
		return report, err
	}
	return report, tx.Commit()
}

func legacyMigrationAdmin(t *testing.T, ctx context.Context, client *ent.Client) *ent.User {
	t.Helper()
	return client.User.Create().SetUsername("old-admin").SetEmail("old-admin@example.test").
		SetPasswordHash("existing-password-hash").SetRole(service.SystemRoleAdmin).SetActive(true).SaveX(ctx)
}

func TestLegacyMigrationEncryptsBackendsAndPreservesAuthority(t *testing.T) {
	ctx := context.Background()
	client := testClient(t)
	admin := legacyMigrationAdmin(t, ctx, client)
	ordinary := client.User.Create().SetUsername("ordinary").SetEmail("ordinary@example.test").
		SetPasswordHash("unchanged-user-hash").SetActive(false).SaveX(ctx)
	org := client.Organization.Create().SetName("legacy-org").SetSlug("legacy-org").SaveX(ctx)
	oldTime := time.Date(2025, 1, 2, 3, 4, 5, 6000, time.UTC)
	options := map[string]any{"api_key": "legacy-secret", "model": "legacy-model", "base_url": "https://example.test/v1", "temperature": 0.5}
	back := client.Backend.Create().SetName("legacy-user").SetScope(service.ScopeUser).SetOwnerUserID(admin.ID).
		SetBackendType("openai").SetOptions(options).SetCreatedAt(oldTime).SetUpdatedAt(oldTime).SaveX(ctx)
	orgBack := client.Backend.Create().SetName("legacy-org").SetScope(service.ScopeOrg).SetOwnerOrgID(org.ID).
		SetBackendType("anthropic").SetOptions(map[string]any{"api_key": "org-secret", "model": "old-model"}).SaveX(ctx)
	empty := client.Backend.Create().SetName("legacy-empty").SetScope(service.ScopeUser).SetOwnerUserID(admin.ID).
		SetBackendType("google").SetOptions(map[string]any{"api_key": "", "model": "empty-model"}).SaveX(ctx)
	client.SystemSetting.Create().SetKey(service.SettingRegistrationEnabled).SetValue("false").SetCreatedAt(oldTime).SetUpdatedAt(oldTime).SaveX(ctx)
	client.SystemSetting.Create().SetKey("default_user_role").SetValue("user").SaveX(ctx)
	client.SystemSetting.Create().SetKey("auto_admin").SetValue("true").SaveX(ctx)
	keys := credentialTestKeyring(t, "migration", "migration")
	report, err := migrateLegacyTestTransaction(ctx, client, keys)
	if err != nil {
		t.Fatal(err)
	}
	if report.CredentialsCreated != 2 || report.BackendsMigrated != 3 || report.BackendsWithoutSecret != 1 || report.SettingsRemoved != 2 || report.RegistrationEnabled {
		t.Fatalf("unexpected migration report: %+v", report)
	}
	got := client.Backend.GetX(ctx, back.ID)
	delete(options, "api_key")
	if !reflect.DeepEqual(got.Options, options) || !got.UpdatedAt.Equal(oldTime) || !got.CreatedAt.Equal(oldTime) {
		t.Fatalf("backend public options or timestamps changed: %+v", got)
	}
	if got.CredentialID == nil || client.Backend.GetX(ctx, orgBack.ID).CredentialID == nil || client.Backend.GetX(ctx, empty.ID).CredentialID != nil {
		t.Fatal("incorrect migrated credential bindings")
	}
	credentials := service.NewCredentialService(client, keys, nil)
	for _, tc := range []struct {
		id       int
		provider string
		endpoint string
		secret   string
	}{{back.ID, "openai", "https://example.test/v1", "legacy-secret"}, {orgBack.ID, "anthropic", "", "org-secret"}} {
		secret, err := credentials.Resolve(ctx, backendBinding(ctx, client, tc.id), tc.provider, tc.endpoint)
		if err != nil || secret != tc.secret {
			t.Fatalf("credential did not resolve for backend %d: %v", tc.id, err)
		}
	}
	for _, row := range client.CredentialVersion.Query().AllX(ctx) {
		if bytes.Contains(row.Ciphertext, []byte("secret")) {
			t.Fatal("plaintext credential persisted")
		}
	}
	for _, before := range []*ent.User{admin, ordinary} {
		after := client.User.GetX(ctx, before.ID)
		if before.PasswordHash != after.PasswordHash || before.Role != after.Role || before.Active != after.Active || !before.UpdatedAt.Equal(after.UpdatedAt) {
			t.Fatal("migration altered existing user authority")
		}
	}
	setting := client.SystemSetting.Query().OnlyX(ctx)
	if setting.Key != service.SettingRegistrationEnabled || setting.Value != "false" || !setting.UpdatedAt.Equal(oldTime) {
		t.Fatal("registration policy or timestamp changed")
	}
	if _, err := service.NewInitializationService(client).Validate(ctx, config.ModeServer); err != nil {
		t.Fatalf("migrated instance cannot start: %v", err)
	}
	reportJSON, err := json.Marshal(report)
	if err != nil || bytes.Contains(reportJSON, []byte("legacy-secret")) || bytes.Contains(reportJSON, []byte("org-secret")) {
		t.Fatal("migration report contains a secret")
	}
	if _, err := migrateLegacyTestTransaction(ctx, client, keys); err == nil || !strings.Contains(err.Error(), "already migrated") {
		t.Fatalf("repeat migration should be rejected: %v", err)
	}
	if client.Credential.Query().CountX(ctx) != 2 || client.ActivityLog.Query().CountX(ctx) != 1 {
		t.Fatal("repeat migration modified data")
	}
}

func TestLegacyMigrationPreservesRegistrationBehavior(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   *string
		enabled bool
	}{
		{name: "missing", enabled: true},
		{name: "empty", value: legacyMigrationString(""), enabled: true},
		{name: "enabled", value: legacyMigrationString("true"), enabled: true},
		{name: "disabled", value: legacyMigrationString("false")},
		{name: "noncanonical", value: legacyMigrationString("TRUE")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := testClient(t)
			legacyMigrationAdmin(t, ctx, client)
			if tc.value != nil {
				client.SystemSetting.Create().SetKey(service.SettingRegistrationEnabled).SetValue(*tc.value).SaveX(ctx)
			}
			if _, err := migrateLegacyTestTransaction(ctx, client, credentialTestKeyring(t, "key", "key")); err != nil {
				t.Fatal(err)
			}
			settings, err := service.NewSettingsService(client).Get(ctx)
			if err != nil || settings.RegistrationEnabled != tc.enabled {
				t.Fatalf("registration policy changed: %+v %v", settings, err)
			}
		})
	}
}

func legacyMigrationString(value string) *string { return &value }

func TestLegacyMigrationRejectsUnknownAndIncompleteState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(context.Context, *ent.Client, *ent.User)
	}{
		{"no active admin", func(ctx context.Context, c *ent.Client, u *ent.User) {
			c.User.UpdateOneID(u.ID).SetActive(false).ExecX(ctx)
		}},
		{"no admin role", func(ctx context.Context, c *ent.Client, u *ent.User) {
			c.User.UpdateOneID(u.ID).SetRole(service.SystemRoleUser).ExecX(ctx)
		}},
		{"unknown setting", func(ctx context.Context, c *ent.Client, _ *ent.User) {
			c.SystemSetting.Create().SetKey("unknown").SetValue("private-setting-value").SaveX(ctx)
		}},
		{"existing marker", func(ctx context.Context, c *ent.Client, _ *ent.User) {
			c.InstanceInitialization.Create().SetVersion(service.InitializationVersion).SetMode(instanceinitialization.ModeServe).SaveX(ctx)
		}},
		{"existing credential", func(ctx context.Context, c *ent.Client, u *ent.User) {
			c.Credential.Create().SetScope(service.ScopeUser).SetOwnerID(u.ID).SetProvider("openai").SetEndpoint("https://api.openai.com/v1/").SetCurrentVersion(1).SaveX(ctx)
		}},
		{"invalid backend secret", func(ctx context.Context, c *ent.Client, u *ent.User) {
			c.Backend.Create().SetName("invalid").SetScope(service.ScopeUser).SetOwnerUserID(u.ID).SetBackendType("openai").SetOptions(map[string]any{"api_key": 123, "model": "test"}).SaveX(ctx)
		}},
		{"invalid endpoint", func(ctx context.Context, c *ent.Client, u *ent.User) {
			c.Backend.Create().SetName("invalid").SetScope(service.ScopeUser).SetOwnerUserID(u.ID).SetBackendType("openai").SetOptions(map[string]any{"api_key": "private-secret", "base_url": "https://example.test/?token=private-secret", "model": "test"}).SaveX(ctx)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := testClient(t)
			admin := legacyMigrationAdmin(t, ctx, client)
			client.Backend.Create().SetName("valid").SetScope(service.ScopeUser).SetOwnerUserID(admin.ID).SetBackendType("openai").SetOptions(map[string]any{"api_key": "valid-secret", "model": "test"}).SaveX(ctx)
			tc.setup(ctx, client, admin)
			count := client.Credential.Query().CountX(ctx)
			_, err := migrateLegacyTestTransaction(ctx, client, credentialTestKeyring(t, "key", "key"))
			if err == nil || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "private-setting-value") {
				t.Fatalf("expected useful redacted rejection: %v", err)
			}
			if client.Credential.Query().CountX(ctx) != count || client.CredentialVersion.Query().CountX(ctx) != 0 || client.ActivityLog.Query().CountX(ctx) != 0 {
				t.Fatal("rejected migration changed database")
			}
		})
	}
}

func TestLegacyMigrationRollsBackAfterCredentialWrite(t *testing.T) {
	ctx := context.Background()
	client := testClient(t)
	admin := legacyMigrationAdmin(t, ctx, client)
	back := client.Backend.Create().SetName("legacy").SetScope(service.ScopeUser).SetOwnerUserID(admin.ID).SetBackendType("openai").
		SetOptions(map[string]any{"api_key": "retain-on-failure", "model": "test"}).SaveX(ctx)
	failure := errors.New("injected marker failure")
	client.InstanceInitialization.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) { return nil, failure })
	})
	_, err := migrateLegacyTestTransaction(ctx, client, credentialTestKeyring(t, "key", "key"))
	if !errors.Is(err, failure) {
		t.Fatalf("expected injected failure: %v", err)
	}
	got := client.Backend.GetX(ctx, back.ID)
	if got.CredentialID != nil || got.Options["api_key"] != "retain-on-failure" || client.Credential.Query().CountX(ctx) != 0 || client.CredentialVersion.Query().CountX(ctx) != 0 || client.SystemSetting.Query().CountX(ctx) != 0 {
		t.Fatal("failed migration did not roll back")
	}
}
