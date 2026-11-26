package v015

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	entschema "entgo.io/ent/dialect/sql/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	source "github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015/source"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

func TestReleasedSyncPlanUpgradePreservesBaselineAndRecovery(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) { testReleasedSyncPlanUpgrade(t, driver) })
	}
}

func testReleasedSyncPlanUpgrade(t *testing.T, driver string) {
	ctx := t.Context()
	db := publishedReleaseFixture(t, driver)
	queries := []string{
		`INSERT INTO projects(id,created_at,updated_at,name,config,owner_user_id) SELECT 1,created_at,updated_at,'sync fixture','{}',1 FROM users WHERE id=1`,
		`INSERT INTO resources(id,created_at,updated_at,path,format,storage_path,project_id) SELECT 1,created_at,updated_at,'sync.txt','txt','fixture',1 FROM users WHERE id=1`,
		`INSERT INTO segments(id,created_at,updated_at,segment_index,source_text,target_text,resource_id) SELECT 1,created_at,updated_at,0,'fixture','already new',1 FROM users WHERE id=1`,
		`INSERT INTO segments(id,created_at,updated_at,segment_index,source_text,target_text,resource_id) SELECT 2,created_at,updated_at,1,'fixture','old',1 FROM users WHERE id=1`,
		`INSERT INTO glossary_entries(id,created_at,updated_at,source_key,source,target,project_id) SELECT 1,created_at,updated_at,'fixture','fixture','old',1 FROM users WHERE id=1`,
	}
	for _, query := range queries {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	stored := `{"total_updated":1,"total_skipped":0,"resources":[{"resource_id":1,"resource_path":"sync.txt","updated_count":1,"skipped_count":0}]}`
	for _, row := range []struct {
		id                  int
		status, ids, result string
		cp, next            int
	}{
		{1, "running", "[1,2,999]", stored, 1, 1},
		{2, "running", "[1,1,999]", stored, 1, 1},
		{3, "completed", "[1,2,999]", stored, 1, 1},
	} {
		_, err := db.ExecContext(ctx, `INSERT INTO sync_tasks(id,created_at,updated_at,old_target,new_target,total_segments,processed_segments,checkpoint_version,next_segment_index,status,segment_ids,resource_ids,result,project_id,entry_id,actor_user_id) SELECT $1,created_at,updated_at,'old','new',3,$2,$3,$4,$5,$6,'[1]',$7,1,1,1 FROM users WHERE id=1`, row.id, row.next, row.cp, row.next, row.status, row.ids, row.result)
		if err != nil {
			t.Fatal(err)
		}
	}
	if driver == "postgres" {
		// Explicit fixture IDs must leave ordinary allocators past MAX(id).
		// Keep the initializer's real SetID(1), uncalled singleton sequence.
		for _, table := range source.Tables {
			if table.Name == "instance_initializations" {
				continue
			}
			for _, column := range table.Columns {
				if !column.Increment {
					continue
				}
				query := "SELECT setval(pg_get_serial_sequence($1,$2),COALESCE((SELECT MAX(" + quoteIdentifier(column.Name) + ") FROM " + quoteIdentifier(table.Name) + "),1),(SELECT COUNT(*)>0 FROM " + quoteIdentifier(table.Name) + "))"
				if _, err := db.ExecContext(ctx, query, table.Name, column.Name); err != nil {
					t.Fatal("align explicit released fixture allocation", err)
				}
			}
		}
		if _, err := Probe(ctx, db, driver); err != nil {
			t.Fatal("released fixed singleton allocation", err)
		}
		if _, err := db.ExecContext(ctx, "SELECT setval(pg_get_serial_sequence('users','id'),1,false)"); err != nil {
			t.Fatal(err)
		}
		if _, err := Probe(ctx, db, driver); err == nil {
			t.Fatal("ordinary allocator accepted an already used next ID")
		}
		if _, err := db.ExecContext(ctx, "SELECT setval(pg_get_serial_sequence('users','id'),1,true)"); err != nil {
			t.Fatal(err)
		}
	}
	p := publishedReleasePlan(t, db, driver)
	for _, table := range p.Baseline.Tables {
		if table.Name == "sync_tasks" && (table.MaxID != 3 || table.Count != 3) {
			t.Fatalf("unexpected source checkpoint scope: %+v", table)
		}
	}
	if err := operation.Initialize(ctx, db, driver, p); err != nil {
		t.Fatal(err)
	}
	for _, step := range []func(context.Context, *sql.DB, *operation.Plan) error{ConvertSchema, ConvertAccounts, ConvertHistory} {
		if err := step(ctx, db, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := operation.Step(ctx, db, driver, p, "storage", func(c *ent.Client) error { return NormalizeBaselineTimestamps(ctx, c, p) }); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBaseline(ctx, db, p, false); err != nil {
		t.Fatal("converted source baseline", err)
	}
	if err := VerifyShape(ctx, db, p); err != nil {
		t.Fatal("final schema", err)
	}
	if err := ConvertSchema(ctx, db, p); err != nil {
		t.Fatal("legal schema resume", err)
	}
	if err := CheckProgress(ctx, db, p, []string{"schema", "accounts", "history", "storage"}); err != nil {
		t.Fatal("legal progress resume", err)
	}
	// The removed source vectors must not hide edits to the derived facts
	// between schema commit and ready. No new digest may endorse those edits.
	for _, tamper := range []struct{ change, restore string }{
		{"UPDATE sync_task_items SET segment_id=9988 WHERE task_id=1 AND ordinal=1", "UPDATE sync_task_items SET segment_id=2 WHERE task_id=1 AND ordinal=1"},
		{"UPDATE sync_task_items SET resource_id=9988 WHERE task_id=1 AND ordinal=1", "UPDATE sync_task_items SET resource_id=1 WHERE task_id=1 AND ordinal=1"},
		{"UPDATE sync_task_results SET updated_count=2 WHERE task_id=1 AND resource_id=1", "UPDATE sync_task_results SET updated_count=1 WHERE task_id=1 AND resource_id=1"},
		{"UPDATE sync_task_results SET resource_path='changed' WHERE task_id=1 AND resource_id=1", "UPDATE sync_task_results SET resource_path='sync.txt' WHERE task_id=1 AND resource_id=1"},
		{"UPDATE sync_tasks SET total_updated=2 WHERE id=1", "UPDATE sync_tasks SET total_updated=1 WHERE id=1"},
	} {
		change, err := db.ExecContext(ctx, tamper.change)
		if err != nil {
			t.Fatal(err)
		}
		if count, err := change.RowsAffected(); err != nil || count != 1 {
			t.Fatal("tamper did not affect the expected converted fact", tamper.change, count, err)
		}
		if err := verifyPublishedProjection(ctx, db, p); err == nil {
			t.Fatal("original projection omitted changed facts", tamper.change)
		}
		var changed *operation.Error
		if err := Complete(ctx, db, p, func(*ent.Client) error { return nil }); !errors.As(err, &changed) || changed.Code != operation.BaselineChanged {
			t.Fatal("ready did not reject changed derived facts", tamper.change, err)
		}
		if err := CheckProgress(ctx, db, p, []string{"schema", "accounts", "history", "storage"}); err == nil {
			t.Fatal("resume endorsed changed derived facts", tamper.change)
		}
		if err := ConvertSchema(ctx, db, p); err == nil {
			t.Fatal("repeated schema endorsed changed derived facts")
		}
		if _, err := db.ExecContext(ctx, tamper.restore); err != nil {
			t.Fatal(err)
		}
	}
	if err := Complete(ctx, db, p, func(*ent.Client) error { return nil }); err != nil {
		t.Fatal("ready", err)
	}
	if _, err := VerifyBaseline(ctx, db, p, true); err != nil {
		t.Fatal("ready baseline", err)
	}
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(database.DialectFor(driver), db))))
	active := client.SyncTask.GetX(ctx, 1)
	if active.CheckpointVersion != 2 || active.Status != "running" || active.NextSegmentIndex != 1 || active.TotalUpdated != 1 || active.Result != "" {
		t.Fatalf("trusted checkpoint lost: %+v", active)
	}
	failed := client.SyncTask.GetX(ctx, 2)
	if failed.Status != "failed" || failed.Result != stored || failed.FinishedAt == nil {
		t.Fatal("untrusted checkpoint replayed or content lost")
	}
	history := client.SyncTask.GetX(ctx, 3)
	if history.Status != "completed" || history.Result != stored || history.FinishedAt != nil {
		t.Fatal("terminal history rewritten")
	}
	var positions int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sync_task_items WHERE task_id=1").Scan(&positions); err != nil || positions != 3 {
		t.Fatal("deleted ordinal lost", positions, err)
	}
	// Changed translated content remains visible to the baseline even though
	// the old serialized vectors have an explicit conversion declaration.
	if _, err := db.ExecContext(ctx, "UPDATE segments SET target_text='tampered' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBaseline(ctx, db, p, true); err == nil {
		t.Fatal("plan conversion hid business tampering")
	}
	t.Log(fmt.Sprintf("published source rows=3 trusted=1 untrusted_failed=1 terminal_preserved=1 plan_positions=%d", positions))
}

// The PostgreSQL endpoint is shared, but every fixture owns its database.
// It uses the frozen released source tree, including the original account and
// serialized sync columns, rather than starting from the final generated tree.
func publishedReleaseFixture(t *testing.T, driver string) *sql.DB {
	t.Helper()
	if driver == "sqlite" {
		db, _, _ := releaseFixture(t, "serve")
		return db
	}
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	settings, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid private PostgreSQL test endpoint")
	}
	admin := stdlib.OpenDB(*settings)
	databaseName := fmt.Sprintf("published_upgrade_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(t.Context(), "CREATE DATABASE "+databaseName+" TEMPLATE template0"); err != nil {
		admin.Close()
		t.Fatal("create isolated upgrade database", err)
	}
	t.Cleanup(func() {
		_, err := admin.ExecContext(context.Background(), "DROP DATABASE "+databaseName)
		if err != nil {
			t.Error("clean isolated upgrade database", err)
		}
		admin.Close()
	})
	settings.Database = databaseName
	settings.RuntimeParams["search_path"], settings.RuntimeParams["timezone"] = "public", "UTC"
	db := stdlib.OpenDB(*settings)
	t.Cleanup(func() { db.Close() })
	m, err := entschema.NewMigrate(entsql.OpenDB("postgres", db))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Create(t.Context(), source.Tables...); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 22, 1, 2, 3, 456789000, time.UTC)
	hash, err := bcrypt.GenerateFromPassword([]byte("original-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO users(id,created_at,updated_at,username,password_hash,email,role,active) VALUES(1,$1,$1,'OriginalAdmin',$2,'Admin@EXAMPLE.invalid','admin',true)", at, string(hash)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO instance_initializations(id,created_at,updated_at,version,data_version,mode) VALUES(1,$1,$1,1,2,'serve')", at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO system_settings(created_at,updated_at,key,value) VALUES($1,$1,'registration_enabled','false')", at); err != nil {
		t.Fatal(err)
	}
	return db
}

func publishedReleasePlan(t *testing.T, db *sql.DB, driver string) *operation.Plan {
	t.Helper()
	if driver == "sqlite" {
		return fixturePlan(t, db, "serve")
	}
	users, err := ReadUsers(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := MapAccounts(users, Mapping{}, "serve", 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := operation.NewID()
	if err != nil {
		t.Fatal(err)
	}
	p := &operation.Plan{ID: id, Version: operation.Version, Strategy: operation.Clone, DatabaseBackup: operation.BackupAuto, Kind: "upgrade", Route: "upgrade-v015-auth1", Source: operation.Endpoint{Driver: driver, Mode: "serve"}, Accounts: accounts, Build: "release-test", TargetSignature: operation.Digest("fixture")}
	inspection, err := Probe(t.Context(), db, driver)
	if err != nil {
		t.Fatal(err)
	}
	p.SourceFingerprint, p.SourceSchema = inspection.Fingerprint, inspection.SchemaDigest
	p.Baseline, err = CaptureBaseline(t.Context(), db, p)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
