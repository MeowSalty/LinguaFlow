package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/jackc/pgx/v5"
)

func TestReadOnlyConnectionsAndIndependentSQLiteSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE evidence(id INTEGER PRIMARY KEY,value TEXT NOT NULL); INSERT INTO evidence VALUES(1,'original')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := operation.Hash(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.ServerConfig{DataDir: dir, Database: config.DatabaseConfig{Driver: "sqlite", DSN: source + "?_pragma=journal_mode(WAL)&_pragma=user_version(99)&mode=rwc"}}
	ro, _, err := Open(ctx, &cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Exec("UPDATE evidence SET value='modified'"); err == nil {
		t.Fatal("source connection is writable")
	}
	var version int
	if err := ro.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 0 {
		t.Fatal("source pragma changed", err)
	}
	copy := filepath.Join(t.TempDir(), "independent.sqlite")
	if err := SQLite(ctx, ro, copy); err != nil {
		t.Fatal(err)
	}
	cfg.Database.DSN = copy
	restored, _, err := Open(ctx, &cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.Exec("UPDATE evidence SET value='independent'"); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := ro.QueryRow("SELECT value FROM evidence").Scan(&value); err != nil || value != "original" {
		t.Fatal("snapshot shares source state", err)
	}
	if err := SQLite(ctx, ro, copy); err == nil {
		t.Fatal("snapshot overwrote occupied path")
	}
	after, err := operation.Hash(ctx, source)
	if err != nil || after.SHA256 != before.SHA256 {
		t.Fatal("source bytes changed", err)
	}
	missing := filepath.Join(dir, "missing.db")
	cfg.Database.DSN = missing
	if d, _, err := Open(ctx, &cfg, true); err == nil {
		d.Close()
		t.Fatal("missing source opened")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("probe created source")
	}
}

func TestPostgresReadOnlyInputPreservesExplicitConnection(t *testing.T) {
	for _, dsn := range []string{"postgres://user:secret@example.invalid:5433/db?sslmode=verify-full&default_transaction_read_only=off", "host=example.invalid port=5433 user=user password='secret' dbname=db sslmode=disable default_transaction_read_only=off"} {
		readOnly, err := ReadOnlyDSN("postgres", dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := pgx.ParseConfig(readOnly)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Host != "example.invalid" || cfg.Port != 5433 || cfg.Database != "db" || cfg.Password != "secret" || cfg.RuntimeParams["default_transaction_read_only"] != "on" || cfg.RuntimeParams["timezone"] != "UTC" {
			t.Fatal("readonly normalization changed connection identity")
		}
	}
}
