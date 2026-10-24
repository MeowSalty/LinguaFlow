package v015

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

type allocationEvidence struct {
	Name   string `json:"name"`
	Table  string `json:"table"`
	Column string `json:"column,omitempty"`
	Last   int64  `json:"last"`
	Called bool   `json:"called,omitempty"`
}

func CaptureAllocationBaseline(ctx context.Context, db *sql.DB, p *operation.Plan, b *operation.Baseline) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var evidence []allocationEvidence
	if p.Source.Driver == "sqlite" {
		rows, err := tx.QueryContext(ctx, "SELECT name,seq FROM sqlite_sequence ORDER BY name")
		if err != nil {
			return err
		}
		for rows.Next() {
			var item allocationEvidence
			if err := rows.Scan(&item.Name, &item.Last); err != nil {
				rows.Close()
				return err
			}
			item.Table = item.Name
			evidence = append(evidence, item)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	} else {
		tables, err := inspectTables(ctx, tx, p.Source.Driver)
		if err != nil {
			return err
		}
		sequences, err := readPostgresSequences(ctx, tx, tables)
		if err != nil {
			return err
		}
		for _, sequence := range sequences {
			evidence = append(evidence, allocationEvidence{Name: sequence.Name, Table: sequence.Table, Column: sequence.Column, Last: sequence.Last, Called: sequence.Called})
		}
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	if b.Facts == nil {
		b.Facts = map[string]string{}
	}
	b.Facts["allocation"] = string(data)
	return nil
}

// VerifyAllocationBaselineClient protects high-water marks of released tables.
// Later transaction retries may consume IDs, but cannot make old IDs reusable.
func VerifyAllocationBaselineClient(ctx context.Context, c *ent.Client, p *operation.Plan) error {
	if p.Baseline == nil {
		return operation.Fail(operation.BackupIncomplete, "allocation baseline is missing")
	}
	data, ok := p.Baseline.Facts["allocation"]
	if !ok {
		return nil
	} // direct conversion unit fixtures predate this route evidence.
	var evidence []allocationEvidence
	if err := json.Unmarshal([]byte(data), &evidence); err != nil {
		return operation.Fail(operation.Incompatible, "allocation baseline is invalid")
	}
	for _, item := range evidence {
		if item.Name == "" || item.Table == "" || strings.ContainsAny(item.Name, "\x00") || item.Last < 0 {
			return operation.Fail(operation.Incompatible, "invalid allocation identity")
		}
		var current int64
		var called bool
		if p.Source.Driver == "sqlite" {
			if err := allocationRow(ctx, c, "SELECT seq FROM sqlite_sequence WHERE name=?", []any{item.Name}, &current); err != nil {
				return operation.Fail(operation.BaselineChanged, "released SQLite allocation disappeared")
			}
		} else {
			if item.Column == "" {
				return operation.Fail(operation.Incompatible, "PostgreSQL allocation owner is missing")
			}
			var owner string
			if err := allocationRow(ctx, c, "SELECT pg_get_serial_sequence($1,$2)", []any{"public." + quoteIdentifier(item.Table), item.Column}, &owner); err != nil || (owner != "public."+item.Name && owner != "public."+quoteIdentifier(item.Name)) {
				return operation.Fail(operation.BaselineChanged, "released PostgreSQL allocation owner changed")
			}
			if err := allocationRow(ctx, c, "SELECT last_value,is_called FROM "+quoteIdentifier("public")+"."+quoteIdentifier(item.Name), nil, &current, &called); err != nil {
				return operation.Fail(operation.BaselineChanged, "released PostgreSQL allocation disappeared")
			}
		}
		if current < item.Last || p.Source.Driver == "postgres" && current == item.Last && item.Called && !called {
			return operation.Fail(operation.BaselineChanged, "migration lowered a released allocation high-water mark")
		}
	}
	return nil
}

func allocationRow(ctx context.Context, c *ent.Client, query string, args []any, dest ...any) error {
	rows, err := c.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := rows.Scan(dest...); err != nil {
		return err
	}
	if rows.Next() {
		return operation.Fail(operation.Incompatible, "allocation identity is not unique")
	}
	return rows.Err()
}

func sqliteAllocations(ctx context.Context, c *ent.Client) ([]allocationEvidence, error) {
	rows, err := c.QueryContext(ctx, "SELECT name,seq FROM sqlite_sequence ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var evidence []allocationEvidence
	for rows.Next() {
		var item allocationEvidence
		if err := rows.Scan(&item.Name, &item.Last); err != nil {
			return nil, err
		}
		evidence = append(evidence, item)
	}
	return evidence, errors.Join(rows.Err(), rows.Close())
}

// Ent may rebuild a table while applying its final constraints. Restore the
// released allocator after every schema rebuild, within the schema transaction.
func restoreSQLiteAllocations(ctx context.Context, c *ent.Client, evidence []allocationEvidence) error {
	for _, item := range evidence {
		result, err := c.ExecContext(ctx, "UPDATE sqlite_sequence SET seq=MAX(seq,?) WHERE name=?", item.Last, item.Name)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := c.ExecContext(ctx, "INSERT INTO sqlite_sequence(name,seq) VALUES(?,?)", item.Name, item.Last); err != nil {
				return err
			}
		}
	}
	return nil
}
