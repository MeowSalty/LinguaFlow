package workstate

import (
	"context"
	"errors"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/ent/runtime"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
)

// Transaction retries only errors proving that the transaction did not commit.
// Connection failures during COMMIT are returned for identity-based verification.
func Transaction(ctx context.Context, client *ent.Client, fn func(*ent.Client) error) error {
	for attempt := 0; ; attempt++ {
		tx, err := client.Tx(ctx)
		if err != nil {
			return err
		}
		err = fn(tx.Client())
		if err == nil {
			err = tx.Commit()
			_ = tx.Rollback()
		} else if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		if err == nil || attempt >= 4 || ctx.Err() != nil || !transactionConflict(err) {
			return err
		}
		timer := time.NewTimer(time.Duration(10<<attempt) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func transactionConflict(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "40001" || pg.Code == "40P01" || pg.Code == "55P03"
	}
	var sq *sqlite.Error
	if errors.As(err, &sq) {
		return sq.Code()&255 == 5 || sq.Code()&255 == 6
	}
	return false
}
