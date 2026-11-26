package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"

	"modernc.org/sqlite"
)

func (c *observedConn) IsValid() bool {
	if c.bad.Load() {
		return false
	}
	value, err := sqliteCall(c, context.Background(), false, func() (bool, error) { return c.isValidNative(), nil })
	return err == nil && value
}
func (c *observedConn) ResetSession(ctx context.Context) error {
	if c.bad.Load() {
		return driver.ErrBadConn
	}
	_, err := sqliteCall(c, ctx, false, func() (struct{}, error) { return struct{}{}, c.resetSessionNative(ctx) })
	return err
}
func (c *observedConn) CheckNamedValue(v *driver.NamedValue) error {
	value := *v
	result, err := sqliteCall(c, context.Background(), false, func() (driver.NamedValue, error) { err := c.checkNamedValueNative(&value); return value, err })
	if err == nil || err == driver.ErrSkip {
		*v = result
	}
	return err
}
func (c *observedConn) Ping(ctx context.Context) error {
	_, err := sqliteCall(c, ctx, false, func() (struct{}, error) { return struct{}{}, c.pingNative(ctx) })
	return err
}
func (c *observedConn) NewBackup(uri string) (*sqlite.Backup, error) {
	return sqliteCall(c, context.Background(), false, func() (*sqlite.Backup, error) { return c.newBackupNative(uri) })
}
func (c *observedConn) Prepare(query string) (driver.Stmt, error) {
	if c.sqlite == nil {
		return c.prepareNative(query)
	}
	return c.PrepareContext(context.Background(), query)
}
func (c *observedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return sqliteCall(c, ctx, false, func() (driver.Stmt, error) { return c.prepareContextNative(ctx, query) })
}
func (c *observedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return sqliteCall(c, ctx, false, func() (driver.Tx, error) { return c.beginTxNative(ctx, opts) })
}
func (c *observedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.sqlite != nil {
		args = cloneNamedValues(args)
	}
	return sqliteCall(c, ctx, false, func() (driver.Result, error) { return c.execContextNative(ctx, query, args) })
}
func (c *observedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.sqlite != nil {
		args = cloneNamedValues(args)
	}
	return sqliteCall(c, ctx, false, func() (driver.Rows, error) { return c.queryContextNative(ctx, query, args) })
}

func (c *observedConn) wrapStmt(stmt driver.Stmt) *observedStmt {
	wrapped := &observedStmt{Stmt: stmt, conn: c, dialect: c.dialect, numInput: stmt.NumInput()}
	if c.sqlite != nil {
		c.sqlite.statements[wrapped] = struct{}{}
	}
	return wrapped
}
func (s *observedStmt) NumInput() int {
	if s.conn == nil {
		return s.Stmt.NumInput()
	}
	return s.numInput
}
func (s *observedStmt) closeNative() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.Stmt.Close()
		if s.conn != nil && s.conn.sqlite != nil {
			delete(s.conn.sqlite.statements, s)
		}
		s.nativeClosed.Store(true)
	})
	return s.closeErr
}
func (s *observedStmt) Close() error {
	if s.nativeClosed.Load() {
		return s.closeErr
	}
	if s.conn == nil {
		return s.closeNative()
	}
	err := s.conn.cleanupSQLite(s.closeNative)
	if s.nativeClosed.Load() {
		return s.closeErr
	}
	return err
}
func namedValues(args []driver.Value) []driver.NamedValue {
	values := make([]driver.NamedValue, len(args))
	for i, value := range args {
		values[i] = driver.NamedValue{Ordinal: i + 1, Value: value}
	}
	return values
}
func (s *observedStmt) Exec(args []driver.Value) (driver.Result, error) {
	if s.conn == nil || s.conn.sqlite == nil {
		return s.execNative(args)
	}
	return s.ExecContext(context.Background(), namedValues(args))
}
func (s *observedStmt) Query(args []driver.Value) (driver.Rows, error) {
	if s.conn == nil || s.conn.sqlite == nil {
		return s.queryNative(args)
	}
	return s.QueryContext(context.Background(), namedValues(args))
}
func (s *observedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if s.conn == nil {
		return s.execContextNative(ctx, args)
	}
	if s.conn.sqlite != nil {
		args = cloneNamedValues(args)
	}
	return sqliteCall(s.conn, ctx, false, func() (driver.Result, error) { return s.execContextNative(ctx, args) })
}
func (s *observedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if s.conn == nil {
		return s.queryContextNative(ctx, args)
	}
	if s.conn.sqlite != nil {
		args = cloneNamedValues(args)
	}
	return sqliteCall(s.conn, ctx, false, func() (driver.Rows, error) { return s.queryContextNative(ctx, args) })
}

func (t *observedTransaction) Commit() error {
	if t.conn.sqlite != nil && t.conn.bad.Load() {
		_ = t.conn.cleanupSQLite(t.rollbackNative)
		return ErrSQLiteConnectionDiscarded
	}
	_, err := sqliteCall(t.conn, t.ctx, false, func() (struct{}, error) { return struct{}{}, t.commitNative() })
	if err != nil && t.conn.sqlite != nil {
		// database/sql may already have claimed Commit, preventing its automatic
		// rollback from running. Even cancellation before enqueue must arrange
		// a real cleanup acknowledgement; completed native Commit is a no-op.
		t.conn.discard()
		_ = t.conn.cleanupSQLite(t.rollbackNative)
	}
	return err
}
func (t *observedTransaction) Rollback() error {
	// Rollback acknowledges actual native cleanup. Joined transaction cleanup
	// may stop waiting on its own wall budget while this worker still owns it.
	if t.conn.sqlite != nil {
		select {
		case <-t.finished:
			return t.finishErr
		default:
		}
		if t.conn.bad.Load() {
			_ = t.conn.Close()
			<-t.finished
			return t.finishErr
		}
	}
	_, err := sqliteCall(t.conn, context.Background(), true, func() (struct{}, error) { return struct{}{}, t.rollbackNative() })
	if t.conn.sqlite != nil {
		select {
		case <-t.finished:
			return t.finishErr
		default:
		}
	}
	return err
}
func (c *observedConn) physicalCleanup() (<-chan struct{}, func() error, func() bool) {
	if c.sqlite == nil {
		return nil, nil, nil
	}
	return c.sqlite.closed, func() error { return c.sqlite.closeErr }, c.bad.Load
}

type rowColumnMetadata struct {
	databaseType          string
	scanType              reflect.Type
	length                int64
	hasLength             bool
	nullable, hasNullable bool
	precision, scale      int64
	hasPrecision          bool
}

func (c *observedConn) trackRows(r *observedRows) {
	if c.sqlite == nil {
		return
	}
	r.columns = append([]string(nil), r.Rows.Columns()...)
	metadata := make([]rowColumnMetadata, len(r.columns))
	for i := range metadata {
		m := &metadata[i]
		m.databaseType = r.ColumnTypeDatabaseTypeName(i)
		m.scanType = r.ColumnTypeScanType(i)
		m.length, m.hasLength = r.ColumnTypeLength(i)
		m.nullable, m.hasNullable = r.ColumnTypeNullable(i)
		m.precision, m.scale, m.hasPrecision = r.ColumnTypePrecisionScale(i)
	}
	r.metadata = metadata
	c.sqlite.rows[r] = struct{}{}
}
func (r *observedRows) Columns() []string {
	if r.conn != nil && r.conn.sqlite != nil {
		return r.columns
	}
	return r.Rows.Columns()
}
func (r *observedRows) Next(values []driver.Value) error {
	if r.conn == nil || r.conn.sqlite == nil {
		return r.nextNative(values)
	}
	private := make([]driver.Value, len(values))
	result, err := sqliteCall(r.conn, r.ctx, false, func() ([]driver.Value, error) {
		err := r.nextNative(private)
		return cloneDriverValues(private), err
	})
	if err == nil {
		copy(values, result)
	}
	return err
}
func (r *observedRows) Close() error {
	if r.nativeClosed.Load() {
		return r.closeErr
	}
	if r.conn == nil {
		return r.closeNative()
	}
	err := r.conn.cleanupSQLite(r.closeNative)
	if r.nativeClosed.Load() {
		return r.closeErr
	}
	return err
}

func closeSQLitePoolAfterUse(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), joinedCleanupTimeout)
	defer cancel()
	return CloseSQLitePool(ctx, db)
}

// Kept outside the worker only for frozen metadata, never for native calls.
func (r *observedRows) metadataColumn(i int) (rowColumnMetadata, bool) {
	if r.conn != nil && r.conn.sqlite != nil && r.metadata != nil {
		return r.metadata[i], true
	}
	return rowColumnMetadata{}, false
}
