package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
)

func TestTaskRetentionIndependentSQLiteTransactions(t *testing.T) {
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "retention.db")) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)"
	open := func() *ent.Client {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	first, second := open(), open()
	if err := first.Schema.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	runTaskRetentionCompetition(t, first, second)
}

func TestTaskRetentionIndependentPostgresTransactions(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PostgreSQL task retention transactions unverified: LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	configuration, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test DSN")
	}
	admin := stdlib.OpenDB(*configuration)
	defer admin.Close()
	schema := fmt.Sprintf("task_retention_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("remove test schema: %v", err)
		}
	}()
	open := func() *ent.Client {
		cfg := configuration.Copy()
		cfg.RuntimeParams["search_path"] = schema
		cfg.RuntimeParams["timezone"] = "UTC"
		db := stdlib.OpenDB(*cfg)
		db.SetMaxOpenConns(1)
		return ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.Postgres, db))))
	}
	first, second := open(), open()
	defer first.Close()
	defer second.Close()
	if err := first.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	runTaskRetentionCompetition(t, first, second)
}

func runTaskRetentionCompetition(t *testing.T, first, second *ent.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := NewInitializationService(first).Initialize(ctx, config.ModeServer, bootstrapForTest("race-admin", false)); err != nil {
		t.Fatal(err)
	}
	first.InstanceInitialization.UpdateOneID(1).SetDataVersion(0).ExecX(ctx)
	first.SystemSetting.Delete().Where(systemsetting.KeyEQ(SettingTaskRetention)).ExecX(ctx)
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	for _, client := range []*ent.Client{first, second} {
		go func() { <-start; errorsOut <- MigrateData(ctx, client) }()
	}
	close(start)
	for range 2 {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	if first.SystemSetting.Query().Where(systemsetting.KeyEQ(SettingTaskRetention)).CountX(ctx) != 1 || first.InstanceInitialization.GetX(ctx, 1).DataVersion != CurrentDataVersion {
		t.Fatal("concurrent migration did not converge")
	}
	actor := first.User.Query().OnlyX(ctx).ID
	var wakes atomic.Int32
	start = make(chan struct{})
	for index, client := range []*ent.Client{first, second} {
		settings := NewSettingsService(client)
		settings.OnTaskRetentionChanged(func() { wakes.Add(1) })
		go func() {
			<-start
			_, err := settings.Patch(ctx, actor, SettingsPatch{TaskRetention: &TaskRetentionPatch{Enabled: true, RetentionDays: index + 1, ExpectedRevision: 1}})
			errorsOut <- err
		}()
	}
	close(start)
	succeeded, conflicted := 0, 0
	for range 2 {
		err := <-errorsOut
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrSettingsConflict) {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 || wakes.Load() != 1 || first.ActivityLog.Query().Where(activitylog.ActionEQ("admin.task_retention.update")).CountX(ctx) != 1 {
		t.Fatalf("success=%d conflicts=%d wakes=%d", succeeded, conflicted, wakes.Load())
	}
	policy, err := NewSettingsService(first).TaskRetention(ctx)
	if err != nil || policy.Revision != 2 {
		t.Fatalf("policy=%+v %v", policy, err)
	}
}
