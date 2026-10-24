package v015

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	entschema "entgo.io/ent/dialect/sql/schema"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	source "github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015/source"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

func releaseFixture(t *testing.T, mode string) (*sql.DB, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m, err := entschema.NewMigrate(entsql.OpenDB("sqlite3", db))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Create(context.Background(), source.Tables...); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("original-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 1, 2, 3, 456789123, time.UTC).Format(timeutil.SQLiteLayout)
	name := "OriginalAdmin"
	var local any
	if mode == "local" {
		name = "local"
		local = 1
	}
	for _, statement := range []string{"PRAGMA user_version=1"} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO users(id,created_at,updated_at,username,password_hash,email,role,active) VALUES(1,?,?,?,?,?,'admin',true)", now, now, name, string(hash), "Admin@EXAMPLE.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO instance_initializations(id,created_at,updated_at,version,data_version,mode,local_user_id) VALUES(1,?,?,1,2,?,?)", now, now, mode, local); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO system_settings(created_at,updated_at,key,value) VALUES(?,?,'registration_enabled','false')", now, now); err != nil {
		t.Fatal(err)
	}
	return db, path, string(hash)
}

func fixturePlan(t *testing.T, db *sql.DB, mode string) *operation.Plan {
	t.Helper()
	users, err := ReadUsers(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	local := 0
	if mode == "local" {
		local = 1
	}
	accounts, err := MapAccounts(users, Mapping{}, mode, local)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := operation.NewID()
	p := &operation.Plan{ID: id, Version: operation.Version, Strategy: operation.Clone, DatabaseBackup: operation.BackupAuto, Kind: "upgrade", Route: "upgrade-v015-auth1", Source: operation.Endpoint{Driver: "sqlite", Mode: mode}, Accounts: accounts, Build: "release-test", TargetSignature: operation.Digest("fixture")}
	inspection, err := Probe(context.Background(), db, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	p.SourceFingerprint, p.SourceSchema = inspection.Fingerprint, inspection.SchemaDigest
	baseline, err := CaptureBaseline(context.Background(), db, p)
	if err != nil {
		t.Fatal(err)
	}
	p.Baseline = baseline
	return p
}
