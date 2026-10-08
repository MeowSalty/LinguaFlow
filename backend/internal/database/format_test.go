package database

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func sqliteFormatConfig(t *testing.T) *config.ServerConfig {
	t.Helper()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.MaxOpenConns = 1
	return cfg
}

func readFormatVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestSQLiteFormatInitializeReopenAndRetry(t *testing.T) {
	cfg := sqliteFormatConfig(t)
	ctx := context.Background()
	db, client, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFormatVersion(t, db); got != 1 {
		t.Fatalf("version=%d want 1", got)
	}
	// A failed first schema attempt must leave a retryable format marker.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := client.Schema.Create(canceled); err == nil {
		t.Fatal("canceled schema initialization should fail")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	_, client, err = Open(ctx, cfg)
	if err != nil {
		t.Fatalf("reopen marked empty database: %v", err)
	}
	for range 2 {
		if err := client.Schema.Create(ctx); err != nil {
			_ = client.Close()
			t.Fatalf("repeat schema initialization: %v", err)
		}
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.AutoMigrate = false
	db, client, err = Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open initialized database without migrations: %v", err)
	}
	defer client.Close()
	if got := readFormatVersion(t, db); got != 1 {
		t.Fatalf("reopened version=%d want 1", got)
	}
}

func TestSQLiteOldFormatRejectedWithoutChangingSchemaOrData(t *testing.T) {
	for _, schema := range []string{
		"CREATE TABLE historical_data (value TEXT)",
		"CREATE VIEW historical_data AS SELECT 'original' AS value",
	} {
		t.Run(schema, func(t *testing.T) {
			cfg := sqliteFormatConfig(t)
			db, err := sql.Open("sqlite", cfg.DatabasePath())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(schema); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(schema, "TABLE") {
				if _, err := db.Exec("INSERT INTO historical_data VALUES ('original')"); err != nil {
					t.Fatal(err)
				}
			}
			_, client, err := Open(context.Background(), cfg)
			if err == nil {
				_ = client.Close()
				t.Fatal("legacy database must be rejected")
			}
			if got := readFormatVersion(t, db); got != 0 {
				t.Fatalf("legacy database was marked: %d", got)
			}
			var original, actualSchema string
			if err := db.QueryRow("SELECT value FROM historical_data").Scan(&original); err != nil || original != "original" {
				t.Fatalf("legacy data changed: %q error=%v", original, err)
			}
			if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name = 'historical_data'").Scan(&actualSchema); err != nil || actualSchema != schema {
				t.Fatalf("legacy schema changed: %q error=%v", actualSchema, err)
			}
			var applicationTables int
			if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&applicationTables); err != nil {
				t.Fatal(err)
			}
			want := 0
			if strings.Contains(schema, "TABLE") {
				want = 1
			}
			if applicationTables != want {
				t.Fatalf("legacy database unexpectedly initialized: %d tables", applicationTables)
			}
		})
	}
}

func TestSQLiteFormatRejectsUnknownVersion(t *testing.T) {
	cfg := sqliteFormatConfig(t)
	db, err := sql.Open("sqlite", cfg.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	_, client, err := Open(context.Background(), cfg)
	if err == nil {
		_ = client.Close()
		t.Fatal("unknown format must be rejected")
	}
	if got := readFormatVersion(t, db); got != 2 {
		t.Fatalf("unknown version changed to %d", got)
	}
}

func TestSQLiteAutoMigrateFalseRejectsUninitializedDatabase(t *testing.T) {
	for _, state := range []string{"empty", "marked", "partial tables", "partial columns"} {
		t.Run(state, func(t *testing.T) {
			cfg := sqliteFormatConfig(t)
			if state == "partial columns" {
				_, client, err := Open(context.Background(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := client.Schema.Create(context.Background()); err != nil {
					_ = client.Close()
					t.Fatal(err)
				}
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
			}
			cfg.AutoMigrate = false
			db, err := sql.Open("sqlite", cfg.DatabasePath())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if state != "empty" {
				if _, err := db.Exec("PRAGMA user_version=1"); err != nil {
					t.Fatal(err)
				}
			}
			if state == "partial tables" {
				if _, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY)"); err != nil {
					t.Fatal(err)
				}
			}
			if state == "partial columns" {
				if _, err := db.Exec("ALTER TABLE users DROP COLUMN updated_at"); err != nil {
					t.Fatal(err)
				}
			}
			_, client, err := Open(context.Background(), cfg)
			if err == nil {
				_ = client.Close()
				t.Fatal("uninitialized database must not open with auto_migrate=false")
			}
			wantVersion := 1
			if state == "empty" {
				wantVersion = 0
			}
			if got := readFormatVersion(t, db); got != wantVersion {
				t.Fatalf("version changed: %d want %d", got, wantVersion)
			}
		})
	}
}

func TestSQLiteConcurrentFormatInitialization(t *testing.T) {
	cfg := sqliteFormatConfig(t)
	// This also exercises a custom DSN: the format transaction must release its
	// connection before callers attempt schema creation on their own pool.
	cfg.Database.DSN = filepath.Join(cfg.DataDir, "concurrent.db") + "?_pragma=busy_timeout(5000)"
	const count = 4
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			db, client, err := Open(context.Background(), cfg)
			if err != nil {
				errs <- err
				return
			}
			defer client.Close()
			var version int
			if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
				errs <- err
			} else if version != 1 {
				errs <- fmt.Errorf("format version=%d", version)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	_, client, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("schema after concurrent format initialization: %v", err)
	}
}

func TestSQLiteRejectsTimeDSNOverrides(t *testing.T) {
	for _, key := range []string{"_time_format", "_time_integer_format", "_timezone", "_inttotime", "_texttotime"} {
		for _, value := range []string{"", "UTC"} {
			t.Run(key+"="+value, func(t *testing.T) {
				cfg := sqliteFormatConfig(t)
				cfg.Database.DSN = filepath.Join(cfg.DataDir, "overridden.db") + "?" + key + "=" + value
				_, client, err := Open(context.Background(), cfg)
				if err == nil {
					_ = client.Close()
					t.Fatalf("time encoding override %s must be rejected", key)
				}
				if !strings.Contains(err.Error(), key) {
					t.Fatalf("error should identify rejected key %s: %v", key, err)
				}
			})
		}
	}
}
