package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresCatalogRejectsLiveUnfingerprintedObjects(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_MIGRATION_TEST_POSTGRES_CATALOG_DSN")
	if dsn == "" {
		t.Skip("dedicated catalog PostgreSQL DSN is not configured")
	}
	settings, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid dedicated catalog PostgreSQL DSN")
	}
	db := stdlib.OpenDB(*settings)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error("close dedicated catalog PostgreSQL connection")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("dedicated catalog PostgreSQL database is unavailable")
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	for _, test := range []struct {
		name string
		ddl  string
	}{
		{name: "enum", ddl: "CREATE TYPE public.lf_catalog_review_enum_" + suffix + " AS ENUM ('review')"},
		{name: "domain", ddl: "CREATE DOMAIN public.lf_catalog_review_domain_" + suffix + " AS text"},
		{name: "function", ddl: "CREATE FUNCTION public.lf_catalog_review_function_" + suffix + "() RETURNS integer LANGUAGE sql IMMUTABLE AS 'SELECT 1'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal("begin temporary catalog object transaction")
			}
			t.Cleanup(func() {
				if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
					t.Error("rollback temporary catalog object transaction")
				}
			})
			if _, err := tx.ExecContext(ctx, test.ddl); err != nil {
				t.Fatalf("create temporary public %s", test.name)
			}
			if err := CheckPostgresCatalog(ctx, tx); !errors.Is(err, ErrPostgresCatalogNotEmpty) {
				t.Fatalf("public %s was not rejected by catalog guard: %v", test.name, err)
			}
		})
	}
}
