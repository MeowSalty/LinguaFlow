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
}

func newSQLiteOwnershipState() *sqliteOwnershipState {
	return &sqliteOwnershipState{entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), arguments: make(chan []byte, 1), bytes: []byte("native")}
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
func (c sqliteOwnershipConnector) Connect(context.Context) (driver.Conn, error) {
	return &sqliteOwnershipConn{state: c.state, id: c.state.connects.Add(1)}, nil
}

type sqliteOwnershipConn struct {
	state *sqliteOwnershipState
	id    int32
}

func (*sqliteOwnershipConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*sqliteOwnershipConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (*sqliteOwnershipConn) Ping(context.Context) error          { return nil }
func (*sqliteOwnershipConn) ResetSession(context.Context) error  { return nil }
func (*sqliteOwnershipConn) IsValid() bool                       { return true }
func (c *sqliteOwnershipConn) Close() error {
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
func (c *sqliteOwnershipConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query == "blocked_write" {
		c.state.block()
		c.state.arguments <- append([]byte(nil), args[0].Value.([]byte)...)
		c.state.finished.Store(true)
	}
	return driver.RowsAffected(1), nil
}
func (c *sqliteOwnershipConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	return &sqliteOwnershipRows{state: c.state, query: query}, nil
}

type sqliteOwnershipRows struct {
	state    *sqliteOwnershipState
	query    string
	position int
}

func (*sqliteOwnershipRows) Columns() []string { return []string{"value"} }
func (r *sqliteOwnershipRows) Close() error {
	if r.query == "blocked_rows" {
		clear(r.state.bytes)
	}
	return nil
}
func (r *sqliteOwnershipRows) Next(values []driver.Value) error {
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
	if state.closedEarly.Load() {
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
	if state.closedEarly.Load() {
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
