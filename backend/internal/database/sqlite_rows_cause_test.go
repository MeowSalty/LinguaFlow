package database

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

func TestSQLiteTransactionRowsRetainIndependentCallerDeadlineCause(t *testing.T) {
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
	driver := NewDriver(entsql.OpenDB(dialect.SQLite, db))
	tx, err := driver.BeginTx(WithJoinedRollback(context.Background()), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	raw := tx.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	for attempt := 0; attempt < 20; attempt++ {
		caller, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		rows, err := raw.QueryContext(caller, "SELECT 1 UNION ALL SELECT 2")
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if !rows.Next() {
			rows.Close()
			cancel()
			t.Fatal("missing first row", rows.Err())
		}
		<-caller.Done()
		limit := time.Now().Add(time.Second)
		for rows.Err() == nil && time.Now().Before(limit) {
			time.Sleep(time.Millisecond)
		}
		err = rows.Err()
		rows.Close()
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("attempt=%d: raw Rows.Err lost independent caller deadline cause: %v", attempt, err)
		}
	}
}

func TestSQLiteRowsNormalCloseDoesNotCancelCaller(t *testing.T) {
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
	driver := NewDriver(entsql.OpenDB(dialect.SQLite, db))
	for attempt := 0; attempt < 20; attempt++ {
		rows, err := driver.QueryContext(persistence.WithExecutionTimeout(t.Context(), time.Second), "SELECT 1 UNION ALL SELECT 2")
		if err != nil {
			t.Fatal(err)
		}
		if !rows.Next() {
			rows.Close()
			t.Fatal(rows.Err())
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
		if err := rows.Err(); err != nil {
			t.Errorf("attempt=%d: normal Close emitted cancellation: %v", attempt, err)
		}
	}
}
