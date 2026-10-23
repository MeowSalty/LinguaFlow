package operation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/ent/runtime"
)

const stateTable = "linguaflow_migration_state"
const stepsTable = "linguaflow_migration_steps"

// Initialize reserves an isolated target before changing its application schema.
// Normal service startup rejects this reservation until the final transaction.
func Initialize(ctx context.Context, db *sql.DB, driver string, p *Plan) error {
	return transaction(ctx, db, driver, false, func(tx *sql.Tx, _ *ent.Client) error {
		for _, ddl := range []string{
			"CREATE TABLE IF NOT EXISTS " + stateTable + " (id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1), version INTEGER NOT NULL, operation_id VARCHAR(32) NOT NULL, plan_digest VARCHAR(64) NOT NULL, target_signature VARCHAR(64) NOT NULL, state VARCHAR(16) NOT NULL)",
			"CREATE TABLE IF NOT EXISTS " + stepsTable + " (operation_id VARCHAR(32) NOT NULL, step VARCHAR(32) NOT NULL, plan_digest VARCHAR(64) NOT NULL, target_signature VARCHAR(64) NOT NULL, build VARCHAR(128) NOT NULL, committed_at VARCHAR(40) NOT NULL, PRIMARY KEY (operation_id, step))",
		} {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+stateTable).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			_, err := tx.ExecContext(ctx, bind(driver, "INSERT INTO "+stateTable+" (id,version,operation_id,plan_digest,target_signature,state) VALUES (1,1,?,?,?,'preparing')"), p.ID, Digest(p), p.TargetSignature)
			return err
		}
		_, err := checkReservation(ctx, tx, p)
		return err
	})
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkReservation(ctx context.Context, db queryer, p *Plan) (string, error) {
	rows, err := db.QueryContext(ctx, "SELECT id,version,operation_id,plan_digest,target_signature,state FROM "+stateTable)
	if err != nil {
		return "", Fail(Incompatible, "target reservation is unavailable; preserve the target")
	}
	defer rows.Close()
	var state string
	count := 0
	for rows.Next() {
		var id, version int
		var operationID, digest, signature string
		if err := rows.Scan(&id, &version, &operationID, &digest, &signature, &state); err != nil {
			return "", err
		}
		if id != 1 || version != Version || operationID != p.ID || digest != Digest(p) || signature != p.TargetSignature || (state != "preparing" && state != "ready" && state != "abandoned") {
			return "", Fail(Incompatible, "target does not belong to this immutable plan")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if count != 1 {
		return "", Fail(Incompatible, "invalid target reservation")
	}
	return state, nil
}

// Steps reads authoritative committed receipts. Journal updates may lag a commit.
func Steps(ctx context.Context, db *sql.DB, p *Plan) ([]string, string, error) {
	state, err := checkReservation(ctx, db, p)
	if err != nil {
		return nil, "", err
	}
	rows, err := db.QueryContext(ctx, bind(p.Source.Driver, "SELECT operation_id,step,plan_digest,target_signature,build FROM "+stepsTable+" WHERE operation_id=? ORDER BY committed_at,step"), p.ID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var steps []string
	for rows.Next() {
		var id, step, digest, signature, build string
		if err := rows.Scan(&id, &step, &digest, &signature, &build); err != nil {
			return nil, "", err
		}
		if id != p.ID || digest != Digest(p) || signature != p.TargetSignature || build != p.Build || !validStep(step) {
			return nil, "", Fail(Incompatible, "invalid database step receipt")
		}
		steps = append(steps, step)
	}
	return steps, state, rows.Err()
}

// ReserveStorage starts a new offline import on a complete current database.
// Prior receipts stay retained; only a valid ready reservation can be replaced.
func ReserveStorage(ctx context.Context, db *sql.DB, p *Plan) error {
	if p.Kind != "storage" {
		return Fail(Incompatible, "storage reservation requires a storage plan")
	}
	return transaction(ctx, db, p.Source.Driver, false, func(tx *sql.Tx, _ *ent.Client) error {
		for _, ddl := range []string{
			"CREATE TABLE IF NOT EXISTS " + stateTable + " (id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1), version INTEGER NOT NULL, operation_id VARCHAR(32) NOT NULL, plan_digest VARCHAR(64) NOT NULL, target_signature VARCHAR(64) NOT NULL, state VARCHAR(16) NOT NULL)",
			"CREATE TABLE IF NOT EXISTS " + stepsTable + " (operation_id VARCHAR(32) NOT NULL, step VARCHAR(32) NOT NULL, plan_digest VARCHAR(64) NOT NULL, target_signature VARCHAR(64) NOT NULL, build VARCHAR(128) NOT NULL, committed_at VARCHAR(40) NOT NULL, PRIMARY KEY (operation_id, step))",
		} {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
		var id, version int
		var oldID, digest, signature, state string
		err := tx.QueryRowContext(ctx, "SELECT id,version,operation_id,plan_digest,target_signature,state FROM "+stateTable).Scan(&id, &version, &oldID, &digest, &signature, &state)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, bind(p.Source.Driver, "INSERT INTO "+stateTable+"(id,version,operation_id,plan_digest,target_signature,state) VALUES(1,1,?,?,?,'preparing')"), p.ID, Digest(p), p.TargetSignature)
			return err
		}
		if err != nil {
			return err
		}
		if oldID == p.ID {
			_, err := checkReservation(ctx, tx, p)
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+stateTable).Scan(&count); err != nil {
			return err
		}
		if count != 1 || id != 1 || version != 1 || !ValidHex(oldID, 32) || !ValidHex(digest, 64) || !ValidHex(signature, 64) || state != "ready" {
			return Fail(NeedsAction, "current database contains an unfinished reservation")
		}
		_, err = tx.ExecContext(ctx, bind(p.Source.Driver, "UPDATE "+stateTable+" SET operation_id=?,plan_digest=?,target_signature=?,state='preparing' WHERE id=1 AND state='ready'"), p.ID, Digest(p), p.TargetSignature)
		return err
	})
}

func validStep(step string) bool {
	switch step {
	case "schema", "accounts", "history", "storage", "ready":
		return true
	}
	return false
}

// Step commits domain changes and their receipt in one database transaction.
// schema rebuilds use one pinned SQLite connection with FK checks restored after
// commit and an explicit foreign_key_check before the receipt is written.
func Step(ctx context.Context, db *sql.DB, driver string, p *Plan, step string, change func(*ent.Client) error) error {
	if !validStep(step) {
		return Fail(Incompatible, "unknown database step")
	}
	return transaction(ctx, db, driver, step == "schema", func(tx *sql.Tx, client *ent.Client) error {
		state, err := checkReservation(ctx, tx, p)
		if err != nil {
			return err
		}
		var digest, signature, build string
		err = tx.QueryRowContext(ctx, bind(driver, "SELECT plan_digest,target_signature,build FROM "+stepsTable+" WHERE operation_id=? AND step=?"), p.ID, step).Scan(&digest, &signature, &build)
		if err == nil {
			if digest != Digest(p) || signature != p.TargetSignature || build != p.Build {
				return Fail(Incompatible, "step receipt was replaced")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if state != "preparing" {
			return Fail(Incompatible, "target is no longer preparing")
		}
		if err := change(client); err != nil {
			return err
		}
		if driver == "sqlite" && step == "schema" {
			rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
			if err != nil {
				return err
			}
			invalid := rows.Next()
			err = errors.Join(rows.Err(), rows.Close())
			if err != nil {
				return err
			}
			if invalid {
				return Fail(NeedsAction, "target contains a broken ownership reference")
			}
		}
		_, err = tx.ExecContext(ctx, bind(driver, "INSERT INTO "+stepsTable+" (operation_id,step,plan_digest,target_signature,build,committed_at) VALUES (?,?,?,?,?,?)"), p.ID, step, Digest(p), p.TargetSignature, p.Build, time.Now().UTC().Format(time.RFC3339Nano))
		return err
	})
}

// MarkReady is called only by the final verified domain step.
func MarkReady(ctx context.Context, client *ent.Client, p *Plan) error {
	result, err := client.ExecContext(ctx, bind(p.Source.Driver, "UPDATE "+stateTable+" SET state='ready' WHERE id=1 AND operation_id=? AND plan_digest=? AND state='preparing'"), p.ID, Digest(p))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return Fail(Incompatible, "target readiness changed")
	}
	return nil
}

func Abandon(ctx context.Context, db *sql.DB, driver string, p *Plan) error {
	return transaction(ctx, db, driver, false, func(tx *sql.Tx, _ *ent.Client) error {
		if _, err := checkReservation(ctx, tx, p); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, bind(driver, "UPDATE "+stateTable+" SET state='abandoned' WHERE id=1 AND operation_id=? AND plan_digest=?"), p.ID, Digest(p))
		return err
	})
}

func transaction(ctx context.Context, db *sql.DB, driver string, rebuild bool, change func(*sql.Tx, *ent.Client) error) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	if rebuild && driver == "sqlite" {
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
			return err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_, restoreErr := conn.ExecContext(cleanup, "PRAGMA foreign_keys=ON")
			err = errors.Join(err, restoreErr)
		}()
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	drv := &transactionDriver{Driver: entsql.NewDriver(database.DialectFor(driver), entsql.Conn{ExecQuerier: tx}), tx: tx, rebuild: rebuild && driver == "sqlite"}
	client := ent.NewClient(ent.Driver(database.NewDriver(drv)))
	if err := change(tx, client); err != nil {
		return err
	}
	return tx.Commit()
}

type transactionDriver struct {
	*entsql.Driver
	tx      *sql.Tx
	rebuild bool
}

// Ent's SQLite adapter insists on enabled FKs before it starts its own rebuild
// transaction. This isolated outer transaction already disabled them before
// BEGIN and owns their restoration and the real foreign_key_check. Satisfy only
// the adapter's initialization query; every integrity check still uses the DB.
func (d *transactionDriver) Query(ctx context.Context, query string, args, result any) error {
	if d.rebuild && query == "PRAGMA foreign_keys" {
		query = "SELECT 1"
	}
	return d.Driver.Query(ctx, query, args, result)
}
func (d *transactionDriver) Tx(context.Context) (dialect.Tx, error) { return d, nil }
func (d *transactionDriver) BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error) {
	return d, nil
}
func (*transactionDriver) Commit() error   { return nil }
func (*transactionDriver) Rollback() error { return nil }
func (*transactionDriver) Close() error    { return nil }

func bind(driver, query string) string {
	if driver != "postgres" {
		return query
	}
	output := ""
	parameter := 0
	for _, c := range query {
		if c == '?' {
			parameter++
			output += fmt.Sprintf("$%d", parameter)
		} else {
			output += string(c)
		}
	}
	return output
}
