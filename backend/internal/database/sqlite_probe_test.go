package database

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestAuthFormatRuntimePoolPreservesJournalConfiguration(t *testing.T) {
	for _, test := range []struct{ query, mode string }{
		{"_pragma=journal_mode(WAL)", "wal"},
		{"_pragma=main.journal_mode(TRUNCATE)", "truncate"},
		{"_journal_mode=PERSIST", "persist"},
		{"_journal=MEMORY", "memory"},
		{"_journal_mode=WAL&_journal=TRUNCATE", "truncate"},
		{"_pragma=journal_mode(WAL)&_journal=MEMORY", "memory"},
	} {
		t.Run(test.query, func(t *testing.T) {
			cfg := sqliteFormatConfig(t)
			cfg.Database.DSN = cfg.DatabasePath() + "?" + test.query
			cfg.Database.MaxOpenConns, cfg.Database.MaxIdleConns = 3, 3
			cfg.Database.ConnMaxLifetime = 0
			db, client, err := Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			for range 2 {
				assertSQLitePoolJournal(t, db, test.mode, 3)
				// Force replacement of all physical connections; the runtime DSN
				// must configure their successors as well as the startup connection.
				db.SetMaxIdleConns(0)
				db.SetMaxIdleConns(3)
			}
		})
	}
}

func TestAuthFormatReadOnlyRuntimePoolPreservesConfiguration(t *testing.T) {
	cfg := sqliteFormatConfig(t)
	_, client, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Schema.Create(t.Context()); err != nil {
		client.Close()
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.AutoMigrate = false
	cfg.Database.DSN = cfg.DatabasePath() + "?_query_only=1&_journal=MEMORY"
	cfg.Database.MaxOpenConns, cfg.Database.MaxIdleConns = 3, 3
	db, client, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("read-only format validation attempted a write: %v", err)
	}
	defer client.Close()
	assertSQLitePoolJournal(t, db, "memory", 3)
	if _, err := db.ExecContext(t.Context(), "UPDATE linguaflow_auth_format SET version=2"); err == nil {
		t.Fatal("runtime pool lost query_only setting")
	}
}

func TestAuthFormatProbePreservesLiteralFilenameCharacters(t *testing.T) {
	cfg := sqliteFormatConfig(t)
	cfg.Database.DSN = filepath.Join(cfg.DataDir, "a % database #1.db")
	_, client, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	_, client, err = Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("reopen with literal filename characters: %v", err)
	}
	defer client.Close()
	if _, err := os.Stat(cfg.Database.DSN); err != nil {
		t.Fatalf("probe changed database path spelling: %v", err)
	}
}

func TestAuthFormatInMemoryRuntimeRetainsMarkers(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared=%t", shared), func(t *testing.T) {
			cfg := config.DefaultServerConfig()
			cfg.Database.DSN = ":memory:?_journal=MEMORY"
			cfg.Database.MaxOpenConns, cfg.Database.MaxIdleConns = 1, 1
			cfg.Database.ConnMaxLifetime = 0
			if shared {
				cfg.Database.DSN = "file:" + t.Name() + "?mode=memory&cache=shared&_journal=MEMORY"
				cfg.Database.MaxOpenConns, cfg.Database.MaxIdleConns = 3, 3
			}
			db, client, err := Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			assertSQLitePoolJournal(t, db, "memory", cfg.Database.MaxOpenConns)
		})
	}
}

func TestAuthFormatSharedMemoryRejectsExistingAccounts(t *testing.T) {
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	keeper, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer keeper.Close()
	if _, err := keeper.Exec("PRAGMA user_version=1; CREATE TABLE users(id INTEGER); INSERT INTO users VALUES(7)"); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultServerConfig()
	cfg.Database.DSN = dsn + "&_pragma=application_id(731)"
	if _, client, err := Open(t.Context(), cfg); !errors.Is(err, ErrAuthFormatUnsupported) {
		if client != nil {
			client.Close()
		}
		t.Fatalf("existing shared-memory accounts accepted: %v", err)
	}
	var id, applicationID int
	if err := keeper.QueryRow("SELECT id, (SELECT application_id FROM pragma_application_id) FROM users").Scan(&id, &applicationID); err != nil || id != 7 || applicationID != 0 {
		t.Fatalf("rejected shared memory changed: id=%d application_id=%d err=%v", id, applicationID, err)
	}
}

func TestAuthFormatRejectedWALIsNotCheckpointed(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.db")
	writer, err := sql.Open("sqlite", source+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec("PRAGMA wal_autocheckpoint=0; PRAGMA user_version=1; CREATE TABLE users(id INTEGER); INSERT INTO users VALUES(7)"); err != nil {
		t.Fatal(err)
	}
	// Copy a consistent, quiescent database and its uncheckpointed WAL while
	// retaining the source writer. Opening the copy must not consolidate it.
	cfg := sqliteFormatConfig(t)
	before := map[string][]byte{}
	for _, suffix := range []string{"", "-wal"} {
		contents, err := os.ReadFile(source + suffix)
		if err != nil {
			t.Fatal(err)
		}
		before[suffix] = contents
		if err := os.WriteFile(cfg.DatabasePath()+suffix, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, client, err := Open(t.Context(), cfg); !errors.Is(err, ErrAuthFormatUnsupported) {
		if client != nil {
			client.Close()
		}
		t.Fatalf("old WAL database accepted: %v", err)
	}
	for suffix, want := range before {
		got, err := os.ReadFile(cfg.DatabasePath() + suffix)
		if err != nil || string(got) != string(want) {
			t.Fatalf("rejected WAL database changed %q: %v", suffix, err)
		}
	}
}

func TestAuthFormatConcurrentOpenOfNewSQLiteDatabase(t *testing.T) {
	// Competing current-version starters only: every writer reserves the new
	// format before schema creation. Replacing the file by another application
	// between probe and runtime open is outside the supported startup contract.
	cfg := sqliteFormatConfig(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, client, err := Open(t.Context(), cfg)
			if err != nil {
				err = fmt.Errorf("concurrent SQLite Open: %w", err)
			} else if err = client.Close(); err != nil {
				err = fmt.Errorf("concurrent SQLite Close: %w", err)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

func assertSQLitePoolJournal(t *testing.T, db *sql.DB, want string, count int) {
	t.Helper()
	var held []*sql.Conn
	defer func() {
		for _, conn := range held {
			conn.Close()
		}
	}()
	for range count {
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
		var mode string
		if err := conn.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != want {
			t.Fatalf("journal_mode=%q want %q: %v", mode, want, err)
		}
		var authVersion, timeVersion, foreignKeys int
		if err := conn.QueryRowContext(t.Context(), "SELECT version, (SELECT user_version FROM pragma_user_version), (SELECT foreign_keys FROM pragma_foreign_keys) FROM linguaflow_auth_format WHERE id=1").Scan(&authVersion, &timeVersion, &foreignKeys); err != nil || authVersion != AuthFormatVersion || timeVersion != sqliteTimeFormatVersion || foreignKeys != 1 {
			t.Fatalf("runtime connection lost markers/settings: auth=%d time=%d foreign_keys=%d: %v", authVersion, timeVersion, foreignKeys, err)
		}
	}
}
