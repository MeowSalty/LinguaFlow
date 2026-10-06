package migrationcli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func legacySQLiteServeFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "serve-data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "linguaflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile("../migration/v013/testdata/v013-sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("migration-test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	const timestamp = "2026-09-29 11:00:00.123456789 +0800 CST"
	if _, err := db.Exec(`INSERT INTO users(id,created_at,updated_at,username,password_hash,email,role,active) VALUES(1,$1,$1,'server-admin',$2,'server@example.test','admin',true),(7,$1,$1,'existing-user','preserved-user-hash','user@example.test','user',false)`, timestamp, string(hash)); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO projects(id,created_at,updated_at,name,config,owner_user_id) VALUES(1,$1,$1,'old-project','{}',1)`,
		`INSERT INTO backends(id,created_at,updated_at,name,scope,backend_type,options,owner_user_id) VALUES(1,$1,$1,'old-backend','user','openai','{"model":"old-model","api_key":"backend-original-key"}',1)`,
	} {
		if _, err := db.Exec(statement, timestamp); err != nil {
			t.Fatal(err)
		}
	}
	seedLegacyRunnableJob(t, db, false)
	resourceDir := filepath.Join(dir, "jobs", "resources")
	if err := os.MkdirAll(resourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resourceDir, "migration.txt"), []byte("old source\nremaining source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, string(hash)
}

func executeSQLiteMigration(t *testing.T, args []string, environment map[string]string) (string, error) {
	t.Helper()
	clearDeploymentEnvironment(t)
	for name, value := range environment {
		t.Setenv(name, value)
	}
	root := NewCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(append([]string{"v013", "sqlite"}, args...))
	err := root.Execute()
	text := output.String()
	if err != nil {
		text += err.Error()
	}
	for _, secret := range []string{"backend-original-key", "job-original-key", environment["LINGUAFLOW_CREDENTIALS_MASTER_KEY"], environment["LINGUAFLOW_JWT_SECRET"], environment["LINGUAFLOW_DATABASE_DSN"]} {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatal("SQLite migration output leaked secret input")
		}
	}
	return output.String(), err
}

func TestMigrateV013SQLiteServeRehearsalApplyAndStartup(t *testing.T) {
	dir, originalHash := legacySQLiteServeFixture(t)
	databasePath := filepath.Join(dir, "linguaflow.db")
	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--mode", "serve", "--data-dir", dir}
	if _, err := executeSQLiteMigration(t, args, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(databasePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rehearsal changed the original database", err)
	}
	for _, name := range []string{"credentials-keyring.json", "jwt-secret", "instance-secret"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rehearsal published %s: %v", name, err)
		}
	}
	output, err := executeSQLiteMigration(t, append(args, "--apply"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Migration published", "Original directory backup:", "LINGUAFLOW_DATABASE_DRIVER=sqlite", "LINGUAFLOW_CREDENTIALS_KEYRING_FILE=", "LINGUAFLOW_JWT_SECRET_FILE="} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing startup instruction %q: %s", want, output)
		}
	}
	backups, err := filepath.Glob(dir + ".v013-backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one original backup: %v %v", backups, err)
	}
	backup, err := os.ReadFile(filepath.Join(backups[0], "linguaflow.db"))
	if err != nil || !bytes.Equal(before, backup) {
		t.Fatal("backup differs from original database", err)
	}
	keys, err := credential.LoadKeyring(filepath.Join(dir, "credentials-keyring.json"))
	if err != nil {
		t.Fatal(err)
	}
	jwt, explicitJWT, err := config.EnvironmentSecret(map[string]string{"LINGUAFLOW_JWT_SECRET_FILE": filepath.Join(dir, "jwt-secret")}, "LINGUAFLOW_JWT_SECRET", dir)
	if err != nil || !explicitJWT || len(jwt) < 32 {
		t.Fatalf("generated serve JWT is not usable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "instance-secret")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("serve migration generated a local-mode secret")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	assertMigratedServiceStarts(t, dir, port, []string{
		"LINGUAFLOW_DATABASE_DRIVER=sqlite",
		"LINGUAFLOW_CREDENTIALS_KEYRING_FILE=" + filepath.Join(dir, "credentials-keyring.json"),
		"LINGUAFLOW_JWT_SECRET_FILE=" + filepath.Join(dir, "jwt-secret"),
		"LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME=server-admin",
		"LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL=replaced@example.test",
		"LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD=do-not-reset-existing-password",
	})
	cfg := config.DefaultServerConfig()
	cfg.DataDir, cfg.AutoMigrate = dir, false
	_, client, err := database.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	admin := client.User.GetX(ctx, 1)
	other := client.User.GetX(ctx, 7)
	if admin.Username != "server-admin" || admin.PasswordHash != originalHash || admin.Email != "server@example.test" || admin.Role != "admin" || !admin.Active || other.Username != "existing-user" || other.PasswordHash != "preserved-user-hash" || other.Role != "user" || other.Active || client.User.Query().CountX(ctx) != 2 {
		t.Fatal("migration or serve bootstrap changed existing users")
	}
	marker := client.InstanceInitialization.GetX(ctx, 1)
	if marker.Mode != "serve" || marker.LocalUserID != nil {
		t.Fatalf("wrong initialization mode: %v", marker)
	}
	if local, err := service.NewInitializationService(client).Validate(ctx, config.ModeServer); err != nil || local != nil {
		t.Fatalf("serve initialization is invalid: %v", err)
	}
	if err := service.NewCredentialService(client, keys, nil).ValidateKeys(ctx); err != nil {
		t.Fatal(err)
	}
	assertMigratedJobReadableAndResumable(t, client, keys)
}

func TestMigrateV013SQLiteServeSuppliedSecrets(t *testing.T) {
	for _, source := range []string{"environment", "file"} {
		t.Run(source, func(t *testing.T) {
			dir, _ := legacySQLiteServeFixture(t)
			master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{41}, 32))
			jwt := strings.Repeat("sqlite-jwt-test", 3)
			environment := map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master, "LINGUAFLOW_JWT_SECRET": jwt}
			if source == "file" {
				environment = make(map[string]string)
				for name, value := range map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master, "LINGUAFLOW_JWT_SECRET": jwt} {
					path := filepath.Join(t.TempDir(), name)
					if err := os.WriteFile(path, []byte(value+"\r\n"), 0600); err != nil {
						t.Fatal(err)
					}
					environment[name+"_FILE"] = path
				}
			}
			output, err := executeSQLiteMigration(t, []string{"--mode", "serve", "--data-dir", dir, "--apply"}, environment)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output, "same LINGUAFLOW_CREDENTIALS_MASTER_KEY") || !strings.Contains(output, "same LINGUAFLOW_JWT_SECRET") || strings.Contains(output, "LINGUAFLOW_CREDENTIALS_KEYRING_FILE=") || strings.Contains(output, master) || strings.Contains(output, jwt) {
				t.Fatalf("incorrect supplied-secret startup instructions: %s", output)
			}
			for _, name := range []string{"credentials-keyring.json", "jwt-secret", "instance-secret"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("supplied secrets generated extra %s", name)
				}
			}
			keys, err := credential.FromMasterKey(master)
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.DefaultServerConfig()
			cfg.DataDir, cfg.AutoMigrate = dir, false
			_, client, err := database.Open(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := service.NewCredentialService(client, keys, nil).ValidateKeys(context.Background()); err != nil {
				t.Fatalf("migration did not encrypt with supplied master key: %v", err)
			}
		})
	}
}

func TestMigrationSQLiteInputValidation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		environment map[string]string
		want        string
	}{
		{"missing mode", nil, nil, "mode"},
		{"empty mode", []string{"--mode="}, nil, "mode"},
		{"invalid mode", []string{"--mode=server"}, nil, "mode"},
		{"postgres driver", []string{"--mode=serve"}, map[string]string{"LINGUAFLOW_DATABASE_DRIVER": "postgres"}, "DATABASE_DRIVER"},
		{"explicit dsn", []string{"--mode=serve"}, map[string]string{"LINGUAFLOW_DATABASE_DSN": "private-invalid-dsn"}, "DATABASE_DSN"},
		{"empty dsn", []string{"--mode=serve"}, map[string]string{"LINGUAFLOW_DATABASE_DSN": ""}, "DATABASE_DSN"},
		{"dsn file", []string{"--mode=serve"}, map[string]string{"LINGUAFLOW_DATABASE_DSN_FILE": "missing-secret"}, "DATABASE_DSN_FILE"},
		{"server config", []string{"--mode=serve"}, map[string]string{"LINGUAFLOW_SERVER_CONFIG": "missing-config"}, "SERVER_CONFIG"},
		{"master conflict", []string{"--mode=serve", "--keyring-file=keyring.json"}, map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))}, "conflict"},
		{"short jwt", []string{"--mode=serve"}, map[string]string{"LINGUAFLOW_JWT_SECRET": "invalid-private-jwt"}, "JWT_SECRET"},
		{"local external key", []string{"--mode=local", "--keyring-file=keyring.json"}, nil, "keyring"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "not-created")
			args := append([]string{"--data-dir", dir}, tc.args...)
			if _, err := executeSQLiteMigration(t, args, tc.environment); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %s", err, tc.want)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid input created the data directory")
			}
		})
	}
	if _, err := executeSQLiteMigration(t, []string{"--mode=serve"}, nil); err == nil || !strings.Contains(err.Error(), "data-dir") {
		t.Fatalf("missing explicit data directory accepted: %v", err)
	}
}

func TestMigrationSQLiteHelpAndNoDotenvLoading(t *testing.T) {
	dir, _ := legacySQLiteServeFixture(t)
	workingDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workingDir, ".env"), []byte("LINGUAFLOW_DATABASE_DRIVER=postgres\nLINGUAFLOW_DATABASE_DSN=private-dotenv-input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workingDir)
	if output, err := executeSQLiteMigration(t, []string{"--help"}, map[string]string{"LINGUAFLOW_DATABASE_DSN_FILE": "missing-secret"}); err != nil || !strings.Contains(output, "--mode") {
		t.Fatalf("SQLite help unexpectedly loads secrets: %v", err)
	}
	if _, err := executeSQLiteMigration(t, []string{"--mode=serve", "--data-dir", dir}, nil); err != nil {
		t.Fatalf("SQLite migration loaded deployment .env: %v", err)
	}
}

func TestMigrateV013SQLiteServeRecoversInternalSecretFiles(t *testing.T) {
	dir, _ := legacySQLiteServeFixture(t)
	master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{56}, 32))
	jwt := strings.Repeat("sqlite-recovery-jwt", 3)
	environment := make(map[string]string)
	for name, value := range map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master, "LINGUAFLOW_JWT_SECRET": jwt} {
		path := filepath.Join(dir, name+".secret")
		if err := os.WriteFile(path, []byte(value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		environment[name+"_FILE"] = path
	}
	original, err := os.ReadFile(filepath.Join(dir, "linguaflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--mode=serve", "--data-dir", dir, "--apply"}
	if _, err := executeSQLiteMigration(t, args, environment); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".v013-switch.json")
	journalBytes, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var journal struct {
		StageDir string `json:"stage_dir"`
	}
	if err := json.Unmarshal(journalBytes, &journal); err != nil || filepath.Dir(journal.StageDir) != filepath.Dir(dir) {
		t.Fatalf("invalid fixture journal: %v", err)
	}
	// Restore the exact state between the two publication renames: the old
	// directory is in backup and the converted directory is still in staging.
	if err := os.Rename(dir, journal.StageDir); err != nil {
		t.Fatal(err)
	}
	output, err := executeSQLiteMigration(t, args, environment)
	if err != nil || !strings.Contains(output, "restored") {
		t.Fatalf("same command could not recover internal secret-file inputs: %v\n%s", err, output)
	}
	restored, err := os.ReadFile(filepath.Join(dir, "linguaflow.db"))
	if err != nil || !bytes.Equal(original, restored) {
		t.Fatal("recovery did not restore original SQLite database", err)
	}
	for name, value := range map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master, "LINGUAFLOW_JWT_SECRET": jwt} {
		raw, err := os.ReadFile(environment[name+"_FILE"])
		if err != nil || string(raw) != value+"\n" {
			t.Fatalf("recovery changed %s input: %v", name, err)
		}
	}
	if _, err := executeSQLiteMigration(t, args, environment); err != nil {
		t.Fatalf("migration could not resume after directory restoration: %v", err)
	}
}
