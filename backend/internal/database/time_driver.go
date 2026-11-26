package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// TimeDriver applies the application's timestamp encoding to writes and query
// parameters alike. Use NewDriver for every ent client, including test clients.
type TimeDriver struct {
	dialect.Driver
	statsMu   sync.Mutex
	statsAt   time.Time
	statsNext atomic.Int64
	previous  sql.DBStats
}

func (d *TimeDriver) observePool(ctx context.Context) {
	driver, ok := d.Driver.(*entsql.Driver)
	if !ok {
		return
	}
	db, ok := driver.Conn.ExecQuerier.(*sql.DB)
	if !ok {
		return
	}
	now := time.Now()
	next := d.statsNext.Load()
	if now.UnixNano() < next || !d.statsNext.CompareAndSwap(next, now.Add(30*time.Second).UnixNano()) {
		return
	}
	d.statsMu.Lock()
	defer d.statsMu.Unlock()
	s := db.Stats()
	persistence.ObservePoolWait(ctx, d.Dialect(), s.WaitCount-d.previous.WaitCount, s.WaitDuration-d.previous.WaitDuration)
	slog.Default().Debug("database pool", "open", s.OpenConnections, "in_use", s.InUse, "idle", s.Idle, "wait_count", s.WaitCount, "wait_count_delta", s.WaitCount-d.previous.WaitCount, "wait_ms_delta", (s.WaitDuration - d.previous.WaitDuration).Milliseconds())
	d.previous = s
	d.statsAt = now
}
func observeSQL(ctx context.Context, phase string, start time.Time, err error, dialect string) {
	persistence.ObserveTransaction(persistence.WithDialect(ctx, dialect), phase, "", time.Since(start), err)
}

type contextSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (d *TimeDriver) ExecContext(ctx context.Context, query string, args ...any) (result sql.Result, err error) {
	if persistence.ExecutionOwned(ctx) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, persistence.ExecutionTimeout(ctx))
		defer cancel()
	}
	start := time.Now()
	defer func() { observeSQL(ctx, "exec", start, err, d.Dialect()); d.observePool(ctx) }()
	driver, ok := d.Driver.(contextSQL)
	if !ok {
		return nil, fmt.Errorf("database driver does not support ExecContext")
	}
	return driver.ExecContext(ctx, query, timeArgs(d.Dialect(), args).([]any)...)
}

func (d *TimeDriver) QueryContext(ctx context.Context, query string, args ...any) (rows *sql.Rows, err error) {
	if persistence.ExecutionOwned(ctx) {
		var cancel context.CancelFunc
		ctx, cancel = newStatementContext(ctx, ctx)
		ctx = context.WithValue(ctx, rawRowsBudgetKey{}, cancel)
		defer func() {
			if err != nil {
				cancel()
			}
		}()
	}
	start := time.Now()
	defer func() { observeSQL(ctx, "query", start, err, d.Dialect()); d.observePool(ctx) }()
	driver, ok := d.Driver.(contextSQL)
	if !ok {
		return nil, fmt.Errorf("database driver does not support QueryContext")
	}
	return driver.QueryContext(ctx, query, timeArgs(d.Dialect(), args).([]any)...)
}

func NewDriver(driver dialect.Driver) *TimeDriver { return &TimeDriver{Driver: driver} }

func (d *TimeDriver) Close() error {
	if driver, ok := d.Driver.(*entsql.Driver); ok {
		if db, ok := driver.Conn.ExecQuerier.(*sql.DB); ok {
			if _, tracked := sqlitePools.Load(db); tracked {
				return closeSQLitePoolAfterUse(db)
			}
		}
	}
	return d.Driver.Close()
}

func (d *TimeDriver) Exec(ctx context.Context, query string, args, v any) (err error) {
	if persistence.ExecutionOwned(ctx) {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, persistence.ExecutionTimeout(ctx))
		defer cancel()
	}
	start := time.Now()
	defer func() { observeSQL(ctx, "exec", start, err, d.Dialect()); d.observePool(ctx) }()
	return d.Driver.Exec(ctx, query, timeArgs(d.Dialect(), args), v)
}

func (d *TimeDriver) Query(ctx context.Context, query string, args, v any) (err error) {
	if persistence.ExecutionOwned(ctx) {
		var cancel context.CancelFunc
		ctx, cancel = newStatementContext(ctx, ctx)
		ctx = context.WithValue(ctx, rawRowsBudgetKey{}, cancel)
		defer func() {
			if err != nil {
				cancel()
			}
		}()
	}
	start := time.Now()
	defer func() { observeSQL(ctx, "query", start, err, d.Dialect()); d.observePool(ctx) }()
	return d.Driver.Query(ctx, query, timeArgs(d.Dialect(), args), v)
}

func (d *TimeDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	var tx dialect.Tx
	var err error
	// A cancelled database/sql transaction can report ErrTxDone before its
	// automatic rollback finishes. Retain its connection until cleanup joins.
	joined, _ := ctx.Value(joinedRollbackKey{}).(bool)
	if driver, ok := d.Driver.(*entsql.Driver); ok && (persistence.ExecutionOwned(ctx) || joined) {
		if db, ok := driver.Conn.ExecQuerier.(*sql.DB); ok {
			tx, err = beginJoinedTransaction(ctx, db, nil)
		} else {
			tx, err = d.Driver.Tx(ctx)
		}
	} else {
		tx, err = d.Driver.Tx(ctx)
	}
	if err != nil {
		return nil, err
	}
	return &timeTx{Tx: tx, dialect: d.Dialect(), ctx: ctx}, nil
}

func (d *TimeDriver) BeginTx(ctx context.Context, opts *sql.TxOptions) (dialect.Tx, error) {
	if joined, _ := ctx.Value(joinedRollbackKey{}).(bool); joined {
		pool, ok := d.Driver.(interface{ DB() *sql.DB })
		if !ok {
			return nil, fmt.Errorf("database driver does not expose a pool for joined rollback")
		}
		tx, err := beginJoinedTransaction(ctx, pool.DB(), opts)
		if err != nil {
			return nil, err
		}
		return &timeTx{Tx: tx, dialect: d.Dialect(), ctx: ctx}, nil
	}
	opener, ok := d.Driver.(interface {
		BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error)
	})
	if !ok {
		return nil, fmt.Errorf("database driver does not support BeginTx")
	}
	tx, err := opener.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &timeTx{Tx: tx, dialect: d.Dialect(), ctx: ctx}, nil
}

type timeTx struct {
	dialect.Tx
	dialect string
	ctx     context.Context
}
type transactionQueryContext struct {
	context.Context
	values context.Context
}

func (c transactionQueryContext) Value(key any) any {
	if v := c.values.Value(key); v != nil {
		return v
	}
	return c.Context.Value(key)
}
func (t *timeTx) queryContext(caller context.Context) (context.Context, context.CancelFunc) {
	parent := t.ctx
	if parent == nil {
		parent = caller
	}
	return newStatementContext(parent, caller)
}

func (t *timeTx) ExecContext(ctx context.Context, query string, args ...any) (result sql.Result, err error) {
	caller := ctx
	ctx, cancel := t.queryContext(ctx)
	defer cancel()
	defer func() { err = t.cancellationError(err, caller, ctx) }()
	start := time.Now()
	defer func() { observeSQL(ctx, "exec", start, err, t.dialect) }()
	tx, ok := t.Tx.(contextSQL)
	if !ok {
		return nil, fmt.Errorf("database transaction does not support ExecContext")
	}
	return tx.ExecContext(ctx, query, timeArgs(t.dialect, args).([]any)...)
}

func (t *timeTx) QueryContext(ctx context.Context, query string, args ...any) (rows *sql.Rows, err error) {
	caller := ctx
	ctx, cancel := t.queryContext(ctx)
	ctx = context.WithValue(ctx, rawRowsBudgetKey{}, cancel)
	defer func() {
		if err != nil {
			cancel()
		}
	}()
	defer func() { err = t.cancellationError(err, caller, ctx) }()
	start := time.Now()
	defer func() { observeSQL(ctx, "query", start, err, t.dialect) }()
	tx, ok := t.Tx.(contextSQL)
	if !ok {
		return nil, fmt.Errorf("database transaction does not support QueryContext")
	}
	return tx.QueryContext(ctx, query, timeArgs(t.dialect, args).([]any)...)
}

func (t *timeTx) Exec(ctx context.Context, query string, args, v any) (err error) {
	caller := ctx
	ctx, cancel := t.queryContext(ctx)
	defer cancel()
	defer func() { err = t.cancellationError(err, caller, ctx) }()
	start := time.Now()
	defer func() { observeSQL(ctx, "exec", start, err, t.dialect) }()
	return t.Tx.Exec(ctx, query, timeArgs(t.dialect, args), v)
}

func (t *timeTx) Query(ctx context.Context, query string, args, v any) (err error) {
	caller := ctx
	ctx, cancel := t.queryContext(ctx)
	// ent's schema adapter requires ColumnScanner to remain a concrete
	// *database/sql.Rows. Let the real driver's Rows.Close own cancellation
	// instead of replacing that scanner with an ORM-level wrapper.
	ctx = context.WithValue(ctx, rawRowsBudgetKey{}, cancel)
	defer func() {
		if err != nil {
			cancel()
		}
	}()
	defer func() { err = t.cancellationError(err, caller, ctx) }()
	start := time.Now()
	defer func() { observeSQL(ctx, "query", start, err, t.dialect) }()
	return t.Tx.Query(ctx, query, timeArgs(t.dialect, args), v)
}

func (t *timeTx) cancellationError(err error, caller, statement context.Context) error {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if caller.Err() != nil {
		err = errors.Join(err, caller.Err())
	}
	if t.ctx != nil && t.ctx.Err() != nil {
		err = errors.Join(err, t.ctx.Err())
	}
	if statement.Err() != nil {
		err = errors.Join(err, statement.Err())
	}
	return err
}

func timeArgs(driver string, args any) any {
	values, ok := args.([]any)
	if !ok {
		return args
	}
	var normalized []any
	for i, value := range values {
		t, changed := timeArg(driver, value)
		if !changed {
			continue
		}
		if normalized == nil {
			normalized = append([]any(nil), values...)
		}
		normalized[i] = t
	}
	if normalized == nil {
		return args
	}
	return normalized
}

func timeArg(driver string, value any) (any, bool) {
	var instant time.Time
	switch v := value.(type) {
	case time.Time:
		instant = v
	case *time.Time:
		if v == nil {
			return nil, true
		}
		instant = *v
	case sql.NullTime:
		if !v.Valid {
			return nil, true
		}
		instant = v.Time
	case *sql.NullTime:
		if v == nil || !v.Valid {
			return nil, true
		}
		instant = v.Time
	default:
		return value, false
	}
	instant = instant.UTC()
	if driver == dialect.SQLite {
		return instant.Format(timeutil.SQLiteLayout), true
	}
	if driver == dialect.Postgres {
		instant = instant.Truncate(time.Microsecond)
	}
	return instant, true
}
