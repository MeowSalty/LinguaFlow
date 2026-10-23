package v015

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

type postgresSequence struct {
	Name, Table, Column, Type                       string
	Start, Increment, Minimum, Maximum, Cache, Last int64
	Cycle, Called                                   bool
}

func readPostgresSequences(ctx context.Context, tx *sql.Tx, tables []actualTable) ([]postgresSequence, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.relname,COALESCE(t.relname,''),COALESCE(a.attname,''),pg_catalog.format_type(s.seqtypid,NULL),s.seqstart,s.seqincrement,s.seqmin,s.seqmax,s.seqcache,s.seqcycle
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_sequence s ON s.seqrelid=c.oid
LEFT JOIN pg_depend d ON d.classid='pg_class'::regclass AND d.objid=c.oid AND d.objsubid=0 AND d.refclassid='pg_class'::regclass AND d.deptype IN ('a','i')
LEFT JOIN pg_class t ON t.oid=d.refobjid LEFT JOIN pg_namespace tn ON tn.oid=t.relnamespace
LEFT JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=d.refobjsubid
WHERE n.nspname='public' AND c.relkind='S' ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	var sequences []postgresSequence
	for rows.Next() {
		var sequence postgresSequence
		if err := rows.Scan(&sequence.Name, &sequence.Table, &sequence.Column, &sequence.Type, &sequence.Start, &sequence.Increment, &sequence.Minimum, &sequence.Maximum, &sequence.Cache, &sequence.Cycle); err != nil {
			rows.Close()
			return nil, err
		}
		sequences = append(sequences, sequence)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range sequences {
		sequence := &sequences[i]
		owned := false
		for _, table := range tables {
			if table.Name != sequence.Table {
				continue
			}
			for _, column := range table.Columns {
				if column.Name == sequence.Column && column.Primary > 0 {
					owned = true
				}
			}
		}
		if !owned || sequence.Increment <= 0 || sequence.Cycle || sequence.Cache <= 0 {
			return nil, operation.Fail(operation.UnsupportedSource, "unknown or unsafe PostgreSQL allocation sequence")
		}
		if err := tx.QueryRowContext(ctx, "SELECT last_value,is_called FROM "+quoteIdentifier("public")+"."+quoteIdentifier(sequence.Name)).Scan(&sequence.Last, &sequence.Called); err != nil {
			return nil, err
		}
		var max sql.NullInt64
		if err := tx.QueryRowContext(ctx, "SELECT MAX("+quoteIdentifier(sequence.Column)+") FROM "+quoteIdentifier("public")+"."+quoteIdentifier(sequence.Table)).Scan(&max); err != nil {
			return nil, err
		}
		if max.Valid && (!sequence.Called && sequence.Last <= max.Int64 || sequence.Called && (sequence.Last > sequence.Maximum-sequence.Increment || sequence.Last+sequence.Increment <= max.Int64)) {
			return nil, operation.Fail(operation.UnsupportedSource, fmt.Sprintf("PostgreSQL sequence for %s does not preserve allocation after original IDs", sequence.Table))
		}
	}
	for _, table := range tables {
		for _, column := range table.Columns {
			allocated := column.Identity == "YES" || column.Default != nil && strings.HasPrefix(*column.Default, "nextval(")
			if !allocated {
				continue
			}
			count := 0
			for _, sequence := range sequences {
				if sequence.Table == table.Name && sequence.Column == column.Name {
					count++
				}
			}
			if count != 1 {
				return nil, operation.Fail(operation.UnsupportedSource, "PostgreSQL allocation lacks one exact owned sequence")
			}
		}
	}
	return sequences, nil
}

// Sequence state is not transactional MVCC data. Offline execution stops all
// allocators; reading it without nextval binds dump/restore's exact allocation
// state and prevents a changed sequence from passing a row-only clone check.
func fingerprintPostgresSequences(ctx context.Context, tx *sql.Tx, h hash.Hash, tables []actualTable) error {
	sequences, err := readPostgresSequences(ctx, tx, tables)
	if err != nil {
		return err
	}
	writeHash(h, sequences)
	return nil
}
