package workstate

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestPersistenceRuntimeInventoryBoundedByteState(t *testing.T) {
	driver := dialect.SQLite
	if os.Getenv("LINGUAFLOW_PERSISTENCE_DRIVER") == dialect.Postgres {
		driver = dialect.Postgres
		if os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN") == "" {
			t.Fatal("runtime inventory requires its isolated PostgreSQL test DSN")
		}
	}
	client, _ := independentClients(t, driver)
	ctx, _, store, scope, row := seedFixture(t, t.Context(), client)
	candidate := ready(scope, row)
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	source := InventorySource{DB: client, Driver: driver, ItemBytes: 1024}
	ownedState := `{"version":1,"progress_id":"candidate-1"}`
	for _, tc := range []struct {
		name                  string
		state                 []byte
		reserved              bool
		attempts, netAttempts int
		grows                 bool
	}{
		{name: "sql-null"},
		{name: "empty-bytes", state: []byte{}},
		{name: "no-progress", state: []byte(`{"version":1}`)},
		{name: "foreign-progress", state: []byte(`{"version":1,"progress_id":"foreign"}`)},
		{name: "owned-progress", state: []byte(`{"version":1,"progress_id":"candidate-1"}`), grows: true},
		{name: "byte-limit", state: []byte(ownedState + strings.Repeat(" ", RuntimeStateLimit-len(ownedState))), grows: true},
		{name: "reserved", reserved: true, grows: true},
		{name: "alignment-attempt", attempts: 1, grows: true},
		{name: "network-attempt", netAttempts: 1, grows: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			update := client.Job.UpdateOneID(scope.JobID)
			if tc.state == nil {
				update.ClearPipelineRuntimeState()
			} else {
				update.SetPipelineRuntimeState(tc.state)
			}
			update.ExecX(ctx)
			client.WorkCandidate.Update().Where(workcandidate.IdentityEQ(candidate.ID)).SetGrowthReserved(tc.reserved).ExecX(ctx)
			client.WorkItem.Update().Where(workitem.JobIDEQ(scope.JobID)).SetAlignmentAttempts(tc.attempts).SetAlignmentNetworkAttempts(tc.netAttempts).ExecX(ctx)
			members, bytes, growth, handoff, err := source.Runtime(ctx, scope.JobID)
			wantGrowth := int64(0)
			if tc.grows {
				wantGrowth = 1024 - int64(len(candidate.Payload))
			}
			if err != nil || members != 1 || bytes != int64(len(candidate.Payload)) || growth != wantGrowth || handoff != 0 {
				t.Fatalf("runtime inventory members=%d bytes=%d growth=%d handoff=%d err=%v; growth want%d", members, bytes, growth, handoff, err, wantGrowth)
			}
		})
	}
	client.Job.UpdateOneID(scope.JobID).SetPipelineRuntimeState([]byte(strings.Repeat("x", RuntimeStateLimit+1))).ExecX(ctx)
	if _, _, _, _, err := source.Runtime(ctx, scope.JobID); !errors.Is(err, ErrManifest) {
		t.Fatalf("oversized state must be rejected before JSON parsing: %v", err)
	}
}

func TestPersistenceRuntimeInventoryMissingJob(t *testing.T) {
	for _, retainBacking := range []bool{false, true} {
		name := "cascade-cleanup"
		if retainBacking {
			name = "remaining-backing"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "missing-job.db")+"?_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
			t.Cleanup(func() { _ = client.Close() })
			if err := client.Schema.Create(ctx); err != nil {
				t.Fatal(err)
			}
			_, _, store, scope, row := seedFixture(t, ctx, client)
			candidate := ready(scope, row)
			if err := store.SaveCandidate(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			client.WorkCandidate.Update().Where(workcandidate.IdentityEQ(candidate.ID)).SetGrowthReserved(true).ExecX(ctx)
			source := InventorySource{DB: client, Driver: dialect.SQLite, ItemBytes: 1024}
			before, storageBefore, err := source.Job(ctx, scope.JobID)
			if err != nil || before.UnconfirmedMembers != 1 || before.PayloadBytes == 0 || storageBefore == 0 {
				t.Fatalf("missing initial backing: inventory=%+v storage=%d err=%v", before, storageBefore, err)
			}
			if retainBacking {
				// This isolated connection keeps orphaned backing to verify that
				// a missing job never short-circuits the remaining aggregates.
				if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
					t.Fatal(err)
				}
			} else if err := client.JobResource.DeleteOneID(scope.JobResourceID).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			if err := client.Job.DeleteOneID(scope.JobID).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			got, storage, err := source.Job(ctx, scope.JobID)
			want, wantStorage := resourcebudget.Vector{}, int64(0)
			if retainBacking {
				want, wantStorage = before, storageBefore
			}
			if err != nil || got != want || storage != wantStorage {
				t.Fatalf("inventory after job deletion=%+v storage=%d err=%v; want=%+v storage=%d", got, storage, err, want, wantStorage)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			_, queryErr := db.QueryContext(ctx, "SELECT 1")
			if _, _, err := source.Job(ctx, scope.JobID); queryErr == nil || !errors.Is(err, queryErr) {
				t.Fatalf("closed database failure was hidden: inventory=%v database=%v", err, queryErr)
			}
		})
	}
}
