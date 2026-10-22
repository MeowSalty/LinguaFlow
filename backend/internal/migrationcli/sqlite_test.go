package migrationcli

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/v013"
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

func TestLegacyMigrationRejectsBeforeSQLiteInputValidation(t *testing.T) {
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
			if _, err := executeSQLiteMigration(t, args, tc.environment); err == nil || !errors.Is(err, v013.ErrTargetIncompatible) {
				t.Fatalf("error=%v, want %s", err, tc.want)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid input created the data directory")
			}
		})
	}
	if _, err := executeSQLiteMigration(t, []string{"--mode=serve"}, nil); err == nil || !errors.Is(err, v013.ErrTargetIncompatible) {
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
	if _, err := executeSQLiteMigration(t, []string{"--mode=serve", "--data-dir", dir}, nil); !errors.Is(err, v013.ErrTargetIncompatible) {
		t.Fatalf("SQLite migration loaded deployment .env: %v", err)
	}
}
