package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/instanceinitialization"
	migrationcli "github.com/MeowSalty/LinguaFlow/backend/internal/migration/cli"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPublishedCheckpointAutoUpgradeThenStoragePlanAndCopy(t *testing.T) {
	postgresDSN := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	clearDeploymentEnvironment(t)
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) { testPublishedCheckpointStoragePlan(t, driver, postgresDSN) })
	}
}

func testPublishedCheckpointStoragePlan(t *testing.T, driver, postgresDSN string) {
	ctx := t.Context()
	dir := t.TempDir()
	cfg := testServerConfig()
	cfg.Mode = config.ModeLocal
	cfg.DataDir = dir
	cfg.Host = "127.0.0.1"
	cfg.SSE.MaxReplayEvents = cfg.SSE.RingBufferCapacity * 2
	if driver == "postgres" {
		cfg.Mode = config.ModeServer
		cfg.JWTSecret = strings.Repeat("j", 32)
		cfg.Storage.Initialization.CapacityBytes.Set = true
		cfg.Storage.Initialization.LogicalLimitBytes.Set = true
		cfg.Database.Driver = driver
		cfg.Database.DSN = isolatedPublishedStorageDSN(t, postgresDSN)
	}
	cfg.Credentials.KeyringFile = filepath.Join(dir, "keys.json")
	if _, err := credential.PrepareKeyring(cfg.Credentials.KeyringFile, true); err != nil {
		t.Fatal(err)
	}
	db, c, cleanup, err := prepareDatabase(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanup != nil {
			cleanup()
		}
	})
	bootstrap := config.BootstrapInput{}
	if driver == "postgres" {
		bootstrap.Admin = &config.BootstrapAdmin{Username: "published_admin", Password: "published-password-123", Email: "published@example.invalid"}
	}
	initializationMode := "serve"
	if cfg.IsLocal() {
		initializationMode = config.ModeLocal
	}
	if _, err := service.NewInitializationService(c).Initialize(ctx, initializationMode, bootstrap); err != nil {
		cleanup()
		t.Fatal(err)
	}
	actor := c.User.Query().OnlyX(ctx)
	project := c.Project.Create().SetName("published sync storage").SetOwnerUserID(actor.ID).SaveX(ctx)
	resource := c.Resource.Create().SetProjectID(project.ID).SetPath("source.txt").SetFormat("txt").SetStoragePath("source.txt").SaveX(ctx)
	segment := c.Segment.Create().SetResourceID(resource.ID).SetSegmentIndex(0).SetSourceText("fixture").SetTargetText("already new").SaveX(ctx)
	entry := c.GlossaryEntry.Create().SetProjectID(project.ID).SetSourceKey("fixture").SetSource("fixture").SetTarget("new").SaveX(ctx)
	stored := fmt.Sprintf(`{"total_updated":1,"total_skipped":0,"resources":[{"resource_id":%d,"resource_path":"source.txt","updated_count":1,"skipped_count":0}]}`, resource.ID)
	task := c.SyncTask.Create().SetProjectID(project.ID).SetEntryID(entry.ID).SetActorUserID(actor.ID).SetOldTarget("old").SetNewTarget("new").SetTotalSegments(2).SetProcessedSegments(1).SetNextSegmentIndex(1).SetCheckpointVersion(1).SetStatus("running").SetResult(stored).SaveX(ctx)
	// Restore the changed, released cp1 contract: unique serialized vectors,
	// no relational plan/results, no new total scalars or persistence marker.
	for _, query := range []string{
		"ALTER TABLE sync_tasks ADD COLUMN segment_ids TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE sync_tasks ADD COLUMN resource_ids TEXT NOT NULL DEFAULT ''",
		"DROP TABLE sync_task_items", "DROP TABLE sync_task_results",
		"ALTER TABLE sync_tasks DROP COLUMN total_updated", "ALTER TABLE sync_tasks DROP COLUMN total_skipped",
		"DROP INDEX loginbinding_evidence_expires_at_id", "ALTER TABLE login_bindings DROP COLUMN evidence_expires_at",
		"DROP TABLE linguaflow_persistence_format",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			cleanup()
			t.Fatal(query, err)
		}
	}
	if _, err := c.ExecContext(ctx, "UPDATE sync_tasks SET segment_ids=$1,resource_ids=$2 WHERE id=$3", fmt.Sprintf("[%d,999999]", segment.ID), fmt.Sprintf("[%d]", resource.ID), task.ID); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "jobs")
	if err := credential.CreatePrivateDirectory(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := credential.PublishPrivateFile(filepath.Join(legacy, "source.txt"), []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	document, err := config.EncodeServerDocument(*cfg, config.LogConfig{Level: "info", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "source.yaml")
	if _, err := credential.PublishPrivateFile(configPath, document); err != nil {
		t.Fatal(err)
	}
	operationDir := filepath.Join(t.TempDir(), "operation")
	runPlan := func() error {
		command := migrationcli.NewCommand()
		command.SetArgs([]string{"storage", "plan", "--mode", initializationMode, "--config", configPath, "--operation-dir", operationDir})
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		return command.ExecuteContext(ctx)
	}
	readSource := func() ([]byte, error) {
		if driver == "sqlite" {
			return os.ReadFile(cfg.DatabasePath())
		}
		probe, _, err := snapshot.Open(ctx, cfg, true)
		if err != nil {
			return nil, err
		}
		defer probe.Close()
		fingerprint, err := v015.BusinessFingerprint(ctx, probe, driver)
		return []byte(fingerprint), err
	}
	before, err := readSource()
	if err != nil {
		t.Fatal(err)
	}
	if err := runPlan(); !errors.Is(err, database.ErrPersistenceMigrationRequired) {
		t.Fatal("published source lacked accurate upgrade guidance", err)
	}
	after, err := readSource()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read-only old source rejection changed database", err)
	}
	if _, err := os.Stat(operationDir); !os.IsNotExist(err) {
		t.Fatal("old source rejection created operation state", err)
	}
	db, c, cleanup, err = prepareDatabase(ctx, cfg)
	if err != nil {
		t.Fatal("actual startup auto upgrade", err)
	}
	actual := c.SyncTask.GetX(ctx, task.ID)
	if actual.CheckpointVersion != 2 || actual.NextSegmentIndex != 1 || actual.TotalUpdated != 1 || actual.Result != "" {
		cleanup()
		t.Fatalf("checkpoint conversion lost receipt: %+v", actual)
	}
	if got := c.Segment.GetX(ctx, segment.ID).TargetText; got == nil || *got != "already new" {
		cleanup()
		t.Fatal("upgrade changed committed translation")
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := runPlan(); err != nil {
		t.Fatal("storage plan after explicit startup upgrade", err)
	}
	if _, err := os.Stat(filepath.Join(operationDir, "plan.json")); err != nil {
		t.Fatal("storage plan not published", err)
	}
	if driver == "postgres" {
		// PostgreSQL storage planning reads an existing consistent snapshot;
		// SQLite's physical copy contract is verified below on its real file.
		cfg.AutoMigrate = false
		_, c, cleanup, err = prepareDatabase(ctx, cfg)
		if err != nil {
			t.Fatal("upgraded PostgreSQL source failed read-only startup", err)
		}
		defer cleanup()
		if c.SyncTaskItem.Query().CountX(ctx) != 2 || c.SyncTaskResult.Query().CountX(ctx) != 1 || c.SyncTask.GetX(ctx, task.ID).TotalUpdated != 1 {
			t.Fatal("PostgreSQL storage planning lost final checkpoint facts")
		}
		return
	}
	// A real consistent SQLite copy retains the final marker and recoverable
	// plan; creating a new-schema fixture cannot prove this copy contract.
	db, _, err = snapshot.Open(ctx, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copied.sqlite")
	if err := snapshot.SQLite(ctx, db, copyPath); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	copyConfig := *cfg
	copyConfig.Database.DSN = copyPath
	copyConfig.AutoMigrate = false
	db, c, err = database.Open(ctx, &copyConfig)
	if err != nil {
		t.Fatal("copied format failed startup validation", err)
	}
	defer c.Close()
	if c.SyncTaskItem.Query().CountX(ctx) != 2 || c.SyncTask.GetX(ctx, task.ID).TotalUpdated != 1 {
		t.Fatal("copy lost final plan or receipt")
	}
}

func isolatedPublishedStorageDSN(t *testing.T, dsn string) string {
	t.Helper()
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	settings, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid private PostgreSQL test endpoint")
	}
	admin := stdlib.OpenDB(*settings)
	databaseName := fmt.Sprintf("published_storage_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(t.Context(), "CREATE DATABASE "+databaseName+" TEMPLATE template0"); err != nil {
		admin.Close()
		t.Fatal("create isolated storage database", err)
	}
	t.Cleanup(func() {
		_, err := admin.ExecContext(context.Background(), "DROP DATABASE "+databaseName)
		if err != nil {
			t.Error("clean isolated storage database", err)
		}
		admin.Close()
	})
	// The published offline contract supports only public in an otherwise
	// known catalog. The actual CLI must open our owned database too.
	// Never log the endpoint or its credentials.
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		endpoint, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("invalid private PostgreSQL URL")
		}
		query := endpoint.Query()
		query.Set("search_path", "public")
		endpoint.Path = "/" + databaseName
		endpoint.RawQuery = query.Encode()
		return endpoint.String()
	}
	return dsn + " dbname=" + databaseName + " search_path=public"
}

func TestPrepareDatabaseFutureDataVersionBeforePublishedSchemaChanges(t *testing.T) {
	ctx := t.Context()
	cfg := testServerConfig()
	cfg.DataDir = t.TempDir()
	db, c, cleanup, err := prepareDatabase(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.InstanceInitialization.Create().SetID(1).SetVersion(service.InitializationVersion).SetDataVersion(service.CurrentDataVersion + 1).SetMode(instanceinitialization.ModeServe).ExecX(ctx)
	for _, table := range []string{"sync_task_items", "sync_task_results"} {
		if _, err := db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			cleanup()
			t.Fatal(err)
		}
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfg.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, unexpected, err := prepareDatabase(ctx, cfg); !errors.Is(err, service.ErrDataMigrationRequired) {
		if unexpected != nil {
			unexpected()
		}
		t.Fatal("future data version accepted", err)
	}
	after, err := os.ReadFile(cfg.DatabasePath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("future data refusal performed schema or business writes", err)
	}
}
