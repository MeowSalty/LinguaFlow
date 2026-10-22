package migrationcli

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/v013"
)

// Each test uses an isolated schema. The checked-in DDL was generated from the
// actual v0.13.0 ent migration tables, rather than today's schema minus columns.
func legacyPostgresFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	namespace := fmt.Sprintf("migration_v013_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + namespace); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec("DROP SCHEMA " + namespace + " CASCADE")
		if err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	sep := " "
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		sep = "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
	}
	dsn += sep + "search_path=" + namespace
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	schema, err := os.ReadFile("../migration/v013/testdata/v013-postgres.sql")
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
	seed := []string{
		`INSERT INTO users (id, created_at, updated_at, username, password_hash, email, role, active) VALUES (1, now(), now(), 'old-admin', $1, 'admin@test.invalid', 'admin', true)`,
		`INSERT INTO backends (id, created_at, updated_at, name, scope, owner_user_id, backend_type, options) VALUES (1, now(), now(), 'old-backend', 'user', 1, 'openai', '{"api_key":"backend-original-key","model":"old-model"}')`,
		`INSERT INTO projects (id, created_at, updated_at, name, owner_user_id, config) VALUES (1, now(), now(), 'old-project', 1, '{}')`,
		`INSERT INTO system_settings (id, created_at, updated_at, key, value) VALUES (1, now(), now(), 'registration_enabled', 'false'), (2, now(), now(), 'auto_admin', 'true')`,
		`INSERT INTO execution_profiles (id, created_at, updated_at, name, scope, owner_user_id, config) VALUES (1, now(), now(), 'old-profile', 'user', 1, '{"glossary":{"bootstrap":{"enabled":true,"max_terms_per_1000_chars":3,"min_source_len":2,"inline_conflict_strategy":"rewrite-local"}},"ruby":{"enabled":false}}')`,
	}
	for i, statement := range seed {
		var args []any
		if i == 0 {
			args = []any{string(hash)}
		}
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatalf("seed statement %d: %v", i, err)
		}
	}
	seedLegacyRunnableJob(t, db, true)
	// Explicit fixture IDs preserve historical relationships, but PostgreSQL
	// identity sequences do not advance on explicit inserts. Match the state
	// of a database populated through normal application writes before migration.
	for _, table := range legacyPostgresSeedTables {
		statement := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', 'id'), (SELECT MAX(id) FROM %s))`, table, table)
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("sync fixture identity sequence for %s: %v", table, err)
		}
	}
	return db, dsn
}

var legacyPostgresSeedTables = []string{
	"users", "backends", "projects", "system_settings", "execution_profiles",
	"resources", "segments", "jobs", "job_resources", "job_rounds", "job_round_segments",
}

func TestLegacyPostgresFixtureIdentitySequences(t *testing.T) {
	db, _ := legacyPostgresFixture(t)
	for _, table := range legacyPostgresSeedTables {
		t.Run(table, func(t *testing.T) {
			var nextID, maxID int64
			statement := fmt.Sprintf(`SELECT nextval(pg_get_serial_sequence('%s', 'id')), MAX(id) FROM %s`, table, table)
			if err := db.QueryRow(statement).Scan(&nextID, &maxID); err != nil {
				t.Fatal(err)
			}
			if nextID <= maxID {
				t.Fatalf("next identity %d would overlap fixture IDs through %d", nextID, maxID)
			}
		})
	}
}

func runMigrationCLI(t *testing.T, dsn, dir string, apply bool) (string, error) {
	t.Helper()
	return runMigrationCLIWithEnvironment(t, dsn, dir, apply, nil)
}

func runMigrationCLIWithEnvironment(t *testing.T, dsn, dir string, apply bool, environment map[string]string) (string, error) {
	t.Helper()
	clearDeploymentEnvironment(t)
	t.Setenv("LINGUAFLOW_DATABASE_DSN", dsn)
	for name, value := range environment {
		t.Setenv(name, value)
	}
	root := NewCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	args := []string{"--data-dir", dir}
	if apply {
		args = append(args, "--apply")
	}
	root.SetArgs(append([]string{"v013", "postgres"}, args...))
	err := root.Execute()
	for _, secret := range []string{"backend-original-key", "job-original-key", dsn, environment["LINGUAFLOW_CREDENTIALS_MASTER_KEY"], environment["LINGUAFLOW_JWT_SECRET"]} {
		if secret != "" && (strings.Contains(output.String(), secret) || (err != nil && strings.Contains(err.Error(), secret))) {
			t.Fatal("migration output leaked sensitive data")
		}
	}
	return output.String(), err
}

func assertLegacyPostgresUnchanged(t *testing.T, db *sql.DB) {
	t.Helper()
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='credentials')`).Scan(&exists); err != nil || exists {
		t.Fatalf("schema changes survived rollback: %v", err)
	}
	var key, settings string
	if err := db.QueryRow(`SELECT options->>'api_key' FROM backends WHERE id=1`).Scan(&key); err != nil || key != "backend-original-key" {
		t.Fatalf("backend changed during rollback: %v", err)
	}
	if err := db.QueryRow(`SELECT value FROM system_settings WHERE key='auto_admin'`).Scan(&settings); err != nil || settings != "true" {
		t.Fatalf("settings changed during rollback: %v", err)
	}
}

func TestLegacyPostgresCommandsRejectWithoutChangingSource(t *testing.T) {
	db, dsn := legacyPostgresFixture(t)
	dir := filepath.Join(t.TempDir(), "not-created")
	for _, apply := range []bool{false, true} {
		if _, err := runMigrationCLI(t, dsn, dir, apply); !errors.Is(err, v013.ErrTargetIncompatible) {
			t.Fatalf("unsupported PostgreSQL command: %v", err)
		}
		assertLegacyPostgresUnchanged(t, db)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected PostgreSQL migration created key files")
	}
}
