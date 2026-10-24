package v015

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	entschema "entgo.io/ent/dialect/sql/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	source "github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015/source"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"golang.org/x/crypto/bcrypt"
)

// Requires three explicitly provisioned empty disposable databases. A skipped
// test is unverified, and must never promote the PostgreSQL support cell.
func TestPostgresServeReleaseDumpRestoreAcceptance(t *testing.T) {
	dsns := []string{os.Getenv("LINGUAFLOW_MIGRATION_TEST_POSTGRES_SOURCE_CLONE"), os.Getenv("LINGUAFLOW_MIGRATION_TEST_POSTGRES_TARGET_CLONE"), os.Getenv("LINGUAFLOW_MIGRATION_TEST_POSTGRES_REHEARSAL_CLONE")}
	for _, dsn := range dsns {
		if dsn == "" {
			t.Skip("unverified: three explicit disposable PostgreSQL DSNs and matching dump/restore tools are required")
		}
	}
	ctx := context.Background()
	root := t.TempDir()
	keys, _, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	keyData, err := credential.EncodeKeyring(keys)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "keys.json")
	if _, err := credential.PublishPrivateFile(keyPath, keyData); err != nil {
		t.Fatal(err)
	}
	configs := make([]string, 3)
	identities := map[string]bool{}
	for i, dsn := range dsns {
		cfg := config.DefaultServerConfig()
		cfg.Database = config.DatabaseConfig{Driver: "postgres", DSN: dsn}
		cfg.DataDir = filepath.Join(root, []string{"source-data", "target-data", "rehearsal-data"}[i])
		cfg.JWTSecret = "12345678901234567890123456789012"
		cfg.SSE.MaxReplayEvents = 1000
		cfg.Credentials.KeyringFile = keyPath
		cfg.Storage.Initialization.CapacityBytes.Set = true
		cfg.Storage.Initialization.LogicalLimitBytes.Set = true
		db, _, err := snapshot.Open(ctx, cfg, false)
		if err != nil {
			t.Fatal(err)
		}
		emptyErr := snapshot.EmptyPostgres(ctx, db)
		identity, idErr := snapshot.PostgresIdentity(ctx, db)
		db.Close()
		if emptyErr != nil {
			t.Fatal("acceptance refuses occupied database", emptyErr)
		}
		if idErr != nil {
			t.Fatal(idErr)
		}
		if identities[identity] {
			t.Fatal("acceptance databases overlap")
		}
		identities[identity] = true
		if i == 0 {
			if err := credential.CreatePrivateDirectory(cfg.DataDir); err != nil {
				t.Fatal(err)
			}
		}
		data, err := config.EncodeServerDocument(*cfg, config.LogConfig{Level: "info", Format: "text"})
		if err != nil {
			t.Fatal(err)
		}
		configs[i] = filepath.Join(root, []string{"source.yaml", "target.yaml", "rehearsal.yaml"}[i])
		if _, err := credential.PublishPrivateFile(configs[i], data); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: "serve", ConfigPath: &configs[0], Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	db, _, err := snapshot.Open(ctx, &resolved.Config, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := snapshot.InspectPostgresTools(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := entschema.NewMigrate(entsql.OpenDB("postgres", db))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Create(ctx, source.Tables...); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("original-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.ExecContext(ctx, "INSERT INTO users(id,created_at,updated_at,username,password_hash,email,role,active) VALUES(1,$1,$1,'OriginalAdmin',$2,'Admin@example.invalid','admin',true)", now, string(hash)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO instance_initializations(id,created_at,updated_at,version,data_version,mode) VALUES(1,$1,$1,1,2,'serve')", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO system_settings(created_at,updated_at,key,value) VALUES($1,$1,'registration_enabled','true')", now); err != nil {
		t.Fatal(err)
	}
	for _, table := range source.Tables {
		for _, column := range table.Columns {
			if !column.Increment {
				continue
			}
			if _, err := db.ExecContext(ctx, "SELECT setval(pg_get_serial_sequence($1,$2),COALESCE((SELECT MAX("+quoteIdentifier(column.Name)+") FROM "+quoteIdentifier(table.Name)+"),1),(SELECT COUNT(*)>0 FROM "+quoteIdentifier(table.Name)+"))", table.Name, column.Name); err != nil {
				t.Fatal("release fixture sequence allocation", err)
			}
		}
	}
	options := PlanOptions{Inputs: config.ServerInputs{Mode: "serve", ConfigPath: &configs[0], Environment: map[string]string{}}, Directory: filepath.Join(root, "operation"), TargetConfig: configs[1], RehearsalConfig: configs[2]}
	p, err := NewPlan(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true, Rehearse: true}); err != nil {
		t.Fatal("real rehearsal dump/restore", err)
	}
	injected := errors.New("restore committed before reservation")
	_, err = Run(ctx, RunOptions{Directory: options.Directory, Offline: true, Fault: func(at string) error {
		if at == "restore_commit" {
			return injected
		}
		return nil
	}})
	if !errors.Is(err, injected) {
		t.Fatal("restore boundary", err)
	}
	targetNarrow, err := config.ResolveMigrationDatabase(config.ServerInputs{Mode: "serve", ConfigPath: &configs[1], Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	tcfg := config.ServerConfig{Database: targetNarrow.Database, DataDir: targetNarrow.DataDir}
	clone, _, err := snapshot.Open(ctx, &tcfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clone.ExecContext(ctx, "SELECT setval(pg_get_serial_sequence('users','id'),1,false)"); err != nil {
		clone.Close()
		t.Fatal(err)
	}
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err == nil {
		clone.Close()
		t.Fatal("restore accepted changed sequence allocation")
	}
	if _, err := clone.ExecContext(ctx, "SELECT setval(pg_get_serial_sequence('users','id'),1,true)"); err != nil {
		clone.Close()
		t.Fatal(err)
	}
	clone.Close()
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
		t.Fatal("restore resume", err)
	}
	if _, err := VerifyOperation(ctx, options.Directory); err != nil {
		t.Fatal(err)
	}
	if after, err := Fingerprint(ctx, db, "postgres"); err != nil || after != p.SourceFingerprint {
		t.Fatal("dump wrote source", err)
	}
	deployment := filepath.Join(p.Target.DataDir, "deployment.yaml")
	targetInputs, err := config.ResolveServerConfig(config.ServerInputs{Mode: "serve", ConfigPath: &deployment, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	tdb, client, err := database.Open(ctx, &targetInputs.Config)
	if err != nil {
		t.Fatal("runtime startup", err)
	}
	defer tdb.Close()
	auth := service.NewAuthService(client, service.AuthConfigFromServer(targetInputs.Config), service.NewSettingsService(client))
	session, err := auth.Login(ctx, service.LoginInput{Identifier: "originaladmin", Password: "original-password-123"})
	if err != nil || session.User.ID != 1 || session.User.Role != "admin" {
		t.Fatal("original PostgreSQL administrator", err)
	}
	if _, err := RollbackOperation(ctx, options.Directory, true); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public'").Scan(&tables); err != nil || tables != len(source.Tables) {
		t.Fatal("source release schema changed", err)
	}
}
