package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

func sqliteBusyPools(t *testing.T) [2]*sql.DB {
	t.Helper()
	dsn := filepath.ToSlash(filepath.Join(t.TempDir(), "busy.db")) + "?_pragma=busy_timeout(5000)"
	var pools [2]*sql.DB
	for i := range pools {
		db, err := OpenSQLitePool(dsn)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() {
			if err := closeSQLitePoolAfterUse(db); err != nil {
				t.Error(err)
			}
		})
		pools[i] = db
	}
	if _, err := pools[0].ExecContext(t.Context(), "CREATE TABLE busy_probe (value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	return pools
}

func physicalBusyTimeout(t *testing.T, conn *sql.Conn) int64 {
	t.Helper()
	var value int64
	err := conn.Raw(func(raw any) error {
		// Inspect the underlying session without the wrapper first restoring it
		// for this diagnostic query. That would conceal a leaked short policy.
		rows, err := raw.(*observedConn).Conn.(driver.QueryerContext).QueryContext(t.Context(), "PRAGMA busy_timeout", nil)
		if err != nil {
			return err
		}
		values := make([]driver.Value, 1)
		err = rows.Next(values)
		closeErr := rows.Close()
		if err == nil {
			value = values[0].(int64)
		}
		return errors.Join(err, closeErr)
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSQLiteBusyPolicyRestoredAfterExecRowsAndTransaction(t *testing.T) {
	db := sqliteBusyPools(t)[0]
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := conn.ExecContext(ctx, "INSERT INTO busy_probe VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if value := physicalBusyTimeout(t, conn); value != 5000 {
		t.Fatalf("Exec retained short busy policy: %d", value)
	}
	rows, err := conn.QueryContext(ctx, "SELECT value FROM busy_probe")
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		rows.Close()
		t.Fatal("missing row", rows.Err())
	}
	if value := physicalBusyTimeout(t, conn); value <= 0 || value > 1000 {
		rows.Close()
		t.Fatalf("open rows lost their physical deadline: %d", value)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if value := physicalBusyTimeout(t, conn); value != 5000 {
		t.Fatalf("Rows.Close retained short busy policy: %d", value)
	}
	for _, commit := range []bool{false, true} {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO busy_probe VALUES (2)"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		if value := physicalBusyTimeout(t, conn); value != 5000 {
			t.Fatalf("transaction finish retained short busy policy: %d", value)
		}
	}
}

func TestSQLiteBusyPolicyObservationIncludesPhysicalOverhead(t *testing.T) {
	db := sqliteBusyPools(t)[0]
	collector := &persistence.Collector{}
	ctx, cancel := context.WithTimeout(persistence.WithObserver(t.Context(), collector), time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "INSERT INTO busy_probe VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	snapshot := collector.Snapshot()
	// Fast PRAGMA statements can take less than Windows' clock resolution;
	// their count must remain visible even when measured duration is zero.
	if snapshot.DriverCalls != 1 || snapshot.PolicyCalls != 2 {
		t.Fatalf("physical timeout set/restore SQL missing: %+v", snapshot)
	}
	collector = &persistence.Collector{}
	ctx = persistence.WithObserver(ctx, collector)
	if err := db.QueryRowContext(ctx, "SELECT value FROM busy_probe").Scan(new(int)); err != nil {
		t.Fatal(err)
	}
	snapshot = collector.Snapshot()
	if snapshot.DriverCalls != 1 || snapshot.PolicyCalls < 2 {
		t.Fatalf("physical rows timeout set/restore SQL missing: %+v", snapshot)
	}
}

func TestSQLiteBusyDeadlinesAcrossStatementPaths(t *testing.T) {
	for _, path := range []string{"exec", "query", "prepared_exec", "prepared_query"} {
		t.Run(path, func(t *testing.T) {
			pools := sqliteBusyPools(t)
			held, err := pools[0].BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Rollback()
			if _, err := held.ExecContext(t.Context(), "INSERT INTO busy_probe VALUES (1)"); err != nil {
				t.Fatal(err)
			}
			query := "INSERT INTO busy_probe VALUES (2)"
			if path == "query" || path == "prepared_query" {
				query += " RETURNING value"
			}
			var stmt *sql.Stmt
			if path == "prepared_exec" || path == "prepared_query" {
				stmt, err = pools[1].PrepareContext(t.Context(), query)
				if err != nil {
					t.Fatal(err)
				}
				defer stmt.Close()
			}
			ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
			defer cancel()
			start := time.Now()
			switch path {
			case "exec":
				_, err = pools[1].ExecContext(ctx, query)
			case "query":
				err = pools[1].QueryRowContext(ctx, query).Scan(new(int))
			case "prepared_exec":
				_, err = stmt.ExecContext(ctx)
			case "prepared_query":
				err = stmt.QueryRowContext(ctx).Scan(new(int))
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("busy deadline lost: %v", err)
			}
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatalf("physical busy wait outlived caller: %v", elapsed)
			}
			conn, err := pools[1].Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if value := physicalBusyTimeout(t, conn); value != 5000 {
				t.Fatalf("busy failure retained short session policy: %d", value)
			}
		})
	}
}

func TestSQLiteBusyCommitUsesRemainingTransactionDeadline(t *testing.T) {
	pools := sqliteBusyPools(t)
	if _, err := pools[0].ExecContext(t.Context(), "INSERT INTO busy_probe VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	reader, err := pools[0].BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	if err := reader.QueryRowContext(t.Context(), "SELECT value FROM busy_probe").Scan(new(int)); err != nil {
		t.Fatal(err)
	}
	writerConn, err := pools[1].Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer writerConn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	writer, err := writerConn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.ExecContext(ctx, "INSERT INTO busy_probe VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	var nativeFinished, physicalClosed <-chan struct{}
	var nativeResult func() error
	if err := writerConn.Raw(func(value any) error {
		observed := value.(*observedConn)
		nativeFinished, nativeResult = observed.transactionCleanup()
		physicalClosed, _, _ = observed.physicalCleanup()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Leave about 50ms for commit. Its native driver method has no context;
	// retaining the earlier 250ms busy limit would exceed the total budget.
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	err = writer.Commit()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy commit lost deadline: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("commit reused an earlier statement's busy budget: %v", elapsed)
	}
	// Caller cancellation can precede native Commit's acknowledgement. Keep
	// the reader lock held until that acknowledgement and physical cleanup;
	// releasing it on an unknown result could let a late Commit legitimately
	// succeed and would not prove the known blocked-failure atomicity oracle.
	if err := writerConn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-nativeFinished:
		if err := nativeResult(); err == nil {
			t.Fatal("blocked native Commit did not fail")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked native Commit did not acknowledge completion")
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("native Commit reused earlier busy budget before ACK: %v", elapsed)
	}
	select {
	case <-physicalClosed:
	case <-time.After(time.Second):
		t.Fatal("blocked commit connection did not acknowledge physical cleanup")
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pools[1].QueryRowContext(t.Context(), "SELECT COUNT(*) FROM busy_probe").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed commit persisted the blocked write: count=%d", count)
	}
}
