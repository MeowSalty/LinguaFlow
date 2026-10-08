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
