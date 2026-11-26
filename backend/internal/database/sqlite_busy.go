package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

var ErrSQLiteBusyPolicyRestore = errors.New("SQLite statement completed but its connection policy could not be restored; connection discarded")

type sqliteBusyState struct{ original, current int64 }

// database/sql recognizes ErrSkip/ErrBadConn by identity. Adding a nil policy
// error must preserve that identity, including on the unchanged PostgreSQL path.
func joinDriverErrors(err, policyErr error) error {
	if policyErr == nil {
		return err
	}
	if err == nil {
		return policyErr
	}
	return errors.Join(err, policyErr)
}

// OpenSQLitePool uses the same registered modernc driver as sql.Open. It adds
// physical statement/Rows deadline enforcement and observation without reserving
// any application schema or format, for explicit offline/test pools as well.
func OpenSQLitePool(dsn string) (*sql.DB, error) {
	registered, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	drv := registered.Driver()
	_ = registered.Close()
	pool := &sqlitePoolOperations{changed: make(chan struct{}), owners: make(map[*observedConn]struct{})}
	db := sql.OpenDB(observedConnector{Connector: registeredConnector{driver: drv, dsn: dsn}, dialect: dialect.SQLite, pool: pool})
	pool.db = db
	sqlitePools.Store(db, pool)
	return db, nil
}

func (c *observedConn) initializeBusy(ctx context.Context) error {
	if c.dialect != dialect.SQLite {
		return nil
	}
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil
	}
	start := time.Now()
	rows, err := q.QueryContext(c.nativeContext(ctx), "PRAGMA busy_timeout", nil)
	persistence.ObserveDatabase(ctx, "driver_policy_query", "", c.dialect, time.Since(start), err, 0)
	if err != nil {
		if rows != nil {
			err = joinDriverErrors(err, rows.Close())
		}
		return err
	}
	values := make([]driver.Value, 1)
	err = rows.Next(values)
	closeErr := rows.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return err
	}
	value, ok := values[0].(int64)
	if !ok || value < 0 {
		return errors.New("invalid SQLite busy_timeout")
	}
	c.busy = &sqliteBusyState{original: value, current: value}
	return nil
}

func (c *observedConn) effectiveDeadline(ctx context.Context) (time.Time, bool, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, false, err
	}
	at, ok := ctx.Deadline()
	if c.activeTx != nil {
		parent := c.activeTx.ctx
		if err := parent.Err(); err != nil {
			return time.Time{}, false, err
		}
		if txAt, txOK := parent.Deadline(); txOK && (!ok || txAt.Before(at)) {
			at, ok = txAt, true
		}
	}
	return at, ok, nil
}

func (c *observedConn) limitBusy(ctx context.Context) error {
	if c.busy == nil {
		return nil
	}
	at, limited, err := c.effectiveDeadline(ctx)
	if err != nil {
		return err
	}
	target := c.busy.original
	if limited {
		left := time.Until(at)
		if left <= 0 {
			return context.DeadlineExceeded
		}
		// Round upward only to SQLite's millisecond grid; never lengthen a
		// shorter caller-configured busy timeout.
		milliseconds := int64(left / time.Millisecond)
		if left%time.Millisecond != 0 {
			milliseconds++
		}
		target = min(target, milliseconds)
	}
	if target == c.busy.current {
		return nil
	}
	if err := c.setBusy(ctx, ctx, target); err != nil {
		c.discard()
		return err
	}
	return nil
}

func (c *observedConn) setBusy(execCtx, observationCtx context.Context, value int64) error {
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return errors.New("SQLite driver cannot set busy timeout")
	}
	start := time.Now()
	_, err := e.ExecContext(c.nativeContext(execCtx), fmt.Sprintf("PRAGMA busy_timeout=%d", value), nil)
	persistence.ObserveDatabase(observationCtx, "driver_policy_exec", "", c.dialect, time.Since(start), err, 0)
	if err == nil {
		c.busy.current = value
	}
	return err
}

func (c *observedConn) restoreBusy(ctx context.Context) error {
	if c.busy == nil || c.activeTx != nil || c.busy.current == c.busy.original {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), joinedCleanupTimeout)
	defer cancel()
	if err := c.setBusy(cleanup, ctx, c.busy.original); err != nil {
		c.discard()
		// Do not surface ErrBadConn here: database/sql could replay a write
		// that has already completed. Health validation discards the session.
		return ErrSQLiteBusyPolicyRestore
	}
	return nil
}

func (c *observedConn) busyError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return errors.Join(err, ctx.Err())
	}
	if c.activeTx != nil && c.activeTx.ctx.Err() != nil {
		return errors.Join(err, c.activeTx.ctx.Err())
	}
	if c.busy != nil && isSQLiteContention(err) {
		at, ok, _ := c.effectiveDeadline(ctx)
		if ok && !time.Now().Before(at) {
			return errors.Join(err, context.DeadlineExceeded)
		}
	}
	return err
}
