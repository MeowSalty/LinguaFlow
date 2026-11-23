package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteRowsNextCPURetainsCallerDeadline(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		name := "query"
		if prepared {
			name = "prepared_query"
		}
		t.Run(name, func(t *testing.T) {
			db, err := OpenSQLitePool(":memory:?_pragma=busy_timeout(5000)")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := closeSQLitePoolAfterUse(db); err != nil {
					t.Error(err)
				}
			}()
			db.SetMaxOpenConns(1)
			const query = "WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000000) SELECT 1 UNION ALL SELECT count(*) FROM n"
			var stmt *sql.Stmt
			if prepared {
				stmt, err = db.PrepareContext(t.Context(), query)
				if err != nil {
					t.Fatal(err)
				}
				defer stmt.Close()
			}
			ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
			defer cancel()
			var rows *sql.Rows
			if prepared {
				rows, err = stmt.QueryContext(ctx)
			} else {
				rows, err = db.QueryContext(ctx, query)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if !rows.Next() {
				t.Fatal("constant first row did not return", rows.Err())
			}
			var first int
			if err := rows.Scan(&first); err != nil || first != 1 {
				t.Fatalf("constant first row=%d err=%v", first, err)
			}
			start := time.Now()
			next := rows.Next()
			elapsed := time.Since(start)
			err = rows.Err()
			t.Logf("CPU second Next: elapsed=%s next=%t err=%v", elapsed, next, err)
			if elapsed > 500*time.Millisecond || next || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("CPU Rows.Next outlived caller deadline: elapsed=%s next=%t err=%v", elapsed, next, err)
			}
		})
	}
}

func TestSQLiteSharedCacheLockRetainsCallerDeadline(t *testing.T) {
	for _, prepare := range []bool{false, true} {
		name := "table_query"
		if prepare {
			name = "schema_prepare"
		}
		t.Run(name, func(t *testing.T) {
			dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "shared.db")) + "?cache=shared&_pragma=busy_timeout(5000)"
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
				if err := db.PingContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				pools[i] = db
			}
			if _, err := pools[0].ExecContext(t.Context(), "CREATE TABLE shared_probe (value INTEGER)"); err != nil {
				t.Fatal(err)
			}
			held, err := pools[0].BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			lockSQL := "INSERT INTO shared_probe VALUES (1)"
			if prepare {
				// Preparing SQL must read sqlite_schema, whose shared-cache lock
				// is held by this uncommitted schema change.
				lockSQL = "CREATE TABLE held_schema_lock (value INTEGER)"
			}
			if _, err := held.ExecContext(t.Context(), lockSQL); err != nil {
				held.Rollback()
				t.Fatal(err)
			}
			// The test owns this release goroutine and joins it before pool
			// cleanup. A caller deadline must not wait for the unrelated owner.
			released := make(chan error, 1)
			go func() {
				time.Sleep(300 * time.Millisecond)
				released <- held.Rollback()
			}()
			defer func() {
				if err := <-released; err != nil {
					t.Error("release shared-cache lock", err)
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
			defer cancel()
			start := time.Now()
			if prepare {
				var stmt *sql.Stmt
				stmt, err = pools[1].PrepareContext(ctx, "SELECT value FROM shared_probe")
				if stmt != nil {
					stmt.Close()
				}
			} else {
				err = pools[1].QueryRowContext(ctx, "SELECT value FROM shared_probe").Scan(new(int))
			}
			elapsed := time.Since(start)
			t.Logf("shared-cache wait: elapsed=%s err=%v; lock owner releases at 300ms", elapsed, err)
			if elapsed > 200*time.Millisecond || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("shared-cache wait outlived caller deadline: elapsed=%s err=%v", elapsed, err)
			}
		})
	}
}
