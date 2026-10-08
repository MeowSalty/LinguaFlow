package database

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

type joinedRollbackKey struct{}

// WithJoinedRollback asks BeginTx to reserve a connection until rollback has
// actually finished. database/sql may otherwise return ErrTxDone while its
// cancellation goroutine is still rolling the transaction back in the driver.
func WithJoinedRollback(ctx context.Context) context.Context {
	return context.WithValue(ctx, joinedRollbackKey{}, true)
}

func beginJoinedTransaction(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (dialect.Tx, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	return &joinedTransaction{Tx: &entsql.Tx{Conn: entsql.Conn{ExecQuerier: tx}, Tx: tx}, conn: conn}, nil
}

type joinedTransaction struct {
	*entsql.Tx
	conn     *sql.Conn
	once     sync.Once
	closeErr error
}

func (t *joinedTransaction) closeConnection() error {
	// Conn.Close waits for the transaction's release function. In particular,
	// it joins database/sql's automatic rollback before returning to the pool.
	t.once.Do(func() { t.closeErr = t.conn.Close() })
	return t.closeErr
}
func (t *joinedTransaction) Commit() error { return errors.Join(t.Tx.Commit(), t.closeConnection()) }
func (t *joinedTransaction) Rollback() error {
	return errors.Join(t.Tx.Rollback(), t.closeConnection())
}
