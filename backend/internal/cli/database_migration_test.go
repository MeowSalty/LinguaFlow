package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestPrepareDatabaseAutomaticDataMigration(t *testing.T) {
	for _, mode := range []string{"serve", "local"} {
		t.Run(mode, func(t *testing.T) {
			clearDeploymentEnvironment(t)
			dir := t.TempDir()
			keyring := filepath.Join(dir, "keys.json")
			if mode == "serve" {
				if _, err := credential.PrepareKeyring(keyring, true); err != nil {
					t.Fatal(err)
				}
				t.Setenv("LINGUAFLOW_JWT_SECRET", strings.Repeat("j", 32))
				t.Setenv("LINGUAFLOW_CREDENTIALS_KEYRING_FILE", keyring)
			}
			t.Setenv("LINGUAFLOW_DATA_DIR", dir)
			resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: mode, Environment: config.Environment()})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			_, client, cleanup, err := prepareDatabase(ctx, &resolved.Config)
			if err != nil {
				t.Fatal(err)
			}
			input := config.BootstrapInput{}
			if mode == "serve" {
				input.Admin = &config.BootstrapAdmin{Username: "admin", Email: "admin@test.invalid", Password: "password-123"}
			}
			if _, err := service.NewInitializationService(client).Initialize(ctx, mode, input); err != nil {
				t.Fatal(err)
			}
			client.InstanceInitialization.UpdateOneID(1).SetDataVersion(0).ExecX(ctx)
			client.SystemSetting.Delete().Where(systemsetting.KeyEQ(service.SettingTaskRetention)).ExecX(ctx)
			beforeUser := client.User.Query().OnlyX(ctx)
			if err := cleanup(); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LINGUAFLOW_AUTO_MIGRATE", "false")
			resolved.Config.AutoMigrate = false
			if _, _, unexpectedCleanup, err := prepareDatabase(ctx, &resolved.Config); !errors.Is(err, service.ErrDataMigrationRequired) {
				if unexpectedCleanup != nil {
					_ = unexpectedCleanup()
				}
				t.Fatalf("stale data version accepted with auto migration disabled: %v", err)
			}
			for range 2 {
				upgradeConfig := resolved.Config
				upgradeConfig.AutoMigrate = true
				_, _, closeUpgrade, err := prepareDatabase(ctx, &upgradeConfig)
				if err != nil {
					t.Fatal(err)
				}
				if err := closeUpgrade(); err != nil {
					t.Fatal(err)
				}
			}
			_, client, cleanup, err = prepareDatabase(ctx, &resolved.Config)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if got := client.User.Query().OnlyX(ctx); got.ID != beforeUser.ID || got.PasswordHash != beforeUser.PasswordHash {
				t.Fatal("migration altered administrator")
			}
			if client.Job.Query().CountX(ctx) != 0 || client.SyncTask.Query().CountX(ctx) != 0 || client.StorageTask.Query().CountX(ctx) != 0 || client.ActivityLog.Query().CountX(ctx) != 1 {
				t.Fatal("migration executed work or created task history")
			}
			if policy, err := service.NewSettingsService(client).TaskRetention(ctx); err != nil || policy.Enabled || policy.RetentionDays != 30 || policy.Revision != 1 {
				t.Fatalf("policy %+v %v", policy, err)
			}
			if mode == "local" {
				if _, err := os.Stat(resolved.LocalSecretPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("database preparation created a local authentication secret")
				}
				if _, err := os.Stat(resolved.Config.Credentials.KeyringFile); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("database preparation created credential keys")
				}
			}
		})
	}
}

func TestPrepareDatabaseDoesNotInitialize(t *testing.T) {
	clearDeploymentEnvironment(t)
	dir := t.TempDir()
	t.Setenv("LINGUAFLOW_DATA_DIR", dir)
	resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: config.ModeLocal, Environment: config.Environment()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, client, cleanup, err := prepareDatabase(ctx, &resolved.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if empty, err := service.NewInitializationService(client).IsEmpty(ctx); err != nil || !empty {
		t.Fatalf("database preparation created identity or settings: %v %v", empty, err)
	}
}
