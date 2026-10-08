package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
)

const organizationMutationAttempts = 5

// withOrganizationMutation serializes writes against the organization before
// reading any authorization. PostgreSQL takes a row write lock; SQLite takes a
// database write lock before establishing a read snapshot. Callbacks must use
// the provided client and recheck authorization on every attempt.
func withOrganizationMutation(ctx context.Context, client *ent.Client, orgID int, mutate func(*ent.Client) error) error {
	if orgID <= 0 {
		return ErrInvalidInput
	}
	return withOrganizationTransaction(ctx, client, func(txClient *ent.Client) error {
		count, err := txClient.Organization.Update().Where(organization.IDEQ(orgID)).SetUpdatedAt(time.Now().UTC()).Save(ctx)
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrOrganizationNotFound
		}
		return mutate(txClient)
	})
}

func withOrganizationTransaction(ctx context.Context, client *ent.Client, mutate func(*ent.Client) error) error {
	for attempt := 0; attempt < organizationMutationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := organizationTransactionAttempt(ctx, client, mutate)
		if err != nil && ctx.Err() != nil {
			// Cancellation may let database/sql start automatic rollback before
			// our explicit Rollback. Preserve any uncertain rollback diagnostic,
			// while reporting cancellation and never retrying the operation.
			return errors.Join(ctx.Err(), err)
		}
		if err == nil || !isOrganizationTransactionConflict(err) || attempt == organizationMutationAttempts-1 {
			return err
		}
		timer := time.NewTimer(time.Duration(10*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func organizationTransactionAttempt(ctx context.Context, client *ent.Client, mutate func(*ent.Client) error) error {
	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() // Also closes failed commits; harmless after success.
	if err := mutate(tx.Client()); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			// A failed rollback has uncertain state and must not be replayed.
			return &organizationRollbackError{cause: err, rollback: rollbackErr}
		}
		return err
	}
	return tx.Commit()
}

type organizationRollbackError struct {
	cause    error
	rollback error
}

func (e *organizationRollbackError) Error() string {
	return "organization transaction rollback failed: " + e.rollback.Error() + "; operation: " + e.cause.Error()
}

func isOrganizationTransactionConflict(err error) bool {
	// Do not replay network errors or ambiguous commit outcomes. These codes
	// specifically identify transactions that did not successfully commit.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01" || pgErr.Code == "55P03"
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code() & 0xff
		return code == 5 || code == 6 // SQLITE_BUSY, SQLITE_LOCKED and extended codes.
	}
	return false
}
