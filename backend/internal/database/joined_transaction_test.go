package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"
)

type rollbackJoinDriver struct{}

func (rollbackJoinDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type rollbackJoinConnector struct{ entered, release chan struct{} }

func (c *rollbackJoinConnector) Driver() driver.Driver { return rollbackJoinDriver{} }
func (c *rollbackJoinConnector) Connect(context.Context) (driver.Conn, error) {
	return &rollbackJoinConn{c: c}, nil
}

type rollbackJoinConn struct{ c *rollbackJoinConnector }

func (*rollbackJoinConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*rollbackJoinConn) Close() error                        { return nil }
func (c *rollbackJoinConn) Begin() (driver.Tx, error)         { return &rollbackJoinTx{c: c.c}, nil }
func (c *rollbackJoinConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}

type rollbackJoinTx struct {
	c    *rollbackJoinConnector
	once sync.Once
}

type retainedRollbackConnector struct {
	*rollbackJoinConnector
	closed chan struct{}
}

func (c *retainedRollbackConnector) Connect(context.Context) (driver.Conn, error) {
	return &retainedRollbackConn{rollbackJoinConn: &rollbackJoinConn{c: c.rollbackJoinConnector}, closed: c.closed}, nil
}

type retainedRollbackConn struct {
	*rollbackJoinConn
	closed chan struct{}
	once   sync.Once
}

func (*retainedRollbackConn) ResetSession(context.Context) error { return nil }
func (*retainedRollbackConn) IsValid() bool                      { return true }
func (c *retainedRollbackConn) Close() error                     { c.once.Do(func() { close(c.closed) }); return nil }

func TestJoinedRollbackDiscardsRetainedGenericDriverAfterCancellation(t *testing.T) {
	connector := &retainedRollbackConnector{rollbackJoinConnector: &rollbackJoinConnector{entered: make(chan struct{}), release: make(chan struct{})}, closed: make(chan struct{})}
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := beginJoinedTransaction(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-connector.entered
	result := make(chan error, 1)
	go func() { result <- tx.Rollback() }()
	select {
	case err := <-result:
		close(connector.release)
		t.Fatal("returned before driver rollback", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(connector.release)
	select {
	case err := <-result:
		if !errors.Is(err, sql.ErrTxDone) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("rollback did not join")
	}
	select {
	case <-connector.closed:
	default:
		t.Fatal("uncertain generic connection returned to pool")
	}
	if stats := db.Stats(); stats.InUse != 0 {
		t.Fatalf("generic retained connection leaked: %+v", stats)
	}
}

func (*rollbackJoinTx) Commit() error { return nil }
func (t *rollbackJoinTx) Rollback() error {
	t.once.Do(func() { close(t.c.entered) })
	<-t.c.release
	return nil
}

func TestJoinedRollbackWaitsForAutomaticDriverRollback(t *testing.T) {
	connector := &rollbackJoinConnector{entered: make(chan struct{}), release: make(chan struct{})}
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := beginJoinedTransaction(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-connector.entered:
	case <-time.After(time.Second):
		t.Fatal("automatic rollback did not start")
	}
	result := make(chan error, 1)
	go func() { result <- tx.Rollback() }()
	select {
	case err := <-result:
		close(connector.release)
		t.Fatalf("returned before driver rollback finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(connector.release)
	select {
	case err := <-result:
		if !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("rollback did not join")
	}
	if stats := db.Stats(); stats.InUse != 0 {
		t.Fatalf("reserved connection leaked: %+v", stats)
	}
}

func TestJoinedRollbackSharesOneCleanupBudgetAcrossRepeatedCalls(t *testing.T) {
	connector := &rollbackJoinConnector{entered: make(chan struct{}), release: make(chan struct{})}
	db := sql.OpenDB(connector)
	defer db.Close()
	tx, err := beginJoinedTransaction(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(connector.release)
		select {
		case <-tx.(*joinedTransaction).done:
		case <-time.After(time.Second):
			t.Error("owned cleanup did not finish")
		}
	}()
	if err := tx.Rollback(); !errors.Is(err, ErrTransactionCleanupTimeout) {
		t.Fatal("stuck rollback was not bounded", err)
	}
	start := time.Now()
	if err := tx.Rollback(); !errors.Is(err, ErrTransactionCleanupTimeout) || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("repeated rollback reset cleanup budget: %v after %v", err, time.Since(start))
	}
}
