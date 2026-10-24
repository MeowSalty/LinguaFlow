package operation

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	_ "modernc.org/sqlite"
)

func TestStepReceiptSharesCommitAndRecoversLaggingJournal(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "receipt.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, _ := NewID()
	p := &Plan{ID: id, Version: Version, Strategy: Inplace, DatabaseBackup: BackupAuto, Kind: "storage", Build: "test-build", Source: Endpoint{Driver: "sqlite"}, TargetSignature: Digest("target")}
	bindTestBaseline(p)
	if err := Initialize(ctx, db, "sqlite", p); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE example (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	fault := errors.New("injected before receipt")
	err = Step(ctx, db, "sqlite", p, "storage", func(c *ent.Client) error {
		if _, err := c.ExecContext(ctx, "INSERT INTO example VALUES (1,42)"); err != nil {
			return err
		}
		return fault
	})
	if !errors.Is(err, fault) {
		t.Fatalf("fault: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM example").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial commit: %d %v", count, err)
	}
	steps, state, err := Steps(ctx, db, p)
	if err != nil || len(steps) != 1 || steps[0] != "preparing" || state != "preparing" {
		t.Fatalf("partial receipt: %v %s %v", steps, state, err)
	}
	for range 2 {
		if err := Step(ctx, db, "sqlite", p, "storage", func(c *ent.Client) error {
			_, err := c.ExecContext(ctx, "INSERT INTO example VALUES (1,42)")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	steps, _, err = Steps(ctx, db, p)
	if err != nil || len(steps) != 2 || steps[1] != "storage" {
		t.Fatalf("committed receipt: %v %v", steps, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM example").Scan(&count); err != nil || count != 1 {
		t.Fatalf("replayed commit: %d %v", count, err)
	}
	changed := *p
	changed.Build = "other-build"
	if _, _, err := Steps(ctx, db, &changed); err == nil {
		t.Fatal("accepted changed plan")
	}
	if err := StepWithResult(ctx, db, "sqlite", p, "ready", func(c *ent.Client) (string, error) { return Digest("verified example"), MarkReady(ctx, c, p) }); err != nil {
		t.Fatal(err)
	}
	if err := Step(ctx, db, "sqlite", p, "history", func(*ent.Client) error { return nil }); err == nil {
		t.Fatal("changed ready target")
	}
}

func TestSchemaStepRejectsBrokenReferencesAndRestoresForeignKeys(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fk.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	id, _ := NewID()
	p := &Plan{ID: id, Version: Version, Kind: "upgrade", Source: Endpoint{Driver: "sqlite"}, TargetSignature: Digest("schema"), Build: "test"}
	bindTestBaseline(p)
	if err := Initialize(ctx, db, "sqlite", p); err != nil {
		t.Fatal(err)
	}
	err = Step(ctx, db, "sqlite", p, "schema", func(c *ent.Client) error {
		for _, s := range []string{"CREATE TABLE parent(id INTEGER PRIMARY KEY)", "CREATE TABLE child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES parent(id))", "INSERT INTO child VALUES(1,99)"} {
			if _, err := c.ExecContext(ctx, s); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		t.Fatal("accepted broken foreign key")
	}
	var enabled int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys=%d: %v", enabled, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name='child'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("DDL escaped rollback: %d %v", count, err)
	}
}

func bindTestBaseline(p *Plan) {
	p.Baseline = &Baseline{Version: Version, ID: p.ID, PlanDigest: Digest(p), SourceFingerprint: p.SourceFingerprint, SourceSchema: p.SourceSchema, SourceIdentity: p.Source.Identity, InputDigest: p.Source.InputDigest, ConversionTime: time.Now().UTC(), Tables: []TableBaseline{{Name: "example", Columns: []string{"id", "value"}, PrimaryKey: []string{"id"}, Keys: Digest("keys"), Preserved: Digest("preserved")}}}
}

func TestMissingOrTamperedPreparingReceiptCannotBeRecreated(t *testing.T) {
	ctx := context.Background()
	for _, change := range []string{"DELETE FROM linguaflow_migration_steps WHERE step='preparing'", "UPDATE linguaflow_migration_steps SET baseline_digest=NULL", "INSERT INTO linguaflow_migration_steps SELECT operation_id,'future_step',plan_digest,target_signature,build,committed_at,baseline_digest,result_digest FROM linguaflow_migration_steps"} {
		t.Run(change, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "receipt.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			id, _ := NewID()
			p := &Plan{Version: Version, ID: id, Kind: "storage", Build: "test", Source: Endpoint{Driver: "sqlite"}, TargetSignature: Digest("target")}
			bindTestBaseline(p)
			if err := ReserveStorage(ctx, db, p); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, change); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Steps(ctx, db, p); err == nil {
				t.Fatal("accepted missing or unknown receipt")
			}
			if change != "INSERT INTO linguaflow_migration_steps SELECT operation_id,'future_step',plan_digest,target_signature,build,committed_at,baseline_digest,result_digest FROM linguaflow_migration_steps" {
				if err := ReserveStorage(ctx, db, p); err == nil {
					t.Fatal("recreated original preparing receipt")
				}
			}
		})
	}
}
