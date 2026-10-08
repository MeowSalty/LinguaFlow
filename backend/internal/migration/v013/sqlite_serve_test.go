package v013

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func sqliteServeFixture(t *testing.T) string {
	t.Helper()
	dir := localFixture(t, true)
	db := openLocalFixture(t, dir)
	if _, err := db.Exec(`UPDATE users SET username='server-admin',email='server@example.test' WHERE id=5`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func sqliteServeOptions(t *testing.T, dir string) SQLiteOptions {
	t.Helper()
	keys, pending, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	return SQLiteOptions{
		DataDir: dir, Mode: config.ModeServer, Keys: keys,
		KeyringFile: filepath.Join(t.TempDir(), "external-keyring.json"), PendingKeyring: pending,
		JWTSecret: strings.Repeat("retained-server-jwt", 3),
	}
}

func TestSQLiteServeExternalKeyringAndPublishedRecovery(t *testing.T) {
	dir := sqliteServeFixture(t)
	options := sqliteServeOptions(t, dir)
	before := readLocalBytes(t, filepath.Join(dir, localDatabaseName))
	rehearsal, err := RunSQLite(context.Background(), options)
	if err != nil || rehearsal.Applied || rehearsal.CredentialsCreated != 2 {
		t.Fatalf("serve rehearsal: %+v %v", rehearsal, err)
	}
	if !bytes.Equal(before, readLocalBytes(t, filepath.Join(dir, localDatabaseName))) {
		t.Fatal("rehearsal changed original database")
	}
	if _, err := os.Stat(options.KeyringFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rehearsal published an external keyring")
	}
	options.Apply = true
	result, err := RunSQLite(context.Background(), options)
	if err != nil || !result.Applied || result.BackupDir == "" {
		t.Fatalf("serve apply: %+v %v", result, err)
	}
	if !bytes.Equal(before, readLocalBytes(t, filepath.Join(result.BackupDir, localDatabaseName))) {
		t.Fatal("backup did not retain the original database")
	}
	if !bytes.Equal(options.PendingKeyring, readLocalBytes(t, options.KeyringFile)) {
		t.Fatal("external keyring differs from the key used for conversion")
	}
	keys, err := credential.LoadKeyring(options.KeyringFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultServerConfig()
	cfg.DataDir, cfg.AutoMigrate = dir, false
	_, client, err := database.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	marker := client.InstanceInitialization.GetX(ctx, 1)
	if marker.Mode != "serve" || marker.LocalUserID != nil {
		t.Fatal("serve conversion wrote a local initialization marker")
	}
	if err := service.NewCredentialService(client, keys, nil).ValidateKeys(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.GetSnapshot(client.Job.GetX(ctx, 1))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := service.NewCredentialService(client, keys, nil).Resolve(ctx, snapshot.Rounds[0].Backend.Credential, "openai", "https://api.openai.com/v1")
	if err != nil || secret != "job-original-key" {
		t.Fatalf("historical credential was lost: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"credentials-keyring.json", "jwt-secret", "instance-secret"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("explicit external secrets generated %s", name)
		}
	}
	keyringBeforeRecovery := readLocalBytes(t, options.KeyringFile)
	options.Keys, options.PendingKeyring = keys, nil
	recovered, err := RunSQLite(ctx, options)
	if err != nil || !recovered.Applied || recovered.Recovery != "published" || recovered.BackupDir != result.BackupDir {
		t.Fatalf("published recovery: %+v %v", recovered, err)
	}
	if !bytes.Equal(keyringBeforeRecovery, readLocalBytes(t, options.KeyringFile)) {
		t.Fatal("recovery changed credential keyring")
	}
}

func TestSQLiteServeRecoveryRejectsChangedModeOrSecrets(t *testing.T) {
	dir := sqliteServeFixture(t)
	options := sqliteServeOptions(t, dir)
	options.Apply = true
	if _, err := RunSQLite(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	options.PendingKeyring = nil
	published := readLocalBytes(t, filepath.Join(dir, localDatabaseName))
	keyBytes := readLocalBytes(t, options.KeyringFile)
	for _, kind := range []string{"mode", "key path", "credential key", "jwt"} {
		t.Run(kind, func(t *testing.T) {
			changed := options
			switch kind {
			case "mode":
				changed = SQLiteOptions{DataDir: dir, Mode: config.ModeLocal, Apply: true}
			case "key path":
				changed.KeyringFile = filepath.Join(t.TempDir(), "other-keyring.json")
				changed.PendingKeyring = keyBytes
			case "credential key":
				var err error
				changed.Keys, _, err = credential.GenerateKeyring()
				if err != nil {
					t.Fatal(err)
				}
			case "jwt":
				changed.JWTSecret = strings.Repeat("different-jwt", 4)
			}
			if report, err := RunSQLite(context.Background(), changed); err == nil || report.Applied {
				t.Fatalf("recovery accepted changed %s: %+v %v", kind, report, err)
			}
			if !bytes.Equal(published, readLocalBytes(t, filepath.Join(dir, localDatabaseName))) || !bytes.Equal(keyBytes, readLocalBytes(t, options.KeyringFile)) {
				t.Fatal("refused recovery changed published data or keys")
			}
		})
	}
}

func TestSQLiteServeRejectsInvalidKeysWithoutPublishing(t *testing.T) {
	for _, kind := range []string{"pending mismatch", "corrupt existing", "existing mismatch", "no mode", "no administrator"} {
		t.Run(kind, func(t *testing.T) {
			dir := sqliteServeFixture(t)
			options := sqliteServeOptions(t, dir)
			options.Apply = true
			var existing []byte
			switch kind {
			case "pending mismatch":
				_, pending, err := credential.GenerateKeyring()
				if err != nil {
					t.Fatal(err)
				}
				options.PendingKeyring = pending
			case "corrupt existing":
				existing = []byte("invalid-keyring-content")
			case "existing mismatch":
				_, raw, err := credential.GenerateKeyring()
				if err != nil {
					t.Fatal(err)
				}
				existing = raw
			case "no mode":
				options.Mode = ""
			case "no administrator":
				db := openLocalFixture(t, dir)
				if _, err := db.Exec(`UPDATE users SET active=false`); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if existing != nil {
				if _, err := credential.PublishPrivateFile(options.KeyringFile, existing); err != nil {
					t.Fatal(err)
				}
				options.PendingKeyring = nil
			}
			before := readLocalBytes(t, filepath.Join(dir, localDatabaseName))
			if report, err := RunSQLite(context.Background(), options); err == nil || report.Applied {
				t.Fatalf("accepted %s: %+v %v", kind, report, err)
			}
			if !bytes.Equal(before, readLocalBytes(t, filepath.Join(dir, localDatabaseName))) {
				t.Fatal("invalid inputs changed original database")
			}
			if existing != nil {
				if !bytes.Equal(existing, readLocalBytes(t, options.KeyringFile)) {
					t.Fatal("invalid inputs overwrote an existing keyring")
				}
			} else if _, err := os.Stat(options.KeyringFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed migration published an external keyring")
			}
			backups, _ := filepath.Glob(dir + ".v013-backup-*")
			if len(backups) != 0 {
				t.Fatal("failed migration switched the original directory")
			}
		})
	}
}

func TestSQLiteExplicitLocalModeKeepsLocalIdentityRequirement(t *testing.T) {
	dir := sqliteServeFixture(t)
	before := readLocalBytes(t, filepath.Join(dir, localDatabaseName))
	if _, err := RunSQLite(context.Background(), SQLiteOptions{DataDir: dir, Mode: config.ModeLocal, Apply: true}); err == nil || !strings.Contains(err.Error(), "named local") {
		t.Fatalf("explicit local mode accepted a serve-only identity: %v", err)
	}
	if !bytes.Equal(before, readLocalBytes(t, filepath.Join(dir, localDatabaseName))) {
		t.Fatal("failed local migration changed original database")
	}
	localDir := localFixture(t, false)
	if result, err := RunSQLite(context.Background(), SQLiteOptions{DataDir: localDir, Mode: config.ModeLocal, Apply: true}); err != nil || !result.Applied {
		t.Fatalf("explicit local mode broke local migration: %+v %v", result, err)
	}
}
