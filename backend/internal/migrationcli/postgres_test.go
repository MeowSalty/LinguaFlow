package migrationcli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/cli"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/v013"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
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

func TestMigrateV013PostgresRehearsalApplyAndStartup(t *testing.T) {
	db, dsn := legacyPostgresFixture(t)
	dir := filepath.Join(t.TempDir(), "data")
	if _, err := runMigrationCLI(t, dsn, dir, false); err != nil {
		t.Fatal(err)
	}
	assertLegacyPostgresUnchanged(t, db)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rehearsal created key files")
	}
	var originalHash string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id=1`).Scan(&originalHash); err != nil {
		t.Fatal(err)
	}
	if _, err := runMigrationCLI(t, dsn, dir, true); err != nil {
		t.Fatal(err)
	}
	keys, err := credential.LoadKeyring(filepath.Join(dir, "credentials-keyring.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultServerConfig()
	cfg.DataDir = dir
	cfg.Database = config.DatabaseConfig{Driver: config.DatabaseDriverPostgres, DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 2}
	_, client, err := database.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	admin := client.User.GetX(ctx, 1)
	if admin.PasswordHash != originalHash || !admin.Active || admin.Role != "admin" {
		t.Fatal("existing administrator identity changed")
	}
	snapshot, err := service.GetSnapshot(client.Job.GetX(ctx, 1))
	if err != nil {
		t.Fatal(err)
	}
	credentials := service.NewCredentialService(client, keys, nil)
	secret, err := credentials.Resolve(ctx, snapshot.Rounds[0].Backend.Credential, "openai", "https://api.openai.com/v1")
	if err != nil || secret != "job-original-key" {
		t.Fatal("historical snapshot lost its own credential")
	}
	keyBytes, err := os.ReadFile(filepath.Join(dir, "credentials-keyring.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runMigrationCLI(t, dsn, dir, true); err == nil || !strings.Contains(err.Error(), "already migrated") {
		t.Fatalf("repeat migration was not refused: %v", err)
	}
	again, err := os.ReadFile(filepath.Join(dir, "credentials-keyring.json"))
	if err != nil || !bytes.Equal(keyBytes, again) {
		t.Fatal("repeat migration replaced encryption keys")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	assertMigratedServeStarts(t, dsn, dir, port)
	assertMigratedJobReadableAndResumable(t, client, keys)
}

func TestMigrateV013PostgresMasterKeyRehearsalApplyAndStartup(t *testing.T) {
	for _, source := range []string{"environment", "file"} {
		t.Run(source, func(t *testing.T) {
			db, dsn := legacyPostgresFixture(t)
			temp := t.TempDir()
			dir := filepath.Join(temp, "data")
			master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{37}, 32))
			environment := map[string]string{
				"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master,
				"LINGUAFLOW_JWT_SECRET":             strings.Repeat("migration-jwt-test", 3),
			}
			if source == "file" {
				path := filepath.Join(temp, "master-key")
				if err := os.WriteFile(path, []byte(master+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				delete(environment, "LINGUAFLOW_CREDENTIALS_MASTER_KEY")
				environment["LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE"] = path
			}
			if _, err := runMigrationCLIWithEnvironment(t, dsn, dir, false, environment); err != nil {
				t.Fatal(err)
			}
			assertLegacyPostgresUnchanged(t, db)
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("master-key rehearsal created files")
			}
			output, err := runMigrationCLIWithEnvironment(t, dsn, dir, true, environment)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output, "same LINGUAFLOW_CREDENTIALS_MASTER_KEY") || strings.Contains(output, "LINGUAFLOW_CREDENTIALS_KEYRING_FILE=") || strings.Contains(output, master) {
				t.Fatal("incorrect or sensitive master-key migration instructions")
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("migration with supplied secrets created key files")
			}
			keys, err := credential.FromMasterKey(master)
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.DefaultServerConfig()
			cfg.DataDir = dir
			cfg.Database = config.DatabaseConfig{Driver: config.DatabaseDriverPostgres, DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 2}
			_, client, err := database.Open(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := service.NewCredentialService(client, keys, nil).ValidateKeys(context.Background()); err != nil {
				t.Fatalf("migration did not encrypt with supplied master key: %v", err)
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := ln.Addr().(*net.TCPAddr).Port
			ln.Close()
			startupEnvironment := []string{"LINGUAFLOW_DATABASE_DRIVER=postgres", "LINGUAFLOW_DATABASE_DSN=" + dsn, "LINGUAFLOW_STORAGE_INITIALIZATION_CAPACITY_BYTES=null", "LINGUAFLOW_STORAGE_INITIALIZATION_LOGICAL_LIMIT_BYTES=null"}
			for name, value := range environment {
				startupEnvironment = append(startupEnvironment, name+"="+value)
			}
			assertMigratedServiceStarts(t, dir, port, startupEnvironment)
			if _, err := os.Stat(filepath.Join(dir, "credentials-keyring.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("migrated service generated a second credential key")
			}
			assertMigratedJobReadableAndResumable(t, client, keys)
		})
	}
}

func TestMigrateV013PostgresFailureRollsBackSchemaAndData(t *testing.T) {
	db, dsn := legacyPostgresFixture(t)
	if _, err := db.Exec(`UPDATE jobs SET execution_config = '{}' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "data")
	if _, err := runMigrationCLI(t, dsn, dir, true); err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("invalid snapshot did not abort: %v", err)
	}
	assertLegacyPostgresUnchanged(t, db)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed conversion published keys")
	}
}

func TestMigrateV013PostgresPublishedKeySurvivesRollbackAndRetry(t *testing.T) {
	db, dsn := legacyPostgresFixture(t)
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "credentials-keyring.json")
	keys, pending, err := migrationKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("injected failure after key publication")
	_, err = v013.RunPostgres(context.Background(), db, keys, true, func() error {
		published, err := credential.PublishPrivateFile(path, pending)
		if err != nil || !published {
			t.Fatalf("publish migration key: %v", err)
		}
		return interrupted
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("expected injected failure, got %v", err)
	}
	assertLegacyPostgresUnchanged(t, db)
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, pending) {
		t.Fatal("published key was lost after rollback")
	}
	if _, err := runMigrationCLI(t, dsn, dir, true); err != nil {
		t.Fatalf("retry with retained key: %v", err)
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, pending) {
		t.Fatal("retry replaced the previously published key")
	}
	cfg := config.DefaultServerConfig()
	cfg.Database = config.DatabaseConfig{Driver: config.DatabaseDriverPostgres, DSN: dsn}
	_, client, err := database.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := service.NewCredentialService(client, keys, nil).ValidateKeys(context.Background()); err != nil {
		t.Fatalf("retried migration cannot decrypt with retained key: %v", err)
	}
}

// The public CLI runs in a child test process so startup is exercised without
// exposing private bootstrap hooks or linking migration into production cli.
func TestMigratedServeHelper(t *testing.T) {
	if os.Getenv("LINGUAFLOW_MIGRATION_STARTUP_HELPER") != "1" {
		return
	}
	os.Args = []string{"linguaflow", "serve"}
	if os.Getenv("LINGUAFLOW_MIGRATION_STARTUP_MODE") == "local" {
		os.Args = []string{"linguaflow", "local", "--no-browser"}
	}
	os.Exit(cli.Execute())
}

func assertMigratedServeStarts(t *testing.T, dsn, dir string, port int) {
	t.Helper()
	assertMigratedServiceStarts(t, dir, port, []string{
		"LINGUAFLOW_DATABASE_DRIVER=postgres", "LINGUAFLOW_DATABASE_DSN=" + dsn,
		"LINGUAFLOW_JWT_SECRET_FILE=" + filepath.Join(dir, "jwt-secret"),
		"LINGUAFLOW_CREDENTIALS_KEYRING_FILE=" + filepath.Join(dir, "credentials-keyring.json"),
	})
}

func assertMigratedServiceStarts(t *testing.T, dir string, port int, environment []string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestMigratedServeHelper$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "LINGUAFLOW_") {
			child.Env = append(child.Env, entry)
		}
	}
	child.Env = append(child.Env, environment...)
	child.Env = append(child.Env,
		"LINGUAFLOW_MIGRATION_STARTUP_HELPER=1", "LINGUAFLOW_DATA_DIR="+dir,
		"LINGUAFLOW_HOST=127.0.0.1", "LINGUAFLOW_PORT="+fmt.Sprint(port))
	log, err := os.Create(filepath.Join(t.TempDir(), "startup.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	child.Stdout, child.Stderr = log, log
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health/ready", port))
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	raw, _ := os.ReadFile(log.Name())
	t.Fatalf("migrated service did not become ready: %s", raw)
}
