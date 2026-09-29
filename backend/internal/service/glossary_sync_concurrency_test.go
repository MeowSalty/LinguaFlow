package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestGlossarySyncSQLiteIndependentTransactionCompetition(t *testing.T) {
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "sync.db")) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)"
	open := func() *ent.Client {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(2)
		client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	first, second := open(), open()
	if err := first.Schema.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	runSyncTransactionCompetition(t, first, second)
}

func TestGlossarySyncPostgresIndependentTransactionCompetition(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg := config.DefaultServerConfig()
	cfg.Database = config.DatabaseConfig{Driver: config.DatabaseDriverPostgres, DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 2}
	db, first, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	_, second, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	unlock, err := database.AcquireMigrationLock(ctx, db, config.DatabaseDriverPostgres)
	if err != nil {
		t.Fatal(err)
	}
	err = first.Schema.Create(ctx)
	unlockErr := unlock()
	if err != nil {
		t.Fatal(err)
	}
	if unlockErr != nil {
		t.Fatal(unlockErr)
	}
	runSyncTransactionCompetition(t, first, second)
}

func TestGlossarySyncSQLiteSchemaUpgradePreservesHistory(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	f := seedSyncLifecycle(t, client, 1)
	pending := f.submit(t)
	client.SyncTask.UpdateOneID(pending.ID).SetResult("").ExecX(ctx)
	history := f.submit(t)
	history = client.SyncTask.UpdateOneID(history.ID).SetStatus(SyncTaskStatusCompleted).SaveX(ctx)
	// Recreate the pre-P4 shape while retaining real historical rows. Running
	// schema migration afterwards must add fields without replacing their data.
	for _, statement := range []string{
		"ALTER TABLE sync_tasks DROP COLUMN started_at",
		"ALTER TABLE sync_tasks DROP COLUMN checkpoint_version",
		"ALTER TABLE sync_tasks DROP COLUMN next_segment_index",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := client.Schema.Create(ctx); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.PrepareRecovery(ctx); err != nil {
			t.Fatal(err)
		}
	}
	row := client.SyncTask.GetX(ctx, pending.ID)
	if row.Status != SyncTaskStatusPending || row.CheckpointVersion != 1 || row.StartedAt != nil || !row.CreatedAt.Equal(pending.CreatedAt) {
		t.Fatalf("pending migration=%+v", row)
	}
	row = client.SyncTask.GetX(ctx, history.ID)
	if row.Status != SyncTaskStatusCompleted || row.Result != history.Result || row.StartedAt != nil || !row.UpdatedAt.Equal(history.UpdatedAt) {
		t.Fatalf("history migration=%+v", row)
	}
}

func runSyncTransactionCompetition(t *testing.T, first, second *ent.Client) {
	t.Helper()
	for _, scenario := range []string{"completion_wins", "batch_wins", "cancellation_wins"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			count := 1
			if scenario == "batch_wins" {
				count = 101
			}
			f := seedSyncLifecycle(t, first, count)
			t.Cleanup(func() {
				_, _ = f.svc.projects.DeleteProject(context.Background(), f.owner, f.projectID)
				_ = first.User.DeleteOneID(f.owner).Exec(context.Background())
			})
			task := f.submit(t)
			first.SyncTask.UpdateOneID(task.ID).SetStatus(SyncTaskStatusRunning).SetStartedAt(time.Now().UTC()).ExecX(ctx)
			other := NewGlossarySyncService(second, nil, nil, nil, discardLogger())
			entered, attempted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var enterOnce, attemptOnce, releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			gate := func(ctx context.Context) error {
				enterOnce.Do(func() { close(entered) })
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if scenario == "cancellation_wins" {
				first.SyncTask.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
						m := mutation.(*ent.SyncTaskMutation)
						status, set := m.Status()
						result, err := next.Mutate(ctx, mutation)
						if err == nil && set && status == SyncTaskStatusCancelled {
							err = gate(ctx)
						}
						return result, err
					})
				})
				second.SyncTask.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
						attemptOnce.Do(func() { close(attempted) })
						return next.Mutate(ctx, mutation)
					})
				})
			} else {
				first.Segment.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
						if mutation.Op() == ent.OpUpdate {
							if err := gate(ctx); err != nil {
								return nil, err
							}
						}
						return next.Mutate(ctx, mutation)
					})
				})
				second.Project.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
						attemptOnce.Do(func() { close(attempted) })
						return next.Mutate(ctx, mutation)
					})
				})
			}
			batchResult, cancelResult := make(chan error, 1), make(chan error, 1)
			runBatch := func(svc *GlossarySyncService) { _, err := svc.executeSyncBatch(ctx, task.ID); batchResult <- err }
			runCancel := func(svc *GlossarySyncService) {
				_, err := svc.CancelSyncTask(ctx, f.owner, f.projectID, task.ID)
				cancelResult <- err
			}
			if scenario == "cancellation_wins" {
				go runCancel(f.svc)
			} else {
				go runBatch(f.svc)
			}
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first transaction did not reach gate")
			}
			if scenario == "cancellation_wins" {
				go runBatch(other)
			} else {
				go runCancel(other)
			}
			select {
			case <-attempted:
			case <-ctx.Done():
				t.Fatal("second connection did not attempt write")
			}
			if scenario == "cancellation_wins" {
				select {
				case err := <-batchResult:
					t.Fatalf("batch escaped cancellation transaction: %v", err)
				default:
				}
			} else {
				select {
				case err := <-cancelResult:
					t.Fatalf("cancel escaped batch transaction: %v", err)
				default:
				}
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-batchResult:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case err := <-cancelResult:
				if scenario == "completion_wins" {
					if !errors.Is(err, ErrSyncTaskStateConflict) {
						t.Fatalf("cancel=%v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			row := first.SyncTask.GetX(ctx, task.ID)
			wantStatus, wantProcessed := SyncTaskStatusCancelled, 0
			if scenario == "completion_wins" {
				wantStatus, wantProcessed = SyncTaskStatusCompleted, 1
			}
			if scenario == "batch_wins" {
				wantProcessed = 100
			}
			if row.Status != wantStatus || row.ProcessedSegments != wantProcessed || row.NextSegmentIndex != wantProcessed {
				t.Fatalf("row=%+v", row)
			}
			for i, id := range f.segmentIDs {
				want := "旧"
				if i < wantProcessed {
					want = "旧新"
				}
				if got := *first.Segment.GetX(ctx, id).TargetText; got != want {
					t.Fatalf("segment %d=%q want=%q", i, got, want)
				}
			}
		})
	}
}
