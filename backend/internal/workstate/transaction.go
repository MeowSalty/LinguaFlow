package workstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/ent/runtime"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
)

type TransactionError struct {
	Phase    string
	Outcome  string
	Primary  error
	Rollback error
	Cleanup  error
}

func (e *TransactionError) Error() string {
	return fmt.Sprintf("transaction %s (%s): %v", e.Phase, e.Outcome, errors.Join(e.Primary, e.Rollback, e.Cleanup))
}
func (e *TransactionError) Unwrap() []error {
	var result []error
	for _, err := range []error{e.Primary, e.Rollback, e.Cleanup} {
		if err != nil {
			result = append(result, err)
		}
	}
	return result
}
func (e *TransactionError) Permanent() bool { return e.Outcome == "unknown" }
func (e *TransactionError) Uncertain() bool { return e.Outcome == "unknown" }

func unknownCommit(err error) bool {
	var tx *TransactionError
	return errors.As(err, &tx) && tx.Outcome == "unknown"
}

// Transaction retries only errors proving that the transaction did not commit.
// Connection failures during COMMIT are returned for identity-based verification.
func Transaction(ctx context.Context, client *ent.Client, fn func(*ent.Client) error) error {
	deadline := persistence.Deadline(ctx)
	if deadline.IsZero() {
		deadline = time.Now().Add(120 * time.Second)
	}
	ctx = persistence.WithDeadline(ctx, deadline)
	ctx = persistence.WithExecutionTimeout(ctx, persistence.ExecutionTimeout(ctx))
	budgetCtx, cancelBudget := context.WithDeadline(ctx, deadline)
	defer cancelBudget()
	ctx = budgetCtx
	maxAttempts := 4
	if persistence.RetryOwned(ctx) {
		maxAttempts = 1
	}
	for attempt := 0; ; attempt++ {
		started := time.Now()
		at := minTime(time.Now().Add(persistence.ExecutionTimeout(ctx)), deadline)
		attemptCtx, cancel := context.WithDeadline(ctx, at)
		phaseStarted := time.Now()
		tx, err := client.Tx(attemptCtx)
		persistence.ObserveTransaction(ctx, "begin", "", time.Since(phaseStarted), err)
		if err != nil {
			cancel()
			err = &TransactionError{Phase: "begin", Outcome: "not_committed", Primary: err}
			persistence.ObserveTransaction(ctx, "transaction", "not_committed", time.Since(started), err)
			return err
		}
		// The callback's context is supplied by its owner; the driver transaction
		// is bounded here as well, including direct callers outside Save.
		phaseStarted = time.Now()
		err = fn(tx.Client())
		persistence.ObserveTransaction(ctx, "execute", "", time.Since(phaseStarted), err)
		if err == nil {
			phaseStarted = time.Now()
			err = tx.Commit()
			persistence.ObserveTransaction(ctx, "commit", "", time.Since(phaseStarted), err)
			phaseStarted = time.Now()
			cleanup := tx.Rollback()
			if errors.Is(cleanup, sql.ErrTxDone) {
				cleanup = nil
			}
			persistence.ObserveTransaction(ctx, "cleanup", "", time.Since(phaseStarted), cleanup)
			if err != nil {
				outcome := "unknown"
				if transactionConflict(err) {
					outcome = "not_committed"
				}
				err = &TransactionError{Phase: "commit", Outcome: outcome, Primary: err, Cleanup: cleanup}
			}
		} else {
			phaseStarted = time.Now()
			rollbackErr := tx.Rollback()
			persistence.ObserveTransaction(ctx, "rollback", "", time.Since(phaseStarted), rollbackErr)
			if errors.Is(rollbackErr, sql.ErrTxDone) && attemptCtx.Err() != nil {
				rollbackErr = nil
			}
			outcome := "not_committed"
			if rollbackErr != nil {
				outcome = "unknown"
			}
			err = &TransactionError{Phase: "execute", Outcome: outcome, Primary: err, Rollback: rollbackErr}
		}
		cancel()
		outcome := "committed"
		if err != nil {
			var txErr *TransactionError
			if errors.As(err, &txErr) {
				outcome = txErr.Outcome
			}
		}
		persistence.ObserveTransaction(ctx, "transaction", outcome, time.Since(started), err)
		if err == nil || attempt+1 >= maxAttempts || ctx.Err() != nil || !time.Now().Before(deadline) || !transactionConflict(err) || unknownCommit(err) {
			return err
		}
		timer := time.NewTimer(time.Duration(100<<attempt) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func operationContext(ctx context.Context) context.Context {
	if persistence.Deadline(ctx).IsZero() {
		return persistence.WithDeadline(ctx, time.Now().Add(120*time.Second))
	}
	return ctx
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
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
