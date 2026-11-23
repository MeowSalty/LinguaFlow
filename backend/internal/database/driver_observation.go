package database

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"modernc.org/sqlite"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"entgo.io/ent/dialect"

	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

// ObserveConnector retains the real driver and its database/sql pool semantics.
// Unlike an ORM hook it also sees implicit RETURNING/edge queries, raw SQL,
// prepared statements, automatic rollback and the complete driver.Rows life.
func ObserveConnector(connector driver.Connector, dialect string) driver.Connector {
	return observedConnector{Connector: connector, dialect: dialect}
}

type observedConnector struct {
	driver.Connector
	dialect string
	pool    *sqlitePoolOperations
}
type registeredConnector struct {
	driver driver.Driver
	dsn    string
}

func (c registeredConnector) Driver() driver.Driver { return c.driver }
func (c registeredConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.driver.Open(c.dsn)
}
func (c observedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.dialect == dialect.SQLite {
		if c.pool != nil {
			if err := c.pool.acquire(ctx); err != nil {
				return nil, err
			}
		}
		wrapped := &observedConn{dialect: c.dialect, pool: c.pool}
		if c.pool != nil {
			c.pool.own(wrapped)
		}
		wrapped.startSQLiteOperations()
		_, err := sqliteCall(wrapped, ctx, false, func() (struct{}, error) {
			conn, err := c.Connector.Connect(ctx)
			if err != nil {
				return struct{}{}, err
			}
			wrapped.Conn = conn
			return struct{}{}, wrapped.initializeBusy(ctx)
		}, nil)
		if err != nil {
			_ = wrapped.Close()
			return nil, err
		}
		return wrapped, nil
	}
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	wrapped := &observedConn{Conn: conn, dialect: c.dialect}
	if err := wrapped.initializeBusy(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return wrapped, nil
}

type observedConn struct {
	driver.Conn
	dialect          string
	bad              atomic.Bool
	busy             *sqliteBusyState
	activeTx, lastTx *observedTransaction
	sqlite           *sqliteOperations
	pool             *sqlitePoolOperations
}

func (c *observedConn) discard() { c.bad.Store(true) }
func (c *observedConn) isValidNative() bool {
	if c.bad.Load() {
		return false
	}
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}
func (c *observedConn) resetSessionNative(ctx context.Context) error {
	if c.bad.Load() {
		return driver.ErrBadConn
	}
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}
func (c *observedConn) checkNamedValueNative(v *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(v)
	}
	return driver.ErrSkip
}
func (c *observedConn) pingNative(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

// Preserve SQLite's consistent-backup extension used by offline backups and
// protected migration snapshots through sql.Conn.Raw.
func (c *observedConn) newBackupNative(uri string) (*sqlite.Backup, error) {
	if b, ok := c.Conn.(interface {
		NewBackup(string) (*sqlite.Backup, error)
	}); ok {
		return b.NewBackup(uri)
	}
	return nil, errors.New("database driver does not provide SQLite backups")
}
func (c *observedConn) prepareNative(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return c.wrapStmt(stmt), nil
}
func (c *observedConn) prepareContextNative(ctx context.Context, query string) (driver.Stmt, error) {
	if err := c.limitBusy(ctx); err != nil {
		return nil, err
	}
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err := p.PrepareContext(ctx, query)
		err = c.busyError(ctx, err)
		err = joinDriverErrors(err, c.restoreBusy(ctx))
		if err != nil {
			if stmt != nil {
				_ = stmt.Close()
			}
			return nil, err
		}
		return c.wrapStmt(stmt), nil
	}
	stmt, err := c.prepareNative(query)
	return stmt, joinDriverErrors(err, c.restoreBusy(ctx))
}
func (c *observedConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *observedConn) beginTxNative(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.limitBusy(ctx); err != nil {
		return nil, err
	}
	start := time.Now()
	var tx driver.Tx
	var err error
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = b.BeginTx(ctx, opts)
	} else {
		if opts.Isolation != 0 || opts.ReadOnly {
			return nil, errors.New("database driver does not support transaction options")
		}
		tx, err = c.Conn.Begin()
	}
	persistence.ObserveDatabase(ctx, "driver_begin", "", c.dialect, time.Since(start), err, 0)
	if err != nil {
		return nil, joinDriverErrors(c.busyError(ctx, err), c.restoreBusy(ctx))
	}
	wrapped := &observedTransaction{Tx: tx, conn: c, ctx: ctx, start: start, finished: make(chan struct{})}
	c.activeTx, c.lastTx = wrapped, wrapped
	return wrapped, nil
}
func (c *observedConn) execContextNative(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	if err := c.limitBusy(ctx); err != nil {
		return nil, err
	}
	start := time.Now()
	result, err := e.ExecContext(ctx, query, args)
	err = c.busyError(ctx, err)
	if !errors.Is(err, driver.ErrSkip) {
		persistence.ObserveDatabase(ctx, "driver_exec", "", c.dialect, time.Since(start), err, 0)
	}
	return result, joinDriverErrors(err, c.restoreBusy(ctx))
}
func (c *observedConn) queryContextNative(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	if err := c.limitBusy(ctx); err != nil {
		return nil, err
	}
	start := time.Now()
	rows, err := q.QueryContext(ctx, query, args)
	err = c.busyError(ctx, err)
	if !errors.Is(err, driver.ErrSkip) {
		persistence.ObserveDatabase(ctx, "driver_query", "", c.dialect, time.Since(start), err, 0)
	}
	if err != nil {
		return nil, joinDriverErrors(err, c.restoreBusy(ctx))
	}
	wrapped := newObservedRows(ctx, rows, c.dialect, start)
	wrapped.conn = c
	c.trackRows(wrapped)
	return wrapped, nil
}

type observedTransaction struct {
	driver.Tx
	conn      *observedConn
	ctx       context.Context
	start     time.Time
	finished  chan struct{}
	finishErr error
	completed bool
}

func (t *observedTransaction) commitNative() error {
	if t.completed {
		return nil
	}
	// driver.Tx.Commit has no context parameter. Refresh the physical busy
	// limit here, rather than retaining the budget of an earlier statement.
	err := t.conn.limitBusy(t.ctx)
	if err == nil {
		err = t.Tx.Commit()
	}
	err = t.conn.busyError(t.ctx, err)
	t.conn.activeTx = nil
	err = errors.Join(err, t.conn.restoreBusy(t.ctx))
	t.finishErr = err
	t.completed = true
	close(t.finished)
	if err != nil {
		t.conn.discard()
	}
	persistence.ObserveDatabase(t.ctx, "driver_transaction", "commit", t.conn.dialect, time.Since(t.start), err, 0)
	return err
}
func (t *observedTransaction) rollbackNative() error {
	if t.completed {
		return nil
	}
	err := t.Tx.Rollback()
	t.conn.activeTx = nil
	err = errors.Join(err, t.conn.restoreBusy(t.ctx))
	t.finishErr = err
	t.completed = true
	close(t.finished)
	// The wrapper exposes pool health interfaces. Do not thereby opt a driver
	// that lacks them into reusing connections after cancellation rollback.
	_, resets := t.conn.Conn.(driver.SessionResetter)
	_, validates := t.conn.Conn.(driver.Validator)
	if err != nil || t.ctx.Err() != nil && (!resets || !validates) {
		t.conn.discard()
	}
	persistence.ObserveDatabase(t.ctx, "driver_transaction", "rollback", t.conn.dialect, time.Since(t.start), err, 0)
	return err
}

// The returned handle contains wrapper-owned completion facts, not a raw
// driver connection. Joined cleanup can distinguish healthy automatic rollback
// from ErrTxDone while the physical rollback is still running.
func (c *observedConn) transactionCleanup() (<-chan struct{}, func() error) {
	if c.lastTx == nil {
		return nil, nil
	}
	tx := c.lastTx
	return tx.finished, func() error { return tx.finishErr }
}

type observedStmt struct {
	driver.Stmt
	dialect  string
	conn     *observedConn
	closed   bool
	numInput int
}

func (s *observedStmt) execNative(args []driver.Value) (driver.Result, error) {
	return s.Stmt.Exec(args)
}
func (s *observedStmt) queryNative(args []driver.Value) (driver.Rows, error) {
	start := time.Now()
	rows, err := s.Stmt.Query(args)
	if err != nil {
		return nil, err
	}
	return newObservedRows(context.Background(), rows, s.dialect, start), nil
}
func (s *observedStmt) execContextNative(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if s.conn != nil {
		if err := s.conn.limitBusy(ctx); err != nil {
			return nil, err
		}
	}
	start := time.Now()
	var result driver.Result
	var err error
	if e, ok := s.Stmt.(driver.StmtExecContext); ok {
		result, err = e.ExecContext(ctx, args)
	} else {
		values := make([]driver.Value, len(args))
		for i, v := range args {
			values[i] = v.Value
		}
		result, err = s.Stmt.Exec(values)
	}
	persistence.ObserveDatabase(ctx, "driver_exec", "", s.dialect, time.Since(start), err, 0)
	if s.conn != nil {
		err = joinDriverErrors(s.conn.busyError(ctx, err), s.conn.restoreBusy(ctx))
	}
	return result, err
}
func (s *observedStmt) queryContextNative(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if s.conn != nil {
		if err := s.conn.limitBusy(ctx); err != nil {
			return nil, err
		}
	}
	start := time.Now()
	var rows driver.Rows
	var err error
	if q, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rows, err = q.QueryContext(ctx, args)
	} else {
		values := make([]driver.Value, len(args))
		for i, v := range args {
			values[i] = v.Value
		}
		rows, err = s.Stmt.Query(values)
	}
	persistence.ObserveDatabase(ctx, "driver_query", "", s.dialect, time.Since(start), err, 0)
	if err != nil {
		if s.conn != nil {
			err = joinDriverErrors(s.conn.busyError(ctx, err), s.conn.restoreBusy(ctx))
		}
		return nil, err
	}
	wrapped := newObservedRows(ctx, rows, s.dialect, start)
	wrapped.conn = s.conn
	if s.conn != nil {
		s.conn.trackRows(wrapped)
	}
	return wrapped, nil
}

type rawRowsBudgetKey struct{}
type observedRows struct {
	driver.Rows
	ctx      context.Context
	dialect  string
	start    time.Time
	once     sync.Once
	count    uint64
	err      error
	conn     *observedConn
	closed   bool
	columns  []string
	metadata []rowColumnMetadata
}

func newObservedRows(ctx context.Context, rows driver.Rows, dialect string, start time.Time) *observedRows {
	return &observedRows{Rows: rows, ctx: ctx, dialect: dialect, start: start}
}
func (r *observedRows) nextNative(values []driver.Value) error {
	if r.conn != nil {
		if err := r.conn.limitBusy(r.ctx); err != nil {
			r.err = err
			return err
		}
	}
	err := r.Rows.Next(values)
	if r.conn != nil {
		err = r.conn.busyError(r.ctx, err)
	}
	if err == nil {
		r.count++
	} else if !errors.Is(err, io.EOF) {
		r.err = err
	}
	return err
}
func (r *observedRows) closeNative() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.conn != nil && r.conn.sqlite != nil {
		delete(r.conn.sqlite.rows, r)
	}
	err := r.Rows.Close()
	if r.conn != nil {
		err = errors.Join(err, r.conn.restoreBusy(r.ctx))
	}
	r.once.Do(func() {
		persistence.ObserveDatabase(r.ctx, "driver_rows", "close", r.dialect, time.Since(r.start), errors.Join(r.err, err, r.ctx.Err()), r.count)
		if cancel, ok := r.ctx.Value(rawRowsBudgetKey{}).(context.CancelFunc); ok {
			cancel()
		}
	})
	return err
}
func (r *observedRows) HasNextResultSet() bool {
	if r.conn != nil && r.conn.sqlite != nil {
		value, err := sqliteCall(r.conn, r.ctx, false, func() (bool, error) {
			if n, ok := r.Rows.(driver.RowsNextResultSet); ok {
				return n.HasNextResultSet(), nil
			}
			return false, nil
		}, nil)
		return err == nil && value
	}
	if n, ok := r.Rows.(driver.RowsNextResultSet); ok {
		return n.HasNextResultSet()
	}
	return false
}
func (r *observedRows) NextResultSet() error {
	if r.conn != nil && r.conn.sqlite != nil {
		_, err := sqliteCall(r.conn, r.ctx, false, func() (struct{}, error) {
			if n, ok := r.Rows.(driver.RowsNextResultSet); ok {
				return struct{}{}, n.NextResultSet()
			}
			return struct{}{}, io.EOF
		}, nil)
		return err
	}
	if n, ok := r.Rows.(driver.RowsNextResultSet); ok {
		return n.NextResultSet()
	}
	return io.EOF
}
func (r *observedRows) ColumnTypeDatabaseTypeName(i int) string {
	if m, ok := r.metadataColumn(i); ok {
		return m.databaseType
	}
	if c, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return c.ColumnTypeDatabaseTypeName(i)
	}
	return ""
}
func (r *observedRows) ColumnTypeScanType(i int) reflect.Type {
	if m, ok := r.metadataColumn(i); ok {
		return m.scanType
	}
	if c, ok := r.Rows.(driver.RowsColumnTypeScanType); ok {
		return c.ColumnTypeScanType(i)
	}
	return reflect.TypeFor[any]()
}
func (r *observedRows) ColumnTypeLength(i int) (int64, bool) {
	if m, ok := r.metadataColumn(i); ok {
		return m.length, m.hasLength
	}
	if c, ok := r.Rows.(driver.RowsColumnTypeLength); ok {
		return c.ColumnTypeLength(i)
	}
	return 0, false
}
func (r *observedRows) ColumnTypeNullable(i int) (bool, bool) {
	if m, ok := r.metadataColumn(i); ok {
		return m.nullable, m.hasNullable
	}
	if c, ok := r.Rows.(driver.RowsColumnTypeNullable); ok {
		return c.ColumnTypeNullable(i)
	}
	return false, false
}
func (r *observedRows) ColumnTypePrecisionScale(i int) (int64, int64, bool) {
	if m, ok := r.metadataColumn(i); ok {
		return m.precision, m.scale, m.hasPrecision
	}
	if c, ok := r.Rows.(driver.RowsColumnTypePrecisionScale); ok {
		return c.ColumnTypePrecisionScale(i)
	}
	return 0, 0, false
}
