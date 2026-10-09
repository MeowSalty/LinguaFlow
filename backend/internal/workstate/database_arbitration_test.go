package workstate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

// Every backend uses independent database pools. PostgreSQL is opt-in and gets
// its own temporary schema; a missing DSN is explicitly reported as unverified.
func TestIndependentDatabaseCommitArbitration(t *testing.T) {
	for _, name := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(name, func(t *testing.T) {
			left, right := independentClients(t, name)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _, store, scope, seg := seedFixture(t, ctx, left)
			candidate := ready(scope, seg)
			if err := store.SaveCandidate(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			locked, attempted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var lockOnce, attemptOnce, releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			left.Job.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
					value, err := next.Mutate(ctx, m)
					if err != nil {
						return value, err
					}
					if _, ok := m.AddedField("retry_epoch"); ok {
						lockOnce.Do(func() {
							close(locked)
							select {
							case <-release:
							case <-ctx.Done():
								err = ctx.Err()
							}
						})
					}
					return value, err
				})
			})
			right.Job.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
					if _, ok := m.AddedField("retry_epoch"); ok {
						attemptOnce.Do(func() { close(attempted) })
					}
					return next.Mutate(ctx, m)
				})
			})
			type completion struct {
				result CommitResult
				err    error
			}
			done := make(chan completion, 2)
			go func() { result, err := store.Commit(ctx, input(candidate)); done <- completion{result, err} }()
			select {
			case <-locked:
			case <-ctx.Done():
				t.Fatal("first transaction did not lock Job")
			}
			go func() { result, err := NewStore(right).Commit(ctx, input(candidate)); done <- completion{result, err} }()
			select {
			case <-attempted:
			case <-ctx.Done():
				t.Fatal("second transaction did not compete for Job")
			}
			releaseOnce.Do(func() { close(release) })
			accepted, duplicate := 0, 0
			for range 2 {
				completion := <-done
				if completion.err != nil {
					t.Fatal(completion.err)
				}
				switch completion.result.Outcome {
				case Committed:
					accepted++
				case AlreadyCommitted:
					duplicate++
				default:
					t.Fatalf("result=%+v", completion.result)
				}
			}
			if accepted != 1 || duplicate != 1 || left.Segment.GetX(ctx, seg.ID).ContentVersion != 2 || left.Job.GetX(ctx, scope.JobID).ProgressCompleted != 1 {
				t.Fatal("independent transactions duplicated acceptance")
			}
		})
	}
}

func independentClients(t *testing.T, name string) (*ent.Client, *ent.Client) {
	t.Helper()
	var open func() *sql.DB
	if name == dialect.Postgres {
		dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("PostgreSQL workstate arbitration unverified: LINGUAFLOW_TEST_POSTGRES_DSN is not set")
		}
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal("invalid PostgreSQL test DSN")
		}
		admin := stdlib.OpenDB(*cfg)
		schema := fmt.Sprintf("workstate_race_%d", time.Now().UnixNano())
		quoted := pgx.Identifier{schema}.Sanitize()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
			admin.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
				t.Errorf("remove workstate test schema: %v", err)
			}
			admin.Close()
		})
		open = func() *sql.DB {
			copy := cfg.Copy()
			copy.RuntimeParams["search_path"] = schema
			copy.RuntimeParams["timezone"] = "UTC"
			return stdlib.OpenDB(*copy)
		}
	} else {
		dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "arbitration.db")) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)"
		open = func() *sql.DB {
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			return db
		}
	}
	client := func() *ent.Client {
		db := open()
		db.SetMaxOpenConns(2)
		c := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(name, db))))
		t.Cleanup(func() { c.Close() })
		return c
	}
	left, right := client(), client()
	if err := left.Schema.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	return left, right
}
