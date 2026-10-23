package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrPostgresCatalogNotEmpty = errors.New("PostgreSQL catalog contains unsupported objects")

// PostgresCatalogQueryer is implemented by *sql.DB and *sql.Tx.
type PostgresCatalogQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const postgresCatalogGuardQuery = `
SELECT EXISTS (
	SELECT 1
	FROM pg_namespace
	WHERE nspname NOT IN ('pg_catalog', 'information_schema', 'public')
	  AND nspname NOT LIKE 'pg_toast%'
	  AND nspname NOT LIKE 'pg_temp_%'
	UNION ALL
	SELECT 1 FROM pg_extension WHERE extname <> 'plpgsql'
	UNION ALL
	SELECT 1 FROM pg_language WHERE lanname NOT IN ('internal', 'c', 'sql', 'plpgsql')
	UNION ALL
	SELECT 1
	FROM pg_type t
	JOIN pg_namespace n ON n.oid = t.typnamespace
	LEFT JOIN pg_class r ON r.oid = t.typrelid
	WHERE n.nspname = 'public'
	  AND NOT (t.typtype = 'b' AND t.typelem <> 0)
	  AND NOT (t.typtype = 'c' AND r.relkind IN ('r', 'p', 'v', 'm', 'f'))
	UNION ALL
	SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public'
	UNION ALL
	SELECT 1 FROM pg_operator o JOIN pg_namespace n ON n.oid = o.oprnamespace WHERE n.nspname = 'public'
	UNION ALL
	SELECT 1 FROM pg_opclass o JOIN pg_namespace n ON n.oid = o.opcnamespace WHERE n.nspname = 'public'
	UNION ALL
	SELECT 1 FROM pg_opfamily o JOIN pg_namespace n ON n.oid = o.opfnamespace WHERE n.nspname = 'public'
	UNION ALL
	SELECT 1 FROM pg_collation c JOIN pg_namespace n ON n.oid = c.collnamespace WHERE n.nspname = 'public'
	UNION ALL
	SELECT 1 FROM pg_conversion c JOIN pg_namespace n ON n.oid = c.connamespace WHERE n.nspname = 'public'
	UNION ALL
	SELECT 1 FROM pg_ts_config c JOIN pg_namespace n ON n.oid = c.cfgnamespace WHERE n.nspname = 'public'
	UNION ALL
	SELECT 1 FROM pg_ts_dict d JOIN pg_namespace n ON n.oid = d.dictnamespace WHERE n.nspname = 'public'
	UNION ALL
	SELECT 1 FROM pg_ts_parser p JOIN pg_namespace n ON n.oid = p.prsnamespace WHERE n.nspname = 'public'
	UNION ALL
	SELECT 1 FROM pg_ts_template t JOIN pg_namespace n ON n.oid = t.tmplnamespace WHERE n.nspname = 'public'
	UNION ALL
	SELECT 1 FROM pg_default_acl
	UNION ALL
	SELECT 1 FROM pg_foreign_data_wrapper
	UNION ALL
	SELECT 1 FROM pg_foreign_server
	UNION ALL
	SELECT 1 FROM pg_user_mapping
	UNION ALL
	SELECT 1 FROM pg_event_trigger
	UNION ALL
	SELECT 1 FROM pg_largeobject_metadata
	UNION ALL
	SELECT 1 FROM pg_am WHERE amname NOT IN ('heap', 'btree', 'hash', 'gist', 'gin', 'spgist', 'brin')
)
`

// CheckPostgresCatalog rejects database objects that pg_class relation checks
// and table fingerprints do not represent. A normal empty database's public
// schema and built-in plpgsql extension are allowed.
func CheckPostgresCatalog(ctx context.Context, q PostgresCatalogQueryer) error {
	if q == nil {
		return errors.New("nil PostgreSQL catalog queryer")
	}
	var unexpected bool
	if err := q.QueryRowContext(ctx, postgresCatalogGuardQuery).Scan(&unexpected); err != nil {
		return fmt.Errorf("inspect PostgreSQL catalog: %w", err)
	}
	if unexpected {
		return ErrPostgresCatalogNotEmpty
	}
	return nil
}

var (
	_ PostgresCatalogQueryer = (*sql.DB)(nil)
	_ PostgresCatalogQueryer = (*sql.Tx)(nil)
)
