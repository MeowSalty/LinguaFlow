package v013

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteLegacyTimeEncoding(t *testing.T) {
	for _, input := range []string{
		"2026-09-29 11:00:00.123456789 +0800 CST",
		"2026-09-28 20:00:00.123456789 -0700 PDT m=+1234.456",
		"2026-09-29T03:00:00.123456789Z",
		"2026-09-29 05:00:00.123456789+02:00",
	} {
		instant, err := parseSQLiteLegacyTime(input)
		if err != nil || instant.Format(sqliteTargetTimeLayout) != "2026-09-29T03:00:00.123456789Z" || instant.Location() != time.UTC {
			t.Fatalf("parse %q: %v %v", input, instant, err)
		}
	}
	for _, input := range []string{"2026-09-29 03:00:00", "1720000000", "2026-09-29", "", "secret-invalid-time", "2026-09-29 03:00:00 +0000 UTC m=garbage", "2026-09-29 03:00:00 +0000 UTC m=+Inf", "2026-09-29T03:00:00.1234567891Z"} {
		if _, err := parseSQLiteLegacyTime(input); err == nil {
			t.Fatalf("accepted ambiguous timestamp %q", input)
		}
	}
}

func TestSQLiteLegacyTimeInventoryAndRollback(t *testing.T) {
	dir := localFixture(t, false)
	db := openLocalFixture(t, dir)
	rows, err := db.Query(`SELECT m.name,p.name FROM sqlite_schema m JOIN pragma_table_info(m.name) p WHERE m.type='table' AND lower(p.type)='datetime'`)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range sqliteLegacyTimes[table] {
			found = found || c == column
		}
		if !found {
			t.Fatalf("fixture time column missing from converter: %s.%s", table, column)
		}
		count++
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	expected := 0
	for _, cols := range sqliteLegacyTimes {
		expected += len(cols)
	}
	if count != expected {
		t.Fatalf("columns=%d expected=%d", count, expected)
	}
	if _, err := db.Exec(`INSERT INTO system_settings(created_at,updated_at,key,value) VALUES(?,?,'registration_enabled','true')`, localOldTimestamp, localOldTimestamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET created_at='sensitive-invalid-value' WHERE id=5`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	err = convertSQLiteTimes(context.Background(), filepath.Join(dir, localDatabaseName))
	if err == nil || !strings.Contains(err.Error(), "users id=5 column=created_at") || strings.Contains(err.Error(), "sensitive-invalid-value") {
		t.Fatalf("error=%v", err)
	}
	db = openLocalFixture(t, dir)
	var version int
	var timestamp string
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 0 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err := db.QueryRow("SELECT CAST(created_at AS TEXT) FROM system_settings").Scan(&timestamp); err != nil || timestamp != localOldTimestamp {
		t.Fatalf("partial update escaped transaction: %s %v", timestamp, err)
	}
}

func TestSQLiteLegacyNullableAndZeroTimes(t *testing.T) {
	dir := localFixture(t, true)
	db := openLocalFixture(t, dir)
	if _, err := db.Exec(`UPDATE users SET created_at='0001-01-01 00:00:00 +0000 UTC' WHERE id=5`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := convertSQLiteTimes(context.Background(), filepath.Join(dir, localDatabaseName)); err != nil {
		t.Fatal(err)
	}
	db = openLocalFixture(t, dir)
	var value string
	var nullCount int
	if err := db.QueryRow(`SELECT CAST(created_at AS TEXT) FROM users WHERE id=5`).Scan(&value); err != nil || value != "0001-01-01T00:00:00.000000000Z" {
		t.Fatalf("zero time=%s %v", value, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM refresh_tokens WHERE revoked_at IS NULL`).Scan(&nullCount); err != nil || nullCount != 1 {
		t.Fatalf("nullable time lost: %d %v", nullCount, err)
	}
}

func TestSQLiteSnapshotIncludesCommittedWAL(t *testing.T) {
	dir := localFixture(t, false)
	db := openLocalFixture(t, dir)
	for _, query := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", `UPDATE users SET display_name='committed only in WAL' WHERE id=5`} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, localDatabaseName+"-wal"))
	if err != nil || info.Size() == 0 {
		t.Fatalf("no source WAL: %v", err)
	}
	target := filepath.Join(t.TempDir(), localDatabaseName)
	if err := snapshotSQLite(context.Background(), filepath.Join(dir, localDatabaseName), target); err != nil {
		t.Fatal(err)
	}
	copy, err := sql.Open("sqlite", sqliteURI(target, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var name string
	if err := copy.QueryRow(`SELECT display_name FROM users WHERE id=5`).Scan(&name); err != nil || name != "committed only in WAL" {
		t.Fatalf("WAL data omitted: %q %v", name, err)
	}
	// Sidecars must never be copied over the independently backed-up database.
	stage := t.TempDir()
	if err := copyLocalFiles(context.Background(), dir, stage); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Stat(filepath.Join(stage, localDatabaseName+suffix)); !os.IsNotExist(err) {
			t.Fatalf("copied old database file %s", suffix)
		}
	}
}

func TestSQLiteUnsupportedTimestampStorage(t *testing.T) {
	for _, value := range []string{"1700000000", "1700000000.125", "x'1234'", "'2026-09-29 03:00:00'"} {
		t.Run(value, func(t *testing.T) {
			dir := localFixture(t, false)
			db := openLocalFixture(t, dir)
			if _, err := db.Exec(fmt.Sprintf("UPDATE users SET created_at=%s WHERE id=5", value)); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := convertSQLiteTimes(context.Background(), filepath.Join(dir, localDatabaseName)); err == nil {
				t.Fatal("unsupported source storage accepted")
			}
		})
	}
}

func TestSQLiteRejectsLinkedSidecarsBeforeOpeningSource(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			dir := localFixture(t, false)
			external := filepath.Join(t.TempDir(), "external-data")
			if err := os.WriteFile(external, []byte("must not be opened by SQLite"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, filepath.Join(dir, localDatabaseName+suffix)); err != nil {
				t.Skipf("symlink privilege unavailable: %v", err)
			}
			target := filepath.Join(t.TempDir(), "copy.db")
			if err := snapshotSQLite(t.Context(), filepath.Join(dir, localDatabaseName), target); err == nil {
				t.Fatal("SQLite opened an unsafe sidecar")
			}
			if string(readLocalBytes(t, external)) != "must not be opened by SQLite" {
				t.Fatal("external sidecar target changed")
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("destination created before unsafe sidecar was rejected")
			}
		})
	}
}
