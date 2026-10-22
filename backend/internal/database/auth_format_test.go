package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestAuthFormatReservationSurvivesSchemaFailure(t *testing.T) {
	cfg := sqliteFormatConfig(t)
	ctx := t.Context()
	db, client, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if client != nil {
			_ = client.Close()
		}
	})
	var version int
	if err := db.QueryRow("SELECT version FROM " + authFormatTable + " WHERE id=1").Scan(&version); err != nil || version != AuthFormatVersion {
		t.Fatalf("format reservation=%d: %v", version, err)
	}
	// Simulate an interrupted schema creation after one new table exists.
	if _, err := db.Exec("CREATE TABLE preparation_was_interrupted (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	_, client, err = Open(ctx, cfg)
	if err != nil {
		t.Fatalf("current reservation cannot resume: %v", err)
	}
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	client.Close()
	cfg.AutoMigrate = false
	_, client, err = Open(ctx, cfg)
	if err != nil {
		t.Fatalf("read-only format verification: %v", err)
	}
	client.Close()
}

func TestAuthFormatRejectsOldOrInvalidMarkersWithoutChanges(t *testing.T) {
	for _, setup := range []string{
		"CREATE TABLE users (id INTEGER, username TEXT)",
		"CREATE VIEW previous_accounts AS SELECT 1 AS id",
		"CREATE TABLE linguaflow_auth_format (id INTEGER, version INTEGER)",
		"CREATE TABLE linguaflow_auth_format (id INTEGER, version INTEGER); INSERT INTO linguaflow_auth_format VALUES(1,0)",
		"CREATE TABLE linguaflow_auth_format (id INTEGER, version INTEGER); INSERT INTO linguaflow_auth_format VALUES(1,2)",
		"CREATE TABLE linguaflow_auth_format (id INTEGER, version INTEGER); INSERT INTO linguaflow_auth_format VALUES(2,1)",
		"CREATE TABLE linguaflow_auth_format (id INTEGER, version INTEGER); INSERT INTO linguaflow_auth_format VALUES(1,1),(1,1)",
		"CREATE TABLE linguaflow_auth_format (id INTEGER, version TEXT); INSERT INTO linguaflow_auth_format VALUES(1,'invalid')",
		"CREATE VIEW linguaflow_auth_format AS SELECT 1 AS id, 1 AS version",
	} {
		t.Run(setup, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "existing.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(setup); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, auto := range []bool{false, true} {
				if err := ensureAuthFormat(t.Context(), db, config.DatabaseDriverSQLite, auto); !errors.Is(err, ErrAuthFormatUnsupported) {
					t.Fatalf("auto_migrate=%v: %v", auto, err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatalf("format rejection changed the database: %v", err)
			}
		})
	}
}

func TestAuthFormatReadOnlyEmptyDoesNotReserve(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ensureAuthFormat(t.Context(), db, config.DatabaseDriverSQLite, false); err == nil {
		t.Fatal("empty database was accepted with auto_migrate=false")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("read-only check reserved a format: count=%d error=%v", count, err)
	}
}

func TestAuthFormatOpenRejectsLegacyWithoutChangingJournalMode(t *testing.T) {
	for _, query := range []string{
		"", // Default WAL configuration.
		"_pragma=journal_mode(WAL)",
		"_pragma=main.journal_mode(WAL)",
		"_journal_mode=WAL",
		"_journal=WAL",
		"_journal_mode=DELETE&_journal=WAL",
		"_pragma=application_id(731)",
		"_auto_vacuum=FULL&_journal=WAL",
		"_vacuum=INCREMENTAL&_journal=WAL",
	} {
		for _, autoMigrate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/auto=%t", query, autoMigrate), func(t *testing.T) {
				testAuthFormatLegacyDSNUnchanged(t, query, autoMigrate)
			})
		}
	}
}

func testAuthFormatLegacyDSNUnchanged(t *testing.T, query string, autoMigrate bool) {
	t.Helper()
	cfg := sqliteFormatConfig(t)
	cfg.AutoMigrate = autoMigrate
	if query != "" {
		cfg.Database.DSN = cfg.DatabasePath() + "?" + query
	}
	db, err := sql.Open("sqlite", cfg.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=1; CREATE TABLE users(id INTEGER, username TEXT); INSERT INTO users VALUES(7,'Original')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfg.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, client, err := Open(t.Context(), cfg); !errors.Is(err, ErrAuthFormatUnsupported) {
		if client != nil {
			client.Close()
		}
		t.Fatalf("old account format was accepted: %v", err)
	}
	after, err := os.ReadFile(cfg.DatabasePath())
	if err != nil || string(before) != string(after) {
		t.Fatalf("opening old account data changed its header or contents: %v", err)
	}
}

func TestAuthFormatIndependentSQLiteConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	const count = 6
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
			if err == nil {
				defer db.Close()
				err = ensureAuthFormat(t.Context(), db, config.DatabaseDriverSQLite, true)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuthFormatPostgresIsolationAndConcurrency(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test DSN")
	}
	admin := stdlib.OpenDB(*parsed)
	defer admin.Close()
	schema := fmt.Sprintf("auth_format_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	parsed.RuntimeParams["search_path"] = schema
	open := func() *sql.DB {
		db := stdlib.OpenDB(*parsed)
		db.SetMaxOpenConns(1)
		return db
	}
	db := open()
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE historical_account(id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	for _, auto := range []bool{false, true} {
		if err := ensureAuthFormat(t.Context(), db, config.DatabaseDriverPostgres, auto); !errors.Is(err, ErrAuthFormatUnsupported) {
			t.Fatalf("legacy PostgreSQL format: %v", err)
		}
	}
	if _, err := db.Exec("DROP TABLE historical_account"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			connection := open()
			defer connection.Close()
			errs <- ensureAuthFormat(ctx, connection, config.DatabaseDriverPostgres, true)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureAuthFormat(ctx, db, config.DatabaseDriverPostgres, false); err == nil {
		t.Fatal("incomplete schema was accepted in read-only mode")
	}
	if _, err := db.Exec("UPDATE " + authFormatTable + " SET version=2"); err != nil {
		t.Fatal(err)
	}
	for _, auto := range []bool{false, true} {
		if err := ensureAuthFormat(ctx, db, config.DatabaseDriverPostgres, auto); !errors.Is(err, ErrAuthFormatUnsupported) {
			t.Fatalf("future PostgreSQL format accepted: %v", err)
		}
	}
}
