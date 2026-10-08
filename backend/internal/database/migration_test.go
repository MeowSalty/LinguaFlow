package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestMigrationPinnedConnectionSQLite(t *testing.T) {
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.MaxOpenConns = 1
	db, c, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WithMigrationLock(ctx, db, cfg.Database.Driver, func(pinned *ent.Client) error {
		if err := pinned.Schema.Create(ctx); err != nil {
			return err
		}
		tx, err := pinned.Tx(ctx)
		if err != nil {
			return err
		}
		return tx.Rollback()
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("migration closed shared pool:", err)
	}
}

func TestMigrationPinnedConnectionPostgres(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test DSN")
	}
	admin := stdlib.OpenDB(*parsed)
	defer admin.Close()
	schema := fmt.Sprintf("migration_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("cleanup migration test schema: %v", err)
		}
	}()
	parsed.RuntimeParams["search_path"] = schema
	parsed.RuntimeParams["timezone"] = "UTC"
	db := stdlib.OpenDB(*parsed)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	for range 2 {
		if err := WithMigrationLock(ctx, db, config.DatabaseDriverPostgres, func(pinned *ent.Client) error {
			if err := pinned.Schema.Create(ctx); err != nil {
				return err
			}
			tx, err := pinned.Tx(ctx)
			if err != nil {
				return err
			}
			return tx.Rollback()
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}
