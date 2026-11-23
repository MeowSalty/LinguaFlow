package tm

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/tmentry"
	pa "github.com/MeowSalty/LinguaFlow/backend/internal/persistenceacceptance"
)

// Capture driver calls, including the raw ExecContext used by Add. Arguments
// are deliberately omitted. The shared acceptance fixture owns the pool and
// its fresh SQLite file or runner-supplied PostgreSQL schema and cleanup.
type tmSQLTrace struct {
	mu                         sync.Mutex
	statements                 []string
	begins, commits, rollbacks int
	isolation                  stdsql.IsolationLevel
}

func (p *tmSQLTrace) record(q string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statements = append(p.statements, q)
}

func (p *tmSQLTrace) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statements = nil
	p.begins, p.commits, p.rollbacks = 0, 0, 0
}

type tmObservedDriver struct {
	dialect.Driver
	trace *tmSQLTrace
}

func (d *tmObservedDriver) Exec(ctx context.Context, q string, a, v any) error {
	d.trace.record(q)
	return d.Driver.Exec(ctx, q, a, v)
}

func (d *tmObservedDriver) Query(ctx context.Context, q string, a, v any) error {
	d.trace.record(q)
	return d.Driver.Query(ctx, q, a, v)
}

func (d *tmObservedDriver) ExecContext(ctx context.Context, q string, a ...any) (stdsql.Result, error) {
	d.trace.record(q)
	return d.Driver.(interface {
		ExecContext(context.Context, string, ...any) (stdsql.Result, error)
	}).ExecContext(ctx, q, a...)
}

func (d *tmObservedDriver) BeginTx(ctx context.Context, opts *stdsql.TxOptions) (dialect.Tx, error) {
	tx, err := d.Driver.(interface {
		BeginTx(context.Context, *stdsql.TxOptions) (dialect.Tx, error)
	}).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	d.trace.mu.Lock()
	d.trace.begins++
	if opts != nil {
		d.trace.isolation = opts.Isolation
	}
	d.trace.mu.Unlock()
	return &tmObservedTx{Tx: tx, trace: d.trace}, nil
}

type tmObservedTx struct {
	dialect.Tx
	trace *tmSQLTrace
}

func (t *tmObservedTx) Exec(ctx context.Context, q string, a, v any) error {
	t.trace.record(q)
	return t.Tx.Exec(ctx, q, a, v)
}

func (t *tmObservedTx) Query(ctx context.Context, q string, a, v any) error {
	t.trace.record(q)
	return t.Tx.Query(ctx, q, a, v)
}

func (t *tmObservedTx) Commit() error {
	err := t.Tx.Commit()
	if err == nil {
		t.trace.mu.Lock()
		t.trace.commits++
		t.trace.mu.Unlock()
	}
	return err
}

func (t *tmObservedTx) Rollback() error {
	err := t.Tx.Rollback()
	if err == nil {
		t.trace.mu.Lock()
		t.trace.rollbacks++
		t.trace.mu.Unlock()
	}
	return err
}

func newObservedTM(t *testing.T) (*SQLite, *ent.Client, *tmSQLTrace) {
	t.Helper()
	fixture, db, _ := pa.Open(t)
	project := fixture.Project.Create().SetName("tm sql fixture").SaveX(t.Context())
	trace := &tmSQLTrace{}
	// The fixture owns this same database pool. This view adds no connection
	// pool, database, hook, service, or independent cleanup lifecycle.
	client := ent.NewClient(ent.Driver(&tmObservedDriver{Driver: database.NewDriver(entsql.OpenDB(pa.Driver(), db)), trace: trace}))
	m, err := NewSQLite(client, Scope{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	return m, client, trace
}

func assertTMSQLCount(t *testing.T, trace *tmSQLTrace, want int) []string {
	t.Helper()
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if len(trace.statements) != want {
		t.Fatalf("driver statements=%d want=%d", len(trace.statements), want)
	}
	t.Logf("driver=%s actual_sql=%d begins=%d commits=%d rollbacks=%d", pa.Driver(), len(trace.statements), trace.begins, trace.commits, trace.rollbacks)
	return append([]string(nil), trace.statements...)
}

func TestTMDatabaseBoundedSQLAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		sql                                  int
		yields                               int
		limit                                int64
		miss, refuse, failYield, cancelYield bool
	}{
		{name: "hit", sql: 3, yields: 1, limit: 1 << 20},
		{name: "miss", sql: 1, limit: 1 << 20, miss: true},
		{name: "oversize", sql: 1, limit: 4096},
		{name: "credit_refused", sql: 1, limit: 1 << 20, refuse: true},
		{name: "yield_failed", sql: 2, yields: 1, limit: 1 << 20, failYield: true},
		{name: "yield_cancelled", sql: 2, yields: 1, limit: 1 << 20, cancelYield: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, client, trace := newObservedTM(t)
			target := "中文 target"
			if tc.name == "oversize" {
				target = strings.Repeat("译", 32<<10)
			}
			if err := m.Add(t.Context(), "source", target, "en", "zh"); err != nil {
				t.Fatal(err)
			}
			before := client.TMEntry.Query().OnlyX(t.Context())
			old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			client.TMEntry.UpdateOneID(before.ID).SetUsageCount(7).SetUpdatedAt(old).SaveX(t.Context())
			trace.reset()
			b, err := backend.NewBuildBounds(tc.limit, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.refuse {
				ctx = WithInputReadCredit(ctx, func(n int64) error {
					if n != int64(len("source")+len(target)) {
						t.Fatalf("byte credit=%d", n)
					}
					return backend.ErrBuildCapacity
				})
			}
			source := " source "
			if tc.miss {
				source = "missing"
			}
			yields := 0
			wantYieldErr := errors.New("visitor failed")
			err = m.VisitMatchesBounded(ctx, source, "en", "zh", b, nil, func(match Match) error {
				yields++
				if match.Source != "source" || match.Target != target || match.Score != 1 {
					t.Fatalf("match=%v", match)
				}
				if tc.failYield {
					return wantYieldErr
				}
				if tc.cancelYield {
					cancel()
					return ctx.Err()
				}
				return nil
			})
			if tc.name == "oversize" || tc.refuse {
				if !errors.Is(err, backend.ErrBuildCapacity) {
					t.Fatalf("err=%v", err)
				}
			} else if tc.failYield {
				if !errors.Is(err, wantYieldErr) {
					t.Fatalf("err=%v", err)
				}
			} else if tc.cancelYield {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if yields != tc.yields {
				t.Fatalf("yields=%d want=%d", yields, tc.yields)
			}
			queries := assertTMSQLCount(t, trace, tc.sql)
			if !strings.Contains(queries[0], "source_bytes") || !strings.Contains(queries[0], "target_bytes") || strings.Contains(queries[0], "COUNT(") {
				t.Fatalf("invalid header projection: %s", queries[0])
			}
			if tc.sql >= 2 {
				if !strings.Contains(queries[1], "source_text") || !strings.Contains(queries[1], "target_text") || strings.Contains(queries[1], "usage_count") || strings.Contains(queries[1], "source_hash") {
					t.Fatalf("invalid body projection: %s", queries[1])
				}
			}
			if tc.sql == 3 && (!strings.HasPrefix(queries[2], "UPDATE") || !strings.Contains(queries[2], "updated_at")) {
				t.Fatalf("usage SQL=%s", queries[2])
			}
			if trace.isolation != stdsql.LevelRepeatableRead {
				t.Fatalf("isolation=%v", trace.isolation)
			}
			after := client.TMEntry.GetX(t.Context(), before.ID)
			wantUsage := 7
			if tc.name == "hit" {
				wantUsage++
			}
			if after.UsageCount != wantUsage {
				t.Fatalf("usage=%d want=%d", after.UsageCount, wantUsage)
			}
			if tc.name == "hit" {
				if !after.UpdatedAt.After(old) {
					t.Fatal("successful usage did not update updated_at")
				}
			} else if !after.UpdatedAt.Equal(old) {
				t.Fatal("failed/missing visit changed updated_at")
			}
		})
	}
}

func TestTMDatabaseUpsertPreservesIdentityAndNoop(t *testing.T) {
	m, client, trace := newObservedTM(t)
	if err := m.Add(t.Context(), " source ", " target ", " en ", " zh "); err != nil {
		t.Fatal(err)
	}
	queries := assertTMSQLCount(t, trace, 1)
	if !strings.HasPrefix(queries[0], "INSERT") || !strings.Contains(queries[0], "ON CONFLICT") {
		t.Fatalf("upsert SQL=%s", queries[0])
	}
	row := client.TMEntry.Query().OnlyX(t.Context())
	if row.SourceText != "source" || row.TargetText != "target" || row.UsageCount != 0 || row.ProjectID == nil || *row.ProjectID != m.scope.ProjectID {
		t.Fatalf("new entry=%+v", row)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	client.TMEntry.UpdateOneID(row.ID).SetUsageCount(4).SetUpdatedAt(old).SaveX(t.Context())
	trace.reset()
	if err := m.Add(t.Context(), "source", "target", "en", "zh"); err != nil {
		t.Fatal(err)
	}
	assertTMSQLCount(t, trace, 1)
	noop := client.TMEntry.GetX(t.Context(), row.ID)
	if noop.UsageCount != 4 || !noop.UpdatedAt.Equal(old) || !noop.CreatedAt.Equal(row.CreatedAt) {
		t.Fatalf("duplicate mutated entry=%+v", noop)
	}
	trace.reset()
	if err := m.Add(t.Context(), "source", "changed", "en", "zh"); err != nil {
		t.Fatal(err)
	}
	assertTMSQLCount(t, trace, 1)
	changed := client.TMEntry.Query().OnlyX(t.Context())
	if changed.ID != row.ID || changed.TargetText != "changed" || changed.UsageCount != 4 || !changed.CreatedAt.Equal(row.CreatedAt) || !changed.UpdatedAt.After(old) {
		t.Fatalf("changed entry=%+v", changed)
	}
	// An inconsistent source text with the same hash must also be repaired.
	client.TMEntry.UpdateOneID(row.ID).SetSourceText("different stored source").SetUpdatedAt(old).SaveX(t.Context())
	if err := m.Add(t.Context(), "source", "changed", "en", "zh"); err != nil {
		t.Fatal(err)
	}
	if repaired := client.TMEntry.GetX(t.Context(), row.ID); repaired.SourceText != "source" || !repaired.UpdatedAt.After(old) {
		t.Fatalf("source repair=%+v", repaired)
	}
	otherProject := client.Project.Create().SetName("other tm project").SaveX(t.Context())
	other, _ := NewSQLite(client, Scope{ProjectID: otherProject.ID})
	if err := other.Add(t.Context(), "source", "other", "en", "zh"); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(t.Context(), "source", "language", "en", "ja"); err != nil {
		t.Fatal(err)
	}
	if count := client.TMEntry.Query().CountX(t.Context()); count != 3 {
		t.Fatalf("scoped/language entries=%d", count)
	}
	matches, err := m.Search(t.Context(), " source ", "en", "zh")
	if err != nil || len(matches) != 1 || matches[0].Target != "changed" {
		t.Fatalf("search=%v err=%v", matches, err)
	}
}

func TestTMDatabaseConcurrentUpsertAndUsage(t *testing.T) {
	m, client, trace := newObservedTM(t)
	if err := m.Add(t.Context(), "source", "initial", "en", "zh"); err != nil {
		t.Fatal(err)
	}
	id := client.TMEntry.Query().OnlyX(t.Context()).ID
	const writers = 12
	start := make(chan struct{})
	errs := make(chan error, writers*2)
	var wg sync.WaitGroup
	trace.reset()
	for i := range writers {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- m.Add(t.Context(), "source", fmt.Sprintf("target %d", i), "en", "zh")
		}(i)
		go func() {
			defer wg.Done()
			<-start
			_, err := client.TMEntry.Update().Where(tmentry.IDEQ(id)).AddUsageCount(1).Save(t.Context())
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertTMSQLCount(t, trace, writers*2)
	row := client.TMEntry.Query().OnlyX(t.Context())
	if row.ID != id || row.UsageCount != writers || !strings.HasPrefix(row.TargetText, "target ") {
		t.Fatalf("concurrent entry=%+v", row)
	}
	if err := m.Add(t.Context(), "source", "last successful write", "en", "zh"); err != nil {
		t.Fatal(err)
	}
	row = client.TMEntry.Query().OnlyX(t.Context())
	if row.TargetText != "last successful write" || row.UsageCount != writers {
		t.Fatalf("last-write entry=%+v", row)
	}
	before := row.UpdatedAt
	start = make(chan struct{})
	errs = make(chan error, writers)
	trace.reset()
	for range writers {
		wg.Go(func() { <-start; errs <- m.Add(t.Context(), "source", "last successful write", "en", "zh") })
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertTMSQLCount(t, trace, writers)
	row = client.TMEntry.Query().OnlyX(t.Context())
	if row.UsageCount != writers || !row.UpdatedAt.Equal(before) {
		t.Fatalf("duplicate writers mutated entry=%+v", row)
	}
}

func TestTMDatabaseBoundedSnapshotRejectsConcurrentReplacement(t *testing.T) {
	m, client, _ := newObservedTM(t)
	if err := m.Add(t.Context(), "source", "old target", "en", "zh"); err != nil {
		t.Fatal(err)
	}
	row := client.TMEntry.Query().OnlyX(t.Context())
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	ctx := WithInputReadCredit(t.Context(), func(int64) error {
		// Another connection changes the row between header and body. Both
		// WAL SQLite and repeatable-read PostgreSQL retain the old read view,
		// then reject the stale usage write instead of losing the replacement.
		return m.Add(t.Context(), "source", "new target", "en", "zh")
	})
	yields := 0
	err := m.VisitMatchesBounded(ctx, "source", "en", "zh", b, nil, func(match Match) error {
		yields++
		if match.Target != "old target" {
			t.Fatalf("snapshot target=%q", match.Target)
		}
		return nil
	})
	if err == nil || yields != 1 {
		t.Fatalf("err=%v yields=%d", err, yields)
	}
	row = client.TMEntry.GetX(t.Context(), row.ID)
	if row.TargetText != "new target" || row.UsageCount != 0 {
		t.Fatalf("concurrent replacement=%+v", row)
	}
}

func TestTMDatabaseAddCancellationAndConstraintFailure(t *testing.T) {
	m, client, trace := newObservedTM(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.Add(ctx, "source", "target", "en", "zh"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled add=%v", err)
	}
	trace.reset()
	if err := m.Add(t.Context(), " ", "target", "en", "zh"); err != nil {
		t.Fatal(err)
	}
	assertTMSQLCount(t, trace, 0)
	missing, err := NewSQLite(client, Scope{ProjectID: 2147483647})
	if err != nil {
		t.Fatal(err)
	}
	if err := missing.Add(t.Context(), "source", "target", "en", "zh"); err == nil {
		t.Fatal("foreign-key failure was suppressed")
	}
	if count := client.TMEntry.Query().CountX(t.Context()); count != 0 {
		t.Fatalf("failed adds created %d entries", count)
	}
	if _, err := NewSQLite(client, Scope{ProjectID: -1}); err == nil {
		t.Fatal("negative project scope accepted")
	}
}

func TestTMDatabaseUnavailableStorage(t *testing.T) {
	m, client, _ := newObservedTM(t)
	// This fixture owns its entire pool; closing it cannot affect another test
	// or service. The acceptance helper remains responsible for cleanup.
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(t.Context(), "source", "target", "en", "zh"); err == nil {
		t.Fatal("unavailable storage was reported as a successful add")
	}
}
