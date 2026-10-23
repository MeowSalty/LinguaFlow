package v015

import (
	"context"
	"testing"

	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/dialect/sql/schema"
	"entgo.io/ent/schema/field"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

func TestProbeFrozenReleaseAndRejectsMixedSchemaWithoutWrites(t *testing.T) {
	ctx := context.Background()
	db, path, _ := releaseFixture(t, "serve")
	before, err := operation.Hash(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := Probe(ctx, db, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if probe.Format != "v015" || probe.Mode != "serve" || probe.Users != 1 {
		t.Fatalf("inspection: %+v", probe)
	}
	again, err := Probe(ctx, db, "sqlite")
	if err != nil || again.Fingerprint != probe.Fingerprint {
		t.Fatalf("unstable probe: %v", err)
	}
	after, err := operation.Hash(ctx, path)
	if err != nil || after.SHA256 != before.SHA256 {
		t.Fatal("probe wrote source database")
	}
	if _, err := db.Exec("ALTER TABLE users ADD COLUMN auth_version bigint NOT NULL DEFAULT 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(ctx, db, "sqlite"); err == nil {
		t.Fatal("accepted mixed auth schema")
	}
	var markers int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name='linguaflow_auth_format'").Scan(&markers); err != nil || markers != 0 {
		t.Fatal("probe reserved a new auth marker")
	}
}

func TestProbeFingerprintTracksDataAndOriginalLocalIdentity(t *testing.T) {
	ctx := context.Background()
	db, _, _ := releaseFixture(t, "local")
	a, err := Probe(ctx, db, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if a.Mode != "local" {
		t.Fatal(a)
	}
	if _, err := db.Exec("UPDATE users SET display_name='changed'"); err != nil {
		t.Fatal(err)
	}
	b, err := Probe(ctx, db, "sqlite")
	if err != nil || a.Fingerprint == b.Fingerprint {
		t.Fatal("fingerprint missed business change")
	}
	if _, err := db.Exec("UPDATE instance_initializations SET local_user_id=88"); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(ctx, db, "sqlite"); err == nil {
		t.Fatal("accepted fabricated local identity")
	}
}

func TestProbeFingerprintBindsOriginalSQLiteAllocationSequence(t *testing.T) {
	db, path, _ := releaseFixture(t, "serve")
	ctx := t.Context()
	before, err := Probe(ctx, db, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE sqlite_sequence SET seq=seq+100 WHERE name='users'"); err != nil {
		t.Fatal(err)
	}
	proof, err := operation.Hash(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Probe(ctx, db, "sqlite")
	if err != nil || before.Fingerprint == after.Fingerprint {
		t.Fatal("source sequence change escaped the bound fingerprint", err)
	}
	readProof, err := operation.Hash(ctx, path)
	if err != nil || proof != readProof {
		t.Fatal("sequence inspection changed source bytes", err)
	}
}

func TestProbeRejectsUnexpectedDefaultBeforePreparingTarget(t *testing.T) {
	ctx := t.Context()
	db, path, _ := releaseFixture(t, "serve")
	// sqlite_schema writable_schema is confined to this disposable fixture; the
	// source can look structurally identical while changing the allocation rule.
	if _, err := db.ExecContext(ctx, "PRAGMA writable_schema=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE sqlite_schema SET sql=replace(sql,'`display_name` text NULL','`display_name` text NULL DEFAULT ''invented''') WHERE type='table' AND name='users'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA writable_schema=OFF; PRAGMA schema_version=123"); err != nil {
		t.Fatal(err)
	}
	before, err := operation.Hash(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(ctx, db, "sqlite"); err == nil {
		t.Fatal("unexpected default acquired release semantics")
	}
	after, err := operation.Hash(ctx, path)
	if err != nil || before != after {
		t.Fatal("source default rejection wrote the source", err)
	}
}

func TestProbeRejectsWeakenedReleasedCheckWithoutWrites(t *testing.T) {
	ctx := t.Context()
	db, path, _ := releaseFixture(t, "serve")
	if _, err := db.ExecContext(ctx, "PRAGMA writable_schema=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE sqlite_schema SET sql=replace(sql,'source_generation >= 0 AND source_generation <= 9007199254740991','source_generation >= 0 AND source_generation <= 9007199254740991 OR 1=1') WHERE name='resources'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA writable_schema=OFF; PRAGMA schema_version=125"); err != nil {
		t.Fatal(err)
	}
	before, err := operation.Hash(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(ctx, db, "sqlite"); err == nil {
		t.Fatal("released check was accepted after its bounds were bypassed")
	}
	after, err := operation.Hash(ctx, path)
	if err != nil || before != after {
		t.Fatal("check rejection wrote source bytes", err)
	}
}

func TestPostgresReleasedCheckDecompilationPreservesLiteralsAndBounds(t *testing.T) {
	id := &schema.Column{Name: "id", Type: field.TypeInt64}
	want := []*schema.Table{{Name: "fixture", Columns: []*schema.Column{id}, Annotation: &entsql.Annotation{Checks: map[string]string{"safe": "id >= 0 AND id <= 9007199254740991 AND label <> 'Keep Case'"}}}}
	got := []actualTable{{Name: "fixture", Columns: []actualColumn{{Name: "id", Type: "bigint"}}, Checks: []string{"((id >= 0) AND (id <= '9007199254740991'::bigint) AND ((label)::text <> 'Keep Case'::text))"}}}
	if err := matchTables(got, want, "postgres"); err != nil {
		t.Fatal("legal PostgreSQL decompilation lost the fixed check contract", err)
	}
	for _, changed := range []string{
		"id >= 0 AND id <= 9007199254740991 AND label <> 'keep case'",
		"id >= 0 AND id <= 9007199254740991 AND label <> 'Keep Case' OR true",
		"id >= 0 AND id <= 9007199254740992 AND label <> 'Keep Case'",
	} {
		got[0].Checks = []string{changed}
		if err := matchTables(got, want, "postgres"); err == nil {
			t.Fatal("changed PostgreSQL check acquired the released contract", changed)
		}
	}
}
