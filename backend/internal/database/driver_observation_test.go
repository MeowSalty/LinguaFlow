package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

func TestDriverObservationCountsRawAndFullRowsLifetime(t *testing.T) {
	for _, name := range []string{"sqlite", "postgres"} {
		t.Run(name, func(t *testing.T) {
			db, c := observedSchemaFixture(t, name)
			collector := &persistence.Collector{}
			ctx := persistence.WithObserver(persistence.WithMetadata(t.Context(), persistence.Metadata{Operation: "reserve"}), collector)
			rows, err := db.QueryContext(ctx, "SELECT 1 UNION ALL SELECT 2")
			if err != nil {
				t.Fatal(err)
			}
			if before := collector.Snapshot(); before.DriverCalls != 1 || before.Calls != 0 || before.RowSets != 0 {
				t.Fatalf("raw calls or premature rows observation: %+v", before)
			}
			if !rows.Next() {
				t.Fatal("no fixture row")
			}
			var value int
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			after := collector.Snapshot()
			if after.RowSets != 1 || after.Rows != 1 || after.RowsDuration < 20*time.Millisecond {
				t.Fatalf("rows lifetime omitted: %+v", after)
			}
			stmt, err := db.PrepareContext(ctx, "SELECT 3")
			if err != nil {
				t.Fatal(err)
			}
			if err := stmt.QueryRowContext(ctx).Scan(&value); err != nil {
				t.Fatal(err)
			}
			stmt.Close()
			if snapshot := collector.Snapshot(); snapshot.DriverCalls != 2 || snapshot.RowSets != 2 {
				t.Fatalf("prepared statement omitted: %+v", snapshot)
			}
			// Schema migration must see the native *sql.Rows concrete scanner even
			// inside a TimeDriver transaction, while the driver still owns Close.
			if err := c.Schema.Create(ctx); err != nil {
				t.Fatal("scanner transparency", err)
			}
		})
	}
}

func BenchmarkDriverObservation(b *testing.B) {
	for _, mode := range []string{"native", "wrapped", "collected", "native_deadline", "wrapped_deadline", "collected_deadline"} {
		b.Run(mode, func(b *testing.B) {
			limited := strings.HasSuffix(mode, "_deadline")
			// testing runs this closure first with N=1, then with the requested
			// iteration count. Keep its captured mode unchanged across runs.
			baseMode := strings.TrimSuffix(mode, "_deadline")
			const dsn = ":memory:?_pragma=busy_timeout(5000)"
			registered, err := sql.Open("sqlite", dsn)
			if err != nil {
				b.Fatal(err)
			}
			db := registered
			if baseMode != "native" {
				drv := registered.Driver()
				registered.Close()
				db = sql.OpenDB(ObserveConnector(registeredConnector{driver: drv, dsn: dsn}, dialect.SQLite))
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			ctx := context.Background()
			if err := db.PingContext(ctx); err != nil {
				b.Fatal(err)
			}
			var collector *persistence.Collector
			if baseMode == "collected" {
				collector = &persistence.Collector{}
				ctx = persistence.WithObserver(persistence.WithMetadata(ctx, persistence.Metadata{Operation: "reserve"}), collector)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				queryCtx := ctx
				var cancel context.CancelFunc
				if limited {
					queryCtx, cancel = context.WithTimeout(ctx, time.Second)
				}
				var value int
				err := db.QueryRowContext(queryCtx, "SELECT 1").Scan(&value)
				if cancel != nil {
					cancel()
				}
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			if collector != nil {
				snapshot := collector.Snapshot()
				if limited && snapshot.PolicyCalls == 0 {
					b.Fatal("deadline calibration did not execute physical policy SQL")
				}
				b.ReportMetric(float64(snapshot.DriverCalls)/float64(b.N), "businessSQL/op")
				b.ReportMetric(float64(snapshot.PolicyCalls)/float64(b.N), "policySQL/op")
			}
		})
	}
}

func TestDriverObservationIncludesEntImplicitReadsAndHookWrites(t *testing.T) {
	for _, name := range []string{"sqlite", "postgres"} {
		t.Run(name, func(t *testing.T) {
			_, c := observedSchemaFixture(t, name)
			ctx := t.Context()
			project := c.Project.Create().SetName("observed").SaveX(ctx)
			resource := c.Resource.Create().SetProjectID(project.ID).SetPath("observed.txt").SetFormat("txt").SetStoragePath("fixture").SaveX(ctx)
			segment := c.Segment.Create().SetResourceID(resource.ID).SetSegmentIndex(0).SetSourceText("fixture").SaveX(ctx)
			collector := &persistence.Collector{}
			ctx = persistence.WithObserver(persistence.WithMetadata(ctx, persistence.Metadata{Operation: "commit"}), collector)
			actual := c.Segment.UpdateOneID(segment.ID).SetTargetText("fixture target").SaveX(ctx)
			if actual.ContentVersion != segment.ContentVersion+1 {
				t.Fatal("hook did not run")
			}
			snapshot := collector.Snapshot()
			if snapshot.Calls != snapshot.DriverCalls || snapshot.DriverCalls < 2 || snapshot.RowSets == 0 {
				t.Fatalf("actual UPDATE and implicit entity read not captured together: %+v", snapshot)
			}
			t.Logf("fixture=%s business_mutations=1 actual_driver_sql=%d row_sets=%d rows=%d hook_version_delta=1", name, snapshot.DriverCalls, snapshot.RowSets, snapshot.Rows)
		})
	}
}

func TestTimeDriverRawQueryBudgetOwnsRowsUntilClose(t *testing.T) {
	for _, name := range []string{"sqlite", "postgres"} {
		t.Run(name, func(t *testing.T) {
			db, _ := observedSchemaFixture(t, name)
			driver := NewDriver(entsql.OpenDB(DialectFor(name), db))
			ctx := persistence.WithExecutionTimeout(t.Context(), 40*time.Millisecond)
			rows, err := driver.QueryContext(ctx, "SELECT 1 UNION ALL SELECT 2")
			if err != nil {
				t.Fatal(err)
			}
			if !rows.Next() {
				t.Fatal("budget canceled when Query returned")
			}
			deadline := time.Now().Add(time.Second)
			for rows.Err() == nil && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if !errors.Is(rows.Err(), context.DeadlineExceeded) {
				rows.Close()
				t.Fatalf("raw query did not retain hard budget: %v", rows.Err())
			}
			rows.Close()
			if stats := db.Stats(); stats.InUse != 0 {
				t.Fatalf("expired raw rows retained connection: %+v", stats)
			}
		})
	}
}

func TestTimeTransactionBridgesIndependentCallerCancellation(t *testing.T) {
	for _, name := range []string{"sqlite", "postgres"} {
		t.Run(name, func(t *testing.T) {
			db, _ := observedSchemaFixture(t, name)
			driver := NewDriver(entsql.OpenDB(DialectFor(name), db))
			tx, err := driver.BeginTx(WithJoinedRollback(t.Context()), nil)
			if err != nil {
				t.Fatal(err)
			}
			caller, cancel := context.WithCancel(context.Background())
			raw := tx.(interface {
				QueryContext(context.Context, string, ...any) (*sql.Rows, error)
			})
			rows, err := raw.QueryContext(caller, "SELECT 1 UNION ALL SELECT 2")
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if !rows.Next() {
				t.Fatal("no first row")
			}
			cancel()
			deadline := time.Now().Add(time.Second)
			for rows.Err() == nil && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !errors.Is(rows.Err(), context.Canceled) {
				rows.Close()
				tx.Rollback()
				t.Fatalf("caller cancellation omitted: %v", rows.Err())
			}
			rows.Close()
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
