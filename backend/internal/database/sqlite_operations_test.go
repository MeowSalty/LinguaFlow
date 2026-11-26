package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
)

type sqliteOwnershipState struct {
	entered, release, closed          chan struct{}
	enterOnce, releaseOnce, closeOnce sync.Once
	connects                          atomic.Int32
	started, finished, closedEarly    atomic.Bool
	arguments                         chan []byte
	bytes                             []byte
	closeEntered, closeRelease        chan struct{}
	closeEnterOnce, closeReleaseOnce  sync.Once
	closeCalls                        atomic.Int32
	rowsCloseCalls, stmtCloseCalls    atomic.Int32
	rollbackCalls, nativeActive       atomic.Int32
	nativeOverlap                     atomic.Bool
	rowsCloseErr, stmtCloseErr        error
	contexts                          chan sqliteNativeContext
}

type sqliteNativeContext struct {
	operation string
	ctx       context.Context
}

func newSQLiteOwnershipState() *sqliteOwnershipState {
	return &sqliteOwnershipState{entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), arguments: make(chan []byte, 1), bytes: []byte("native"), contexts: make(chan sqliteNativeContext, 64)}
}

func (s *sqliteOwnershipState) native() func() {
	if s.nativeActive.Add(1) != 1 {
		s.nativeOverlap.Store(true)
	}
	return func() { s.nativeActive.Add(-1) }
}
func (s *sqliteOwnershipState) observeContext(operation string, ctx context.Context) {
	s.contexts <- sqliteNativeContext{operation: operation, ctx: ctx}
}
func (s *sqliteOwnershipState) unblock() { s.releaseOnce.Do(func() { close(s.release) }) }
func (s *sqliteOwnershipState) unblockClose() {
	if s.closeRelease != nil {
		s.closeReleaseOnce.Do(func() { close(s.closeRelease) })
	}
}
func (s *sqliteOwnershipState) block() {
	s.started.Store(true)
	s.enterOnce.Do(func() { close(s.entered) })
	<-s.release
}

type sqliteOwnershipDriver struct{}

func (sqliteOwnershipDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type sqliteOwnershipConnector struct{ state *sqliteOwnershipState }

func (c sqliteOwnershipConnector) Driver() driver.Driver { return sqliteOwnershipDriver{} }
func (c sqliteOwnershipConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c.state.observeContext("connect", ctx)
	return &sqliteOwnershipConn{state: c.state, id: c.state.connects.Add(1)}, nil
}

type sqliteOwnershipConn struct {
	state *sqliteOwnershipState
	id    int32
}

func (c *sqliteOwnershipConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}
func (c *sqliteOwnershipConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	defer c.state.native()()
	c.state.observeContext("prepare", ctx)
	if query == "blocked_prepare" {
		c.state.block()
		c.state.finished.Store(true)
	}
	return &sqliteOwnershipStmt{state: c.state, query: query}, nil
}
func (c *sqliteOwnershipConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *sqliteOwnershipConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	defer c.state.native()()
	c.state.observeContext("begin", ctx)
	if ctx.Value(sqliteBlockedBeginKey{}) != nil {
		c.state.block()
		c.state.finished.Store(true)
	}
	return &sqliteOwnershipTx{state: c.state}, nil
}
func (c *sqliteOwnershipConn) Ping(ctx context.Context) error {
	defer c.state.native()()
	c.state.observeContext("ping", ctx)
	return nil
}
func (c *sqliteOwnershipConn) ResetSession(ctx context.Context) error {
	defer c.state.native()()
	c.state.observeContext("reset", ctx)
	return nil
}
func (*sqliteOwnershipConn) IsValid() bool { return true }
func (c *sqliteOwnershipConn) Close() error {
	defer c.state.native()()
	if c.id == 1 {
		c.state.closeCalls.Add(1)
		if c.state.closeRelease != nil {
			c.state.closeEnterOnce.Do(func() { close(c.state.closeEntered) })
			<-c.state.closeRelease
		}
		if c.state.started.Load() && !c.state.finished.Load() {
			c.state.closedEarly.Store(true)
		}
		c.state.closeOnce.Do(func() { close(c.state.closed) })
	}
	return nil
}
func (c *sqliteOwnershipConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	defer c.state.native()()
	c.state.observeContext("exec", ctx)
	if query == "blocked_write" {
		c.state.block()
		c.state.arguments <- append([]byte(nil), args[0].Value.([]byte)...)
		c.state.finished.Store(true)
	}
	return driver.RowsAffected(1), nil
}
func (c *sqliteOwnershipConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	defer c.state.native()()
	c.state.observeContext("query", ctx)
	if query == "blocked_query" {
		c.state.block()
		c.state.finished.Store(true)
	}
	return &sqliteOwnershipRows{state: c.state, query: query}, nil
}

type sqliteBlockedBeginKey struct{}
type sqliteOwnershipTx struct{ state *sqliteOwnershipState }

func (t *sqliteOwnershipTx) Commit() error { defer t.state.native()(); return nil }
func (t *sqliteOwnershipTx) Rollback() error {
	defer t.state.native()()
	t.state.rollbackCalls.Add(1)
	return nil
}

type sqliteOwnershipStmt struct {
	state *sqliteOwnershipState
	query string
}

func (*sqliteOwnershipStmt) NumInput() int { return -1 }
func (s *sqliteOwnershipStmt) Close() error {
	defer s.state.native()()
	s.state.stmtCloseCalls.Add(1)
	return s.state.stmtCloseErr
}
func (s *sqliteOwnershipStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), namedValues(args))
}
func (s *sqliteOwnershipStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), namedValues(args))
}
func (s *sqliteOwnershipStmt) ExecContext(ctx context.Context, _ []driver.NamedValue) (driver.Result, error) {
	defer s.state.native()()
	s.state.observeContext("prepared_exec", ctx)
	if s.query == "blocked_prepared_exec" {
		s.state.block()
		s.state.finished.Store(true)
	}
	return driver.RowsAffected(1), nil
}
func (s *sqliteOwnershipStmt) QueryContext(ctx context.Context, _ []driver.NamedValue) (driver.Rows, error) {
	defer s.state.native()()
	s.state.observeContext("prepared_query", ctx)
	if s.query == "blocked_prepared_query" {
		s.state.block()
		s.state.finished.Store(true)
	}
	return &sqliteOwnershipRows{state: s.state, query: s.query}, nil
}

type sqliteOwnershipRows struct {
	state    *sqliteOwnershipState
	query    string
	position int
}

func (*sqliteOwnershipRows) Columns() []string { return []string{"value"} }
func (r *sqliteOwnershipRows) Close() error {
	defer r.state.native()()
	if r.query != "PRAGMA busy_timeout" {
		r.state.rowsCloseCalls.Add(1)
	}
	if r.query == "blocked_rows" {
		clear(r.state.bytes)
	}
	if r.query != "PRAGMA busy_timeout" {
		return r.state.rowsCloseErr
	}
	return nil
}
func (r *sqliteOwnershipRows) Next(values []driver.Value) error {
	defer r.state.native()()
	if r.query == "blocked_rows" {
		if r.position == 0 {
			values[0] = r.state.bytes
			r.position++
			return nil
		}
		if r.position == 1 {
			r.state.block()
			values[0] = []byte("late")
			r.position++
			r.state.finished.Store(true)
			return nil
		}
		return io.EOF
	}
	if r.position != 0 {
		return io.EOF
	}
	r.position++
	if r.query == "PRAGMA busy_timeout" {
		values[0] = int64(5000)
	} else {
		values[0] = int64(1)
	}
	return nil
}
func sqliteOwnershipPool(t *testing.T, state *sqliteOwnershipState) *sql.DB {
	t.Helper()
	pool := &sqlitePoolOperations{changed: make(chan struct{}), owners: make(map[*observedConn]struct{})}
	db := sql.OpenDB(observedConnector{Connector: sqliteOwnershipConnector{state}, dialect: dialect.SQLite, pool: pool})
	pool.db = db
	sqlitePools.Store(db, pool)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		state.unblock()
		state.unblockClose()
		if err := closeSQLitePoolAfterUse(db); err != nil {
			t.Error(err)
		}
	})
	return db
}

func TestSQLiteCanceledOperationKeepsArgumentsPhysicalLimitAndCloseACK(t *testing.T) {
	state := newSQLiteOwnershipState()
	db := sqliteOwnershipPool(t, state)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	payload := []byte("caller payload")
	result := make(chan error, 1)
	go func() { _, err := db.ExecContext(ctx, "blocked_write", payload); result <- err }()
	<-state.entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrSQLiteOperationPending) || errors.Is(err, driver.ErrBadConn) {
			t.Fatalf("unknown canceled write can be replayed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("native operation blocked caller cancellation")
	}
	copy(payload, "mutated value!")
	second, stop := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer stop()
	if err := db.QueryRowContext(second, "SELECT 1").Scan(new(int)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("quarantined physical connection released its slot: %v", err)
	}
	if state.connects.Load() != 1 {
		t.Fatal("configured MaxOpenConns opened a second physical connection before cleanup")
	}
	select {
	case <-state.closed:
		t.Fatal("native connection closed before pending operation completed")
	default:
	}
	state.unblock()
	select {
	case bytes := <-state.arguments:
		if string(bytes) != "caller payload" {
			t.Fatalf("worker borrowed caller byte storage: %q", bytes)
		}
	case <-time.After(time.Second):
		t.Fatal("native work did not acknowledge release")
	}
	select {
	case <-state.closed:
	case <-time.After(time.Second):
		t.Fatal("physical Close was not acknowledged")
	}
	if state.closedEarly.Load() || state.nativeOverlap.Load() || state.closeCalls.Load() != 1 {
		t.Fatal("native Close overlapped native work")
	}
	if err := db.QueryRowContext(t.Context(), "SELECT 1").Scan(new(int)); err != nil {
		t.Fatal("pool unusable after acknowledged cleanup", err)
	}
}

func TestSQLitePoolCloseBudgetAndRepeatedNativeJoin(t *testing.T) {
	state := newSQLiteOwnershipState()
	state.closeEntered, state.closeRelease = make(chan struct{}), make(chan struct{})
	db := sqliteOwnershipPool(t, state)
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := CloseSQLitePool(ctx, db); !errors.Is(err, ErrTransactionCleanupTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("native close ignored cleanup budget", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("native close blocked the cleanup caller")
	}
	select {
	case <-state.closeEntered:
	case <-time.After(time.Second):
		t.Fatal("logical Close did not own native Close")
	}
	value, ok := sqlitePools.Load(db)
	if !ok {
		t.Fatal("unfinished cleanup lost its pool owner")
	}
	pool := value.(*sqlitePoolOperations)
	done := pool.logicalCloseDone
	second, stop := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer stop()
	if err := CloseSQLitePool(second, db); !errors.Is(err, ErrTransactionCleanupTimeout) {
		t.Fatal("repeated Close treated logical closed as physical done", err)
	}
	if done != pool.logicalCloseDone || state.closeCalls.Load() != 1 {
		t.Fatal("repeated Close started a second native close lifecycle")
	}
	select {
	case <-done:
		t.Fatal("logical Close falsely acknowledged unfinished native Close")
	default:
	}
	state.unblockClose()
	final, finish := context.WithTimeout(t.Context(), time.Second)
	defer finish()
	if err := CloseSQLitePool(final, db); err != nil {
		t.Fatal("native close did not join after release", err)
	}
	if _, exists := sqlitePools.Load(db); exists {
		t.Fatal("completed pool cleanup retained registry")
	}
}

func TestSQLiteCanceledRowsKeepDestinationAndReturnedBytesOwned(t *testing.T) {
	state := newSQLiteOwnershipState()
	db := sqliteOwnershipPool(t, state)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var raw *observedConn
	if err := conn.Raw(func(value any) error { raw = value.(*observedConn); return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rows, err := raw.QueryContext(ctx, "blocked_rows", nil)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		t.Fatal(err)
	}
	first := values[0].([]byte)
	result := make(chan error, 1)
	go func() { result <- rows.Next(values) }()
	<-state.entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrSQLiteOperationPending) {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Rows.Next blocked caller cancellation")
	}
	values[0] = "caller destination"
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	state.unblock()
	select {
	case <-state.closed:
	case <-time.After(time.Second):
		t.Fatal("rows/connection cleanup did not finish")
	}
	if values[0] != "caller destination" {
		t.Fatalf("late native Next wrote caller destination: %v", values)
	}
	if string(first) != "native" {
		t.Fatalf("native Close mutated handed-off bytes: %q", first)
	}
	if state.closedEarly.Load() || state.nativeOverlap.Load() || state.rowsCloseCalls.Load() != 1 || state.closeCalls.Load() != 1 {
		t.Fatal("native Close overlapped pending Next")
	}
}

func TestSQLiteCommitCanceledBeforeEnqueueAcknowledgesRollback(t *testing.T) {
	db := sqliteBusyPools(t)[0]
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(t.Context())
	var raw *observedConn
	var tx *observedTransaction
	if err := conn.Raw(func(value any) error {
		raw = value.(*observedConn)
		transaction, err := raw.BeginTx(ctx, driver.TxOptions{})
		if err == nil {
			tx = transaction.(*observedTransaction)
		}
		return err
	}); err != nil {
		cancel()
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, "INSERT INTO busy_probe VALUES (1)", nil); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := tx.Commit(); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled pre-enqueue Commit", err)
	}
	select {
	case <-tx.finished:
	case <-time.After(time.Second):
		t.Fatal("pre-enqueue Commit never acknowledged rollback")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM busy_probe").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("canceled pre-enqueue Commit persisted a write")
	}
}

func sqliteOwnershipConnection(t *testing.T, state *sqliteOwnershipState, ctx context.Context) *observedConn {
	t.Helper()
	conn, err := (observedConnector{Connector: sqliteOwnershipConnector{state}, dialect: dialect.SQLite}).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observed := conn.(*observedConn)
	t.Cleanup(func() {
		state.unblock()
		state.unblockClose()
		_ = observed.Close()
		select {
		case <-observed.sqlite.closed:
		case <-time.After(time.Second):
			t.Error("native connection cleanup did not finish")
		}
	})
	return observed
}

func TestSQLiteNativeContextRetainsValuesWithoutCancellation(t *testing.T) {
	type valueKey struct{}
	for _, operation := range []string{"exec", "query", "prepare", "begin", "prepared_exec", "prepared_query"} {
		t.Run(operation, func(t *testing.T) {
			state := newSQLiteOwnershipState()
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), valueKey{}, "caller value"), time.Second)
			defer cancel()
			conn := sqliteOwnershipConnection(t, state, ctx)
			if err := conn.ResetSession(ctx); err != nil {
				t.Fatal(err)
			}
			if err := conn.Ping(ctx); err != nil {
				t.Fatal(err)
			}
			var stmt driver.Stmt
			if operation == "prepared_exec" || operation == "prepared_query" {
				var err error
				stmt, err = conn.PrepareContext(ctx, "blocked_"+operation)
				if err != nil {
					t.Fatal(err)
				}
			}
			result := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "exec":
					_, err = conn.ExecContext(ctx, "blocked_write", []driver.NamedValue{{Ordinal: 1, Value: []byte("payload")}})
				case "query":
					_, err = conn.QueryContext(ctx, "blocked_query", nil)
				case "prepare":
					_, err = conn.PrepareContext(ctx, "blocked_prepare")
				case "begin":
					_, err = conn.BeginTx(context.WithValue(ctx, sqliteBlockedBeginKey{}, true), driver.TxOptions{})
				case "prepared_exec":
					_, err = stmt.(driver.StmtExecContext).ExecContext(ctx, nil)
				case "prepared_query":
					_, err = stmt.(driver.StmtQueryContext).QueryContext(ctx, nil)
				}
				result <- err
			}()
			select {
			case <-state.entered:
			case <-time.After(time.Second):
				t.Fatal("native operation did not start")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrSQLiteOperationPending) || errors.Is(err, driver.ErrBadConn) {
					t.Fatal("canceled operation lost its unknown-result contract", err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("caller cancellation waited for native completion")
			}
			if stmt != nil {
				if err := stmt.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			if state.closeCalls.Load() != 0 || state.rowsCloseCalls.Load() != 0 || state.stmtCloseCalls.Load() != 0 || state.rollbackCalls.Load() != 0 {
				t.Fatal("native object closed before pending operation acknowledged completion")
			}
			state.unblock()
			select {
			case <-conn.sqlite.closed:
			case <-time.After(time.Second):
				t.Fatal("final stop job did not finish")
			}
			if _, err := conn.ExecContext(context.Background(), "replay", nil); !errors.Is(err, ErrSQLiteConnectionDiscarded) {
				t.Fatal("quarantined connection accepted another operation", err)
			}
			if state.nativeOverlap.Load() || state.closedEarly.Load() || state.closeCalls.Load() != 1 {
				t.Fatal("native connection cleanup overlapped or ran more than once")
			}
			wantRows, wantStmt, wantRollback := int32(0), int32(0), int32(0)
			switch operation {
			case "query":
				wantRows = 1
			case "prepare", "prepared_exec":
				wantStmt = 1
			case "prepared_query":
				wantRows, wantStmt = 1, 1
			case "begin":
				wantRollback = 1
			}
			if state.rowsCloseCalls.Load() != wantRows || state.stmtCloseCalls.Load() != wantStmt || state.rollbackCalls.Load() != wantRollback {
				t.Fatalf("abandoned %s cleanup counts: rows=%d stmt=%d rollback=%d", operation, state.rowsCloseCalls.Load(), state.stmtCloseCalls.Load(), state.rollbackCalls.Load())
			}
			seen := make(map[string]bool)
			for len(state.contexts) > 0 {
				call := <-state.contexts
				seen[call.operation] = true
				if _, deadline := call.ctx.Deadline(); deadline || call.ctx.Done() != nil || call.ctx.Err() != nil || call.ctx.Value(valueKey{}) != "caller value" {
					t.Errorf("%s native context retained cancellation or lost caller values", call.operation)
				}
			}
			if !seen[operation] || !seen["connect"] || !seen["reset"] || !seen["ping"] {
				t.Fatal("native context coverage incomplete", seen)
			}
		})
	}
}

func TestSQLiteNativeCloseErrorsAreCachedOnce(t *testing.T) {
	for _, object := range []string{"rows", "stmt"} {
		for _, owner := range []string{"explicit", "connection"} {
			t.Run(object+"/"+owner, func(t *testing.T) {
				state := newSQLiteOwnershipState()
				closeErr := errors.New("native close failed")
				conn := sqliteOwnershipConnection(t, state, t.Context())
				var closeObject func() error
				var calls *atomic.Int32
				if object == "rows" {
					state.rowsCloseErr = closeErr
					rows, err := conn.QueryContext(t.Context(), "rows", nil)
					if err != nil {
						t.Fatal(err)
					}
					closeObject, calls = rows.Close, &state.rowsCloseCalls
				} else {
					state.stmtCloseErr = closeErr
					stmt, err := conn.PrepareContext(t.Context(), "stmt")
					if err != nil {
						t.Fatal(err)
					}
					closeObject, calls = stmt.Close, &state.stmtCloseCalls
				}
				if owner == "explicit" {
					for range 2 {
						if err := closeObject(); !errors.Is(err, closeErr) {
							t.Fatal("explicit Close did not retain native close error", err)
						}
					}
				}
				err := conn.Close()
				if owner == "connection" && !errors.Is(err, closeErr) {
					t.Fatal("final stop job did not report native object close error", err)
				}
				for range 2 {
					if err := closeObject(); !errors.Is(err, closeErr) {
						t.Fatal("Close after worker stop lost native close error", err)
					}
				}
				if calls.Load() != 1 || state.closeCalls.Load() != 1 || state.nativeOverlap.Load() || len(conn.sqlite.rows) != 0 || len(conn.sqlite.statements) != 0 {
					t.Fatal("native close lifecycle ran more than once or retained tracked objects")
				}
			})
		}
	}
}

func TestSQLiteQuarantineOwnsRowsStmtAndCloseErrors(t *testing.T) {
	state := newSQLiteOwnershipState()
	state.rowsCloseErr, state.stmtCloseErr = errors.New("rows close failed"), errors.New("stmt close failed")
	conn := sqliteOwnershipConnection(t, state, t.Context())
	stmt, err := conn.PrepareContext(t.Context(), "blocked_rows")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rows, err := stmt.(driver.StmtQueryContext).QueryContext(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- rows.Next(values) }()
	<-state.entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrSQLiteOperationPending) {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Rows.Next waited for native completion")
	}
	for _, closeObject := range []func() error{rows.Close, stmt.Close, conn.Close} {
		if err := closeObject(); err != nil {
			t.Fatal("quarantine Close blocked on or guessed a pending native result", err)
		}
	}
	if state.rowsCloseCalls.Load() != 0 || state.stmtCloseCalls.Load() != 0 || state.closeCalls.Load() != 0 {
		t.Fatal("explicit Close bypassed pending native ownership")
	}
	state.unblock()
	select {
	case <-conn.sqlite.closed:
	case <-time.After(time.Second):
		t.Fatal("quarantine final stop job did not finish")
	}
	for range 2 {
		if err := rows.Close(); !errors.Is(err, state.rowsCloseErr) {
			t.Fatal("quarantined rows lost native close error", err)
		}
		if err := stmt.Close(); !errors.Is(err, state.stmtCloseErr) {
			t.Fatal("quarantined statement lost native close error", err)
		}
	}
	if !errors.Is(conn.sqlite.closeErr, state.rowsCloseErr) || !errors.Is(conn.sqlite.closeErr, state.stmtCloseErr) {
		t.Fatal("final stop job lost native object close errors", conn.sqlite.closeErr)
	}
	if state.rowsCloseCalls.Load() != 1 || state.stmtCloseCalls.Load() != 1 || state.closeCalls.Load() != 1 || state.nativeOverlap.Load() || len(conn.sqlite.rows) != 0 || len(conn.sqlite.statements) != 0 {
		t.Fatal("quarantine cleanup ran more than once, overlapped, or retained tracked objects")
	}
}
