package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationDatabaseInputsIgnoreUnrelatedSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.yml")
	data := []byte("server:\n  data_dir: origin\n  database:\n    driver: sqlite\n    dsn: old.db\n  jwt_secret: ${MISSING_SECRET}\n  credentials:\n    keyring_file: absent.json\n  storage:\n    backends: bad-value\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := ResolveMigrationDatabase(ServerInputs{Mode: "serve", ConfigPath: &path, WorkingDirectory: dir, Environment: map[string]string{"LINGUAFLOW_JWT_SECRET_FILE": filepath.Join(dir, "absent.secret")}})
	if err != nil {
		t.Fatal(err)
	}
	if r.DataDir != filepath.Join(dir, "origin") || r.Database.DSN != filepath.Join(dir, "old.db") {
		t.Fatalf("relative inputs: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "origin")); !os.IsNotExist(err) {
		t.Fatal("probe resolver created data directory")
	}
}

func TestMigrationDatabaseInputsPrecedenceAndLocalDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "database.yml")
	if err := os.WriteFile(path, []byte("server:\n  data_dir: file-data\n  database:\n    driver: postgres\n    dsn: postgresql://invalid-password@db.example/db\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := ResolveMigrationDatabase(ServerInputs{Mode: "serve", ConfigPath: &path, WorkingDirectory: dir, Environment: map[string]string{"LINGUAFLOW_DATA_DIR": "env-data", "LINGUAFLOW_DATABASE_DRIVER": "sqlite", "LINGUAFLOW_DATABASE_DSN": "environment.db"}, Overrides: map[string]any{"server.data_dir": "flag-data"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.DataDir != filepath.Join(dir, "flag-data") || r.Database.Driver != "sqlite" || r.Database.DSN != filepath.Join(dir, "environment.db") {
		t.Fatalf("precedence: %+v", r)
	}
	r, err = ResolveMigrationDatabase(ServerInputs{Mode: "local", ConfigPath: &path, WorkingDirectory: dir, UserConfigDir: dir, Environment: map[string]string{"LINGUAFLOW_DATABASE_DSN_FILE": "absent"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.DataDir != filepath.Join(dir, "file-data") || r.Database.Driver != "sqlite" {
		t.Fatal("local defaults differ")
	}
}

func TestMigrationDatabaseInputsRequireExplicitSourceSelection(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []string{"serve", "local"} {
		if _, err := ResolveMigrationDatabase(ServerInputs{Mode: mode, WorkingDirectory: dir, UserConfigDir: dir, Environment: map[string]string{"LINGUAFLOW_JWT_SECRET_FILE": filepath.Join(dir, "missing-unrelated-secret")}}); err == nil {
			t.Fatal("offline input resolver selected an implicit runtime directory", mode)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Fatal("implicit source rejection created a runtime root")
	}
}

func TestMigrationDatabaseInputsRejectRelevantDuplicateAndRedact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "duplicate.yml")
	if err := os.WriteFile(path, []byte("server:\n  database:\n    driver: sqlite\n    driver: postgres\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveMigrationDatabase(ServerInputs{Mode: "serve", ConfigPath: &path, Environment: map[string]string{}}); err == nil {
		t.Fatal("duplicate accepted")
	}
	secret := "secret-query-password"
	_, err := ResolveMigrationDatabase(ServerInputs{Mode: "serve", Environment: map[string]string{"LINGUAFLOW_DATABASE_DRIVER": "postgres", "LINGUAFLOW_DATABASE_DSN": "postgresql://" + secret + "@%invalid"}})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error must be redacted: %v", err)
	}
}
