package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	_ "modernc.org/sqlite"
)

func TestCheckMigrationStateSQLite(t *testing.T) {
	tests := []struct {
		name    string
		create  string
		insert  string
		wantErr error
	}{
		{name: "missing table"},
		{name: "ready", create: migrationStateDDLSQLite, insert: migrationStateRowSQLite("ready")},
		{name: "preparing", create: migrationStateDDLSQLite, insert: migrationStateRowSQLite("preparing"), wantErr: ErrMigrationIncomplete},
		{name: "abandoned", create: migrationStateDDLSQLite, insert: migrationStateRowSQLite("abandoned"), wantErr: ErrMigrationIncomplete},
		{name: "unknown state", create: migrationStateDDLSQLite, insert: migrationStateRowSQLite("unknown"), wantErr: ErrMigrationIncomplete},
		{name: "bad operation id", create: migrationStateDDLSQLite, insert: "INSERT INTO " + MigrationStateTable + " VALUES (1, 1, 'BAD', '" + strings.Repeat("a", 64) + "', '" + strings.Repeat("b", 64) + "', 'ready')", wantErr: ErrMigrationIncomplete},
		{name: "bad digest", create: migrationStateDDLSQLite, insert: "INSERT INTO " + MigrationStateTable + " VALUES (1, 1, '" + strings.Repeat("a", 32) + "', 'BAD', '" + strings.Repeat("b", 64) + "', 'ready')", wantErr: ErrMigrationIncomplete},
		{name: "duplicate rows", create: migrationStateDDLSQLite, insert: migrationStateRowSQLite("ready") + ";" + migrationStateRowSQLite("ready"), wantErr: ErrMigrationIncomplete},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if tt.create != "" {
				if _, err := db.Exec(tt.create); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(tt.insert); err != nil {
					t.Fatal(err)
				}
			}
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			err = CheckMigrationState(context.Background(), conn, config.DatabaseDriverSQLite)
			conn.Close()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error=%v want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckMigrationStateDoesNotCompareRuntimeSchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(migrationStateDDLSQLite + ";" + migrationStateRowSQLite("ready") + ";CREATE TABLE unrelated (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := CheckMigrationState(context.Background(), conn, config.DatabaseDriverSQLite); err != nil {
		t.Fatalf("ready state rejected because runtime schema differs: %v", err)
	}
}

func TestCheckMigrationStateRejectsUnknownDriver(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := CheckMigrationState(context.Background(), conn, "mysql"); err == nil || !strings.Contains(err.Error(), "unsupported database driver") {
		t.Fatalf("error=%v", err)
	}
}

func TestOpenSQLiteWithoutMigrationStateDoesNotCreateMarker(t *testing.T) {
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	db, client, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name = ?", MigrationStateTable).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("ordinary startup created migration state marker: count=%d", count)
	}
}

func TestOpenSQLiteRejectsIncompleteMigrationBeforeWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocked.db")
	setup, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec("PRAGMA user_version = 1;" + migrationStateDDLSQLite + ";" + migrationStateRowSQLite("preparing")); err != nil {
		setup.Close()
		t.Fatal(err)
	}
	if err := setup.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultServerConfig()
	cfg.DataDir = dir
	cfg.Database.DSN = path
	db, client, err := Open(context.Background(), cfg)
	if client != nil {
		client.Close()
	}
	if db != nil {
		db.Close()
	}
	if !errors.Is(err, ErrMigrationIncomplete) {
		t.Fatalf("error=%v want ErrMigrationIncomplete", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(before) != string(after) {
		t.Fatalf("incomplete migration changed database before startup write: %v", readErr)
	}
}

const migrationStateDDLSQLite = "CREATE TABLE " + MigrationStateTable + " (id INTEGER NOT NULL, version INTEGER NOT NULL, operation_id TEXT NOT NULL, plan_digest TEXT NOT NULL, target_signature TEXT NOT NULL, state TEXT NOT NULL)"

func migrationStateRowSQLite(state string) string {
	return fmt.Sprintf("INSERT INTO %s VALUES (1, 1, '%s', '%s', '%s', '%s')", MigrationStateTable, strings.Repeat("a", 32), strings.Repeat("b", 64), strings.Repeat("c", 64), state)
}
