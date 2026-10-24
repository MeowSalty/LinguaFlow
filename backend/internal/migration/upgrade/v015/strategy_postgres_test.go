package v015

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	entschema "entgo.io/ent/dialect/sql/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/api"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	source "github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015/source"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const postgresLegacyStoragePolicy = `{"mode":"site_only","default_choice":"site","generation":7,"historical_rule":{"enabled":false},"default_space_capacity_bytes":null,"logical_limit_bytes":null}`

func postgresAcceptanceDSN(kind, group string) string {
	name := "LINGUAFLOW_MIGRATION_TEST_POSTGRES_" + strings.ToUpper(kind) + "_" + strings.ToUpper(group)
	return os.Getenv(name)
}

func postgresToolName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func postgresUtilityEnv(t *testing.T, dsn string) []string {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse private PostgreSQL test endpoint")
	}
	env := make([]string, 0, len(os.Environ())+8)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(item), "PG") {
			env = append(env, item)
		}
	}
	return append(env,
		"PGHOST="+cfg.Host,
		fmt.Sprintf("PGPORT=%d", cfg.Port),
		"PGUSER="+cfg.User,
		"PGPASSWORD="+cfg.Password,
		"PGDATABASE="+cfg.Database,
		"PGSSLMODE=disable",
		"PGCONNECT_TIMEOUT=10",
	)
}

func runPostgresUtility(t *testing.T, dsn, tool string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), tool, args...)
	cmd.Env = postgresUtilityEnv(t, dsn)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatalf("PostgreSQL utility %s failed", filepath.Base(tool))
	}
}

func postgresStrategyFixture(t *testing.T, dsn, operationName string) (PlanOptions, *config.ServerConfig, *credential.Keyring) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	dataDir := filepath.Join(root, "source-data")
	if err := credential.CreatePrivateDirectory(dataDir); err != nil {
		t.Fatal(err)
	}
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
	cfg := config.DefaultServerConfig()
	cfg.Database = config.DatabaseConfig{Driver: "postgres", DSN: dsn}
	cfg.DataDir = dataDir
	cfg.JWTSecret = "12345678901234567890123456789012"
	cfg.SSE.MaxReplayEvents = 1000
	cfg.Credentials.KeyringFile = keyPath
	cfg.Storage.Initialization.CapacityBytes.Set = true
	cfg.Storage.Initialization.LogicalLimitBytes.Set = true
	db, _, err := snapshot.Open(ctx, cfg, false)
	if err != nil {
		t.Fatal("open isolated PostgreSQL source")
	}
	if err := snapshot.EmptyPostgres(ctx, db); err != nil {
		db.Close()
		t.Fatal("source fixture database must be empty", err)
	}
	m, err := entschema.NewMigrate(entsql.OpenDB("postgres", db))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := m.Create(ctx, source.Tables...); err != nil {
		db.Close()
		t.Fatal(err)
	}
	seedPostgresReleasedBusiness(t, db, keys, dataDir)
	if err := setPostgresReleaseSequences(ctx, db); err != nil {
		db.Close()
		t.Fatal("align released sequence high-water marks", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	configData, err := config.EncodeServerDocument(*cfg, config.LogConfig{Level: "info", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "source.yaml")
	if _, err := credential.PublishPrivateFile(configPath, configData); err != nil {
		t.Fatal(err)
	}
	mappingPath := filepath.Join(root, "mapping.json")
	if _, err := credential.PublishPrivateFile(mappingPath, []byte(`{"2":{"username":"BOB","email":null},"3":{"username":"ALICE","email":null}}`)); err != nil {
		t.Fatal(err)
	}
	return PlanOptions{
		Inputs:      config.ServerInputs{Mode: "serve", ConfigPath: &configPath, Environment: map[string]string{}},
		Directory:   filepath.Join(root, operationName),
		MappingPath: mappingPath,
	}, cfg, keys
}

func seedPostgresReleasedBusiness(t *testing.T, db *sql.DB, keys *credential.Keyring, dataDir string) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 1, 2, 3, 456789000, time.UTC)
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("original-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct {
		id          int
		name, email string
		role        string
	}{{1, "OriginalAdmin", "Admin@example.invalid", "admin"}, {2, "Alice", "alice@example.invalid", "user"}, {3, "Bob", "bob@example.invalid", "user"}} {
		if _, err := db.ExecContext(ctx, "INSERT INTO users(id,created_at,updated_at,username,password_hash,email,role,active) VALUES($1,$2,$2,$3,$4,$5,$6,true)", user.id, now, user.name, string(passwordHash), user.email, user.role); err != nil {
			t.Fatal("insert released user", err)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO instance_initializations(id,created_at,updated_at,version,data_version,mode) VALUES(1,$1,$1,1,2,'serve')", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO system_settings(created_at,updated_at,key,value) VALUES($1,$1,'registration_enabled','true')", now); err != nil {
		t.Fatal(err)
	}
	for id, key := range map[int]string{21: "legacy.txt", 22: "missing.txt", 23: "uncertain.txt", 24: "imported.txt"} {
		projectID := 10 + id%2
		if _, err := db.ExecContext(ctx, "INSERT INTO organizations(id,created_at,updated_at,name,slug) VALUES(7,$1,$1,'original-org','original-org') ON CONFLICT(id) DO NOTHING", now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO org_memberships(id,created_at,updated_at,role,organization_memberships,user_memberships) VALUES(8,$1,$1,'owner',7,1) ON CONFLICT(id) DO NOTHING", now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO projects(id,created_at,updated_at,name,config,owner_user_id) VALUES(10,$1,$1,'personal','{}',1) ON CONFLICT(id) DO NOTHING", now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO projects(id,created_at,updated_at,name,config,owner_org_id) VALUES(11,$1,$1,'organization','{}',7) ON CONFLICT(id) DO NOTHING", now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO resources(id,created_at,updated_at,path,format,storage_path,total_segments,project_id) VALUES($1,$2,$2,$3,'txt',$3,1,$4)", id, now, key, projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO segments(id,created_at,updated_at,segment_index,source_text,target_text,status,resource_id,user_reviewed_segments) VALUES($1,$2,$2,0,'hello','existing translation','approved',$3,1)", id+10, now, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO jobs(id,created_at,updated_at,status,execution_plan_id,execution_config,resource_count,project_id,user_created_jobs) VALUES(40,$1,$1,'paused',70,$2,1,11,1)", now, `{"schema_version":1,"credential_id":50,"original_payload":"preserve"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO job_resources(id,created_at,updated_at,status,segment_ids,segment_count,job_job_resources,resource_job_resources) VALUES(41,$1,$1,'pending','[31]',1,40,21)", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO credentials(id,created_at,updated_at,scope,owner_id,provider,endpoint,current_version) VALUES(50,$1,$1,'user',1,'openai','https://api.example.invalid',1)", now); err != nil {
		t.Fatal(err)
	}
	encrypted, err := keys.Encrypt("historical-llm-secret", credential.AssociatedData{ID: 50, Version: 1, Provider: "openai", Endpoint: "https://api.example.invalid", Scope: "user", OwnerID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO credential_versions(id,created_at,updated_at,version,encryption_version,key_id,nonce,ciphertext,credential_id) VALUES(51,$1,$1,1,$2,$3,$4,$5,50)", now, encrypted.Version, encrypted.KeyID, encrypted.Nonce, encrypted.Data); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO credential_job_references(id,credential_version_id,job_id) VALUES(52,51,40)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO refresh_tokens(id,created_at,updated_at,token_hash,expires_at,user_refresh_tokens) VALUES(60,$1,$1,'old-token-hash',$1,1)", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO activity_logs(id,created_at,updated_at,action,resource_type,resource_id,metadata,project_activity_logs,user_activity_logs) VALUES(61,$1,$1,'resource.segment.update','resource',21,'{}',11,1)", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO activity_logs(id,created_at,updated_at,action,resource_type,resource_id,metadata,project_activity_logs,user_activity_logs) VALUES(64,$1,$1,'resource_upload','resource',21,'{}',11,1)", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO usage_records(id,created_at,updated_at,source,project_usage_records,user_usage_records) VALUES(62,$1,$1,'job',11,1)", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO system_settings(id,created_at,updated_at,key,value) VALUES(63,$1,$1,'storage_policy',$2)", now, postgresLegacyStoragePolicy); err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(dataDir, "jobs")
	objectsDir := filepath.Join(dataDir, "objects")
	for _, dir := range []string{legacyDir, objectsDir} {
		if err := credential.CreatePrivateDirectory(dir); err != nil {
			t.Fatal(err)
		}
	}
	for name, contents := range map[string]string{"legacy.txt": "hello", "uncertain.txt": "bytes changed before inventory"} {
		if _, err := credential.PublishPrivateFile(filepath.Join(legacyDir, name), []byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := credential.PublishPrivateFile(filepath.Join(objectsDir, "imported.txt"), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	markerDir := filepath.Join(objectsDir, ".linguaflow")
	if err := credential.CreatePrivateDirectory(markerDir); err != nil {
		t.Fatal(err)
	}
	marker := []byte(`{"version":1,"identity":"original-space-identity","nonce":"original-nonce"}`)
	if _, err := credential.PublishPrivateFile(filepath.Join(markerDir, "space.json"), marker); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO storage_connections(id,created_at,updated_at,name,backend_id,driver) VALUES(71,$1,$1,'original-local','local','local')", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO storage_spaces(id,created_at,updated_at,name,identity,marker_nonce,verified,connection_id) VALUES(72,$1,$1,'original-space','original-space-identity','original-nonce',true,71)", now); err != nil {
		t.Fatal(err)
	}
	// A verified released space retains both its physical marker and the
	// committed marker write that runtime admission uses to validate identity.
	if _, err := db.ExecContext(ctx, "INSERT INTO storage_tasks(id,created_at,updated_at,operation_id,idempotency_key,request_hash,kind,status,phase) VALUES(76,$1,$1,'original-marker','original-marker','original-marker','storage_marker','completed','committed')", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO storage_writes(id,created_at,updated_at,attempt_id,object_key,max_bytes,actual_bytes,phase,task_id,space_id) VALUES(77,$1,$1,'original-marker-write','.linguaflow/space.json',$2,$2,'committed',76,72)", now, len(marker)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO blobs(id,created_at,updated_at,identity,project_id,owner_kind,owner_id,purpose,size,sha256,status,location_generation) VALUES(73,$1,$1,'original-blob',10,'user',1,'source',5,'2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824','ready',4)", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO blob_locations(id,created_at,updated_at,object_key,size,status,integrity,blob_id,space_id) VALUES(74,$1,$1,'imported.txt',5,'live','available',73,72)", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE blobs SET active_location_id=74 WHERE id=73"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO source_revisions(id,created_at,updated_at,resource_id,project_id,format,parser_version,size,sha256,current,deleted,source_blob_id) VALUES(75,$1,$1,24,10,'txt','release-parser-v1',5,'2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824',true,false,73)", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE resources SET current_source_revision_id=75,source_generation=6 WHERE id=24"); err != nil {
		t.Fatal(err)
	}
}

func setPostgresReleaseSequences(ctx context.Context, db *sql.DB) error {
	for _, table := range source.Tables {
		for _, column := range table.Columns {
			if !column.Increment {
				continue
			}
			query := "SELECT setval(pg_get_serial_sequence($1,$2),COALESCE((SELECT MAX(" + quoteIdentifier(column.Name) + ") FROM " + quoteIdentifier(table.Name) + "),0)+1000,true)"
			if _, err := db.ExecContext(ctx, query, table.Name, column.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestPostgresInplaceExternalRestoreAcceptance(t *testing.T) {
	dsn := postgresAcceptanceDSN("source", "inplace")
	toolBin := os.Getenv("LINGUAFLOW_MIGRATION_POSTGRES_BIN")
	if dsn == "" || toolBin == "" {
		t.Skip("unverified: isolated PostgreSQL source and matching 18.x test utilities are required")
	}
	ctx := context.Background()
	options, cfg, keys := postgresStrategyFixture(t, dsn, "operation-inplace")
	originals := postgresOriginalArtifacts(t, options, cfg)
	pgDump := filepath.Join(toolBin, postgresToolName("pg_dump"))
	pgRestore := filepath.Join(toolBin, postgresToolName("pg_restore"))
	manualArchive := filepath.Join(t.TempDir(), "original-source.pgdump")
	runPostgresUtility(t, dsn, pgDump, "--format=custom", "--no-owner", "--no-acl", "--file", manualArchive, "--dbname", "migration_source_inplace")
	db, _, err := snapshot.Open(ctx, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	originalIdentity, err := snapshot.PostgresIdentity(ctx, db)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	p, err := NewPlan(ctx, options)
	if err != nil {
		logPostgresReleasedSchemaMismatch(t, cfg)
		t.Fatal(err)
	}
	if p.Strategy != operation.Inplace || p.DatabaseBackup != operation.BackupExternal {
		t.Fatalf("PostgreSQL defaults = %s/%s, want inplace/external", p.Strategy, p.DatabaseBackup)
	}
	emptyToolBin := t.TempDir()
	t.Setenv("LINGUAFLOW_MIGRATION_POSTGRES_BIN", emptyToolBin)
	if _, err := snapshot.InspectPostgresDump(ctx); err == nil {
		t.Fatal("pg_dump unexpectedly available in external protection tool directory")
	}
	if _, err := snapshot.InspectPostgresRestore(ctx); err == nil {
		t.Fatal("pg_restore unexpectedly available in external protection tool directory")
	}
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err == nil {
		t.Fatal("external inplace apply accepted a missing backup reference")
	}
	db, _, err = snapshot.Open(ctx, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := Fingerprint(ctx, db, "postgres")
	db.Close()
	if err != nil || unchanged != p.SourceFingerprint {
		t.Fatal("missing-reference apply changed source fingerprint", err)
	}
	reference := "acceptance:postgres18.6:inplace:source-backup"
	injected := errors.New("stop after external declaration is durably bound")
	_, err = Run(ctx, RunOptions{Directory: options.Directory, Offline: true, BackupReference: reference, Fault: func(at string) error {
		if at == "backup" {
			return injected
		}
		return nil
	}})
	if !errors.Is(err, injected) {
		t.Fatal("external declaration interruption did not fire", err)
	}
	boundPlan, boundJournal, err := operation.Load(options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.LoadBaseline(options.Directory, boundPlan, boundJournal); err != nil {
		t.Fatal(err)
	}
	boundBaseline := operation.BaselineDigest(boundPlan)
	backup, err := LoadBackupReceipt(ctx, boundJournal.BackupDirectory, boundPlan)
	if err != nil {
		t.Fatal(err)
	}
	if backup.State != snapshot.OperatorDeclared || backup.Database.Name != "" || backup.Reference != reference || boundPlan.Baseline.Facts["backup_reference"] != reference || !backup.CreatedAt.Equal(boundPlan.Baseline.ConversionTime) {
		t.Fatal("external declaration did not bind the reference and confirmation time to the original baseline")
	}
	changedReference := *backup
	changedReference.Reference += ":changed"
	if err := VerifyBackupReceipt(ctx, boundJournal.BackupDirectory, boundPlan, &changedReference); err == nil {
		t.Fatal("external receipt accepted a changed backup reference")
	}
	changedTime := *backup
	changedTime.CreatedAt = changedTime.CreatedAt.Add(time.Second)
	if err := VerifyBackupReceipt(ctx, boundJournal.BackupDirectory, boundPlan, &changedTime); err == nil {
		t.Fatal("external receipt accepted a changed confirmation time")
	}
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true, BackupReference: reference + ":changed"}); err == nil {
		t.Fatal("resume replaced the original external backup declaration")
	}
	report, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true})
	if err != nil {
		t.Fatal("external inplace apply with bound reference", err)
	}
	if report.Phase != "completed" || report.Protection != snapshot.OperatorDeclared || report.BackupReference != reference || report.DatabaseBackup != operation.BackupExternal {
		t.Fatalf("external protection report is incomplete: %+v", report)
	}
	resumedPlan, resumedJournal, err := operation.Load(options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.LoadBaseline(options.Directory, resumedPlan, resumedJournal); err != nil {
		t.Fatal(err)
	}
	if operation.BaselineDigest(resumedPlan) != boundBaseline {
		t.Fatal("resume changed the original baseline or confirmation time")
	}
	assertPostgresOriginalArtifacts(t, originals)
	t.Log("external apply/resume completed with neither pg_dump nor pg_restore available; original declaration reference/time reused; changed receipt fields rejected")
	if err := assertPostgresConvertedBusiness(ctx, cfg, keys, resumedPlan); err != nil {
		t.Fatal(err)
	}
	rollback, err := RollbackOperation(ctx, options.Directory, true)
	if err != nil {
		t.Fatal("request external source restoration", err)
	}
	if rollback.Phase != "restore_required" || rollback.Restoration != "restore_required" || rollback.Verified {
		t.Fatalf("external rollback falsely reported restoration: %+v", rollback)
	}
	db, _, err = snapshot.Open(ctx, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "DROP SCHEMA public CASCADE"); err == nil {
		if _, err := db.ExecContext(ctx, "CREATE SCHEMA public AUTHORIZATION CURRENT_USER"); err != nil {
			db.Close()
			t.Fatal("recreate source schema under ordinary owner", err)
		}
	} else {
		db.Close()
		t.Fatal("drop converted schema for operator-controlled restore", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	runPostgresUtility(t, dsn, pgRestore, "--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction", "--dbname", "migration_source_inplace", manualArchive)
	db, _, err = snapshot.Open(ctx, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	restoredIdentity, idErr := snapshot.PostgresIdentity(ctx, db)
	restoredFingerprint, fpErr := Fingerprint(ctx, db, "postgres")
	db.Close()
	if errors.Join(idErr, fpErr) != nil || restoredIdentity != originalIdentity || restoredFingerprint != p.SourceFingerprint {
		t.Fatal("manual restore did not preserve the original database OID and baseline", errors.Join(idErr, fpErr))
	}
	rollback, err = RollbackOperation(ctx, options.Directory, true)
	if err != nil {
		t.Fatal("verify operator-restored source", err)
	}
	if rollback.Phase != "rolled_back" || rollback.Restoration != "verified" || !rollback.Verified {
		t.Fatalf("restored baseline was not registered: %+v", rollback)
	}
	assertPostgresOriginalArtifacts(t, originals)
	t.Log("operator manually restored the original custom archive; same database OID, full source fingerprint, configuration, keyring and original files verified")
	assertPostgresRestoredRehearsal(t, options, cfg, pgRestore, manualArchive, originals)

	// A fresh operation proves runtime password login after conversion without
	// making the first operation's deliberate restore boundary include new rows.
	second := options
	second.Directory = filepath.Join(filepath.Dir(options.Directory), "operation-after-restore")
	p2, err := NewPlan(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, RunOptions{Directory: second.Directory, Offline: true, BackupReference: "acceptance:postgres18.6:inplace:second-source-backup"}); err != nil {
		t.Fatal("second external inplace apply", err)
	}
	resolved, err := config.ResolveServerConfig(options.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	db, client, err := database.Open(ctx, &resolved.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	auth := service.NewAuthService(client, service.AuthConfigFromServer(resolved.Config), service.NewSettingsService(client))
	session, err := auth.Login(ctx, service.LoginInput{Identifier: p2.Accounts[0].Username, Password: "original-password-123"})
	if err != nil || session.User.ID != 1 {
		t.Fatal("original administrator password login after PostgreSQL conversion", err)
	}
	// API startup reconciles runtime storage rows; exercise token rejection only
	// after the first operation's manual same-OID restore has been verified.
	if err := assertPostgresOldAccessTokenRejected(ctx, &resolved.Config, keys); err != nil {
		t.Fatal(err)
	}
	assertPostgresOriginalArtifacts(t, originals)
}

func assertPostgresRestoredRehearsal(t *testing.T, sourceOptions PlanOptions, sourceCfg *config.ServerConfig, pgRestore, manualArchive string, originals map[string]operation.Artifact) {
	t.Helper()
	ctx := context.Background()
	if len(sourceCfg.Storage.Backends) != 0 {
		t.Fatal("restored rehearsal must exercise the original implicit local root")
	}
	dsn := postgresAcceptanceDSN("rehearsal", "inplace")
	if dsn == "" {
		t.Fatal("restored rehearsal requires the dedicated REHEARSAL_INPLACE database")
	}
	root := t.TempDir()
	cfg := *sourceCfg
	cfg.DataDir = filepath.Join(root, "restored-rehearsal-data")
	cfg.Database.DSN = dsn
	cfg.Storage.Backends = []config.StorageBackendConfig{{ID: "local", Driver: "local", Root: filepath.Join(sourceCfg.DataDir, "objects")}}
	db, _, err := snapshot.Open(ctx, &cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	err = snapshot.EmptyPostgres(ctx, db)
	db.Close()
	if err != nil {
		t.Fatal("manual rehearsal restore requires its unused empty database", err)
	}
	runPostgresUtility(t, dsn, pgRestore, "--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction", "--dbname", "migration_rehearsal_inplace", manualArchive)
	if _, err := snapshot.InspectPostgresDump(ctx); err == nil {
		t.Fatal("restored rehearsal unexpectedly has pg_dump available")
	}
	if _, err := snapshot.InspectPostgresRestore(ctx); err == nil {
		t.Fatal("restored rehearsal unexpectedly has pg_restore available")
	}
	data, err := config.EncodeServerDocument(cfg, config.LogConfig{Level: "info", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "restored-rehearsal.yaml")
	if _, err := credential.PublishPrivateFile(configPath, data); err != nil {
		t.Fatal(err)
	}
	options := sourceOptions
	options.Directory = filepath.Join(root, "restored-rehearsal-parent")
	options.RehearsalConfig = configPath
	parent, err := NewPlan(ctx, options)
	if err != nil {
		t.Fatal("plan the actual manually restored PostgreSQL copy", err)
	}
	if !parent.Target.RehearsalRestored || parent.Target.RehearsalIdentity == parent.Source.Identity || parent.Target.RehearsalDataDir != cfg.DataDir {
		t.Fatal("plan did not identify an independent manually restored rehearsal")
	}
	parentJournalPath := filepath.Join(options.Directory, "journal.json")
	parentJournal, err := operation.Hash(ctx, parentJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	assertParentUnchanged := func() {
		t.Helper()
		db, _, err := snapshot.Open(ctx, sourceCfg, true)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := Fingerprint(ctx, db, "postgres")
		db.Close()
		if err != nil || fingerprint != parent.SourceFingerprint {
			t.Fatal("restored rehearsal changed the original source fingerprint", err)
		}
		proof, err := operation.Hash(ctx, parentJournalPath)
		if err != nil || proof != parentJournal {
			t.Fatal("restored rehearsal changed the parent journal", err)
		}
		_, journal, err := operation.Load(options.Directory)
		if err != nil || journal.Phase != "planned" || journal.BaselineDigest != "" {
			t.Fatal("rehearsal parent did not remain planned without a baseline", err)
		}
		assertPostgresOriginalArtifacts(t, originals)
	}
	copyFingerprint := func() string {
		t.Helper()
		db, _, err := snapshot.Open(ctx, &cfg, true)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := Fingerprint(ctx, db, "postgres")
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		return fingerprint
	}
	changeCopyUsername := func(username string) {
		t.Helper()
		db, _, err := snapshot.Open(ctx, &cfg, false)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.ExecContext(ctx, "UPDATE users SET username=$1 WHERE id=1", username)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	changeCopyUsername("RehearsalMismatch")
	mismatched := copyFingerprint()
	if mismatched == parent.SourceFingerprint {
		t.Fatal("rehearsal mismatch fixture did not change its fingerprint")
	}
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true, Rehearse: true}); errorCode(err) != operation.BaselineChanged {
		t.Fatal("changed manually restored copy was not rejected before writes", err)
	}
	if copyFingerprint() != mismatched {
		t.Fatal("rejected rehearsal wrote to the mismatching copy")
	}
	childDirectory := filepath.Join(options.Directory, "rehearsal")
	for _, path := range []string{childDirectory, cfg.DataDir, filepath.Join(options.Directory, "rehearsal-source.yaml")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("rejected rehearsal published recovery material", err)
		}
	}
	assertParentUnchanged()
	changeCopyUsername("OriginalAdmin")
	if copyFingerprint() != parent.SourceFingerprint {
		t.Fatal("restoring the one changed copy field did not recover the baseline")
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	baselineCalls := 0
	_, err = Run(cancelCtx, RunOptions{Directory: options.Directory, Offline: true, Rehearse: true, Fault: func(at string) error {
		if at == "baseline" {
			baselineCalls++
			cancel()
		}
		return nil
	}})
	if !errors.Is(err, context.Canceled) || baselineCalls != 1 {
		t.Fatal("restored rehearsal did not propagate natural baseline cancellation", err)
	}
	assertParentUnchanged()
	if copyFingerprint() != parent.SourceFingerprint {
		t.Fatal("baseline cancellation wrote to the restored copy")
	}
	child, journal, err := operation.Load(childDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.LoadBaseline(childDirectory, child, journal); err != nil {
		t.Fatal(err)
	}
	if child.Strategy != operation.Inplace || child.DatabaseBackup != operation.BackupExternal || child.Source.Identity != parent.Target.RehearsalIdentity || child.Source.DataDir != cfg.DataDir || journal.Phase != "needs_action" || len(journal.Steps) != 0 {
		t.Fatal("cancelled rehearsal did not retain its independent inplace/external child")
	}
	childInputs, err := config.ResolveServerConfig(PlannedInputs(child))
	if err != nil {
		t.Fatal(err)
	}
	childRoot, _, err := DefaultLocalRoot(childInputs.Config)
	if err != nil || len(childInputs.Config.Storage.Backends) == 0 || childRoot != filepath.Join(sourceCfg.DataDir, "objects") {
		t.Fatal("restored child did not materialize the parent's bound implicit object root", err)
	}
	boundBaseline := operation.BaselineDigest(child)
	boundTime := child.Baseline.ConversionTime
	boundReference := "manual-rehearsal:" + parent.SourceFingerprint
	if child.Baseline.Facts["backup_reference"] != boundReference || boundBaseline == "" {
		t.Fatal("natural cancellation lost the child's bound external declaration")
	}
	report, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true, Rehearse: true})
	if err != nil {
		t.Fatal("resume manually restored rehearsal without a new declaration", err)
	}
	if !report.Verified || report.Phase != "completed" || report.Rehearsal != "verified" || report.Strategy != operation.Inplace || report.DatabaseBackup != operation.BackupExternal || report.Protection != snapshot.OperatorDeclared || report.BackupReference != boundReference {
		t.Fatal("restored rehearsal did not complete its verified inplace/external report")
	}
	child, journal, err = operation.Load(childDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.LoadBaseline(childDirectory, child, journal); err != nil {
		t.Fatal(err)
	}
	if operation.BaselineDigest(child) != boundBaseline || !child.Baseline.ConversionTime.Equal(boundTime) || child.Baseline.Facts["backup_reference"] != boundReference {
		t.Fatal("restored rehearsal resume replaced the original declaration or baseline time")
	}
	assertParentUnchanged()
	t.Log("actual restored-copy rehearsal: mismatch rejected before writes; natural baseline cancellation retained parent/source; resumed inplace/external child verified with reused baseline/reference/time and neither pg_dump nor pg_restore")
}

func assertPostgresConvertedBusiness(ctx context.Context, cfg *config.ServerConfig, keys *credential.Keyring, p *operation.Plan) error {
	db, client, err := snapshot.Open(ctx, cfg, true)
	if err != nil {
		return err
	}
	defer db.Close()
	var username2, username3 string
	var email2, email3 sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT username,email FROM users WHERE id=2").Scan(&username2, &email2); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "SELECT username,email FROM users WHERE id=3").Scan(&username3, &email3); err != nil {
		return err
	}
	if username2 != "bob" || username3 != "alice" || email2.Valid || email3.Valid {
		return fmt.Errorf("mapped account exchange or explicit email null changed")
	}
	var oldSessions, translations int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM refresh_tokens").Scan(&oldSessions); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM segments WHERE target_text='existing translation'").Scan(&translations); err != nil {
		return err
	}
	if oldSessions != 0 || translations != 4 {
		return fmt.Errorf("legacy sessions/history counts = %d/%d", oldSessions, translations)
	}
	var policy string
	if err := db.QueryRowContext(ctx, "SELECT value FROM system_settings WHERE key='storage_policy'").Scan(&policy); err != nil {
		return err
	}
	if policy != postgresLegacyStoragePolicy {
		return fmt.Errorf("historical storage policy changed")
	}
	var references, visibleActivities, visibleUsage, unknownActivities int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM credential_job_references WHERE id=52 AND credential_version_id=51 AND job_id=40").Scan(&references); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM activity_logs WHERE id=61 AND visibility_scope='project' AND project_activity_logs=11 AND organization_activity_logs=7").Scan(&visibleActivities); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_records WHERE id=62 AND visibility_scope='project' AND project_usage_records=11 AND organization_usage_records=7").Scan(&visibleUsage); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM activity_logs WHERE id=64 AND action='resource_upload' AND visibility_scope='unknown' AND project_activity_logs=11").Scan(&unknownActivities); err != nil {
		return err
	}
	if references != 1 || visibleActivities != 1 || visibleUsage != 1 || unknownActivities != 1 {
		return fmt.Errorf("historical credential/activity/usage/unknown counts = %d/%d/%d/%d, want 1/1/1/1", references, visibleActivities, visibleUsage, unknownActivities)
	}
	var jobStatus string
	var jobPayload []byte
	if err := db.QueryRowContext(ctx, "SELECT status,execution_config FROM jobs WHERE id=40").Scan(&jobStatus, &jobPayload); err != nil {
		return err
	}
	if jobStatus != "failed" || !strings.Contains(string(jobPayload), "original_payload") {
		return fmt.Errorf("historical job payload was not retained")
	}
	var aad credential.AssociatedData
	var encrypted credential.Ciphertext
	if err := db.QueryRowContext(ctx, "SELECT v.credential_id,v.version,v.encryption_version,v.key_id,v.nonce,v.ciphertext,c.provider,c.endpoint,c.scope,c.owner_id FROM credential_versions v JOIN credentials c ON c.id=v.credential_id WHERE v.id=51").Scan(&aad.ID, &aad.Version, &encrypted.Version, &encrypted.KeyID, &encrypted.Nonce, &encrypted.Data, &aad.Provider, &aad.Endpoint, &aad.Scope, &aad.OwnerID); err != nil {
		return err
	}
	secret, err := keys.Decrypt(encrypted, aad)
	if err != nil || secret != "historical-llm-secret" {
		return fmt.Errorf("encrypted historical credential did not decrypt with the protected keyring")
	}
	if err := VerifySourceKeys(ctx, db, keys); err != nil {
		return err
	}
	var revisionGeneration, locationGeneration int
	if err := db.QueryRowContext(ctx, "SELECT source_generation FROM resources WHERE id=24").Scan(&revisionGeneration); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "SELECT location_generation FROM blobs WHERE id=73").Scan(&locationGeneration); err != nil {
		return err
	}
	if revisionGeneration != 6 || locationGeneration != 4 {
		return fmt.Errorf("storage generation facts changed: resource=%d blob=%d", revisionGeneration, locationGeneration)
	}
	for _, path := range []string{filepath.Join(cfg.DataDir, "jobs", "legacy.txt"), filepath.Join(cfg.DataDir, "objects", "imported.txt")} {
		if _, err := operation.Hash(ctx, path); err != nil {
			return fmt.Errorf("original file reference was lost: %w", err)
		}
	}
	var sequenceValue int64
	var sequenceCalled bool
	if err := db.QueryRowContext(ctx, "SELECT last_value,is_called FROM users_id_seq").Scan(&sequenceValue, &sequenceCalled); err != nil {
		return err
	}
	if sequenceValue < 1003 || !sequenceCalled {
		return fmt.Errorf("user sequence high-water mark changed: %d/%t", sequenceValue, sequenceCalled)
	}
	if _, err := VerifyBaseline(ctx, db, p, true); err != nil {
		return err
	}
	if err := VerifyAllocationBaselineClient(ctx, client, p); err != nil {
		return err
	}
	if _, err := client.User.Get(ctx, p.Accounts[0].UserID); err != nil {
		return err
	}
	return nil
}

func assertPostgresOldAccessTokenRejected(ctx context.Context, cfg *config.ServerConfig, keys *credential.Keyring) error {
	db, client, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	server, err := api.NewServer(cfg, keys, slog.New(slog.NewTextHandler(io.Discard, nil)), db, client, config.ModeServer, nil)
	if err != nil {
		return err
	}
	defer server.Shutdown(ctx)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"uid": 1, "username": "OriginalAdmin", "role": "admin", "iss": cfg.JWTIssuer,
		"sub": "user:1", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(cfg.JWTSecret))
	if err != nil {
		return err
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.GetCurrentUser(response, request)
	if response.Code != http.StatusUnauthorized {
		return fmt.Errorf("pre-migration access token returned HTTP %d", response.Code)
	}
	account, err := client.User.Get(ctx, 1)
	if err != nil {
		return err
	}
	currentToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, service.AccessTokenClaims{
		UserID: account.ID, Username: account.Username, Role: account.Role, AuthVersion: account.AuthVersion,
		RegisteredClaims: jwt.RegisteredClaims{Issuer: cfg.JWTIssuer, Subject: "user:1", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}).SignedString([]byte(cfg.JWTSecret))
	if err != nil {
		return err
	}
	currentRequest := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	currentRequest.Header.Set("Authorization", "Bearer "+currentToken)
	currentResponse := httptest.NewRecorder()
	server.GetCurrentUser(currentResponse, currentRequest)
	if currentResponse.Code != http.StatusOK {
		return fmt.Errorf("current access token returned HTTP %d", currentResponse.Code)
	}
	return nil
}

func TestPostgresAutoInplaceDumpOnlyAcceptance(t *testing.T) {
	dsn := postgresAcceptanceDSN("source", "auto")
	fullToolBin := os.Getenv("LINGUAFLOW_MIGRATION_POSTGRES_BIN")
	if dsn == "" || fullToolBin == "" {
		t.Skip("unverified: isolated PostgreSQL source and matching 18.x dump utility are required")
	}
	ctx := context.Background()
	options, cfg, keys := postgresStrategyFixture(t, dsn, "operation-auto")
	originals := postgresOriginalArtifacts(t, options, cfg)
	options.DatabaseBackup = string(operation.BackupAuto)
	dumpOnlyBin := t.TempDir()
	dumpPath := filepath.Join(fullToolBin, postgresToolName("pg_dump"))
	if err := copyPostgresUtility(dumpPath, filepath.Join(dumpOnlyBin, postgresToolName("pg_dump"))); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fullToolBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LINGUAFLOW_MIGRATION_POSTGRES_BIN", dumpOnlyBin)
	version, err := snapshot.InspectPostgresDump(ctx)
	if err != nil || version != "18.6" {
		t.Fatalf("auto dump tool version = %q, %v", version, err)
	}
	if _, err := snapshot.InspectPostgresRestore(ctx); err == nil {
		t.Fatal("pg_restore unexpectedly visible in dump-only migration tool directory")
	}
	p, err := NewPlan(ctx, options)
	if err != nil {
		logPostgresReleasedSchemaMismatch(t, cfg)
		t.Fatal(err)
	}
	if p.Strategy != operation.Inplace || p.DatabaseBackup != operation.BackupAuto {
		t.Fatalf("auto plan strategy/protection = %s/%s", p.Strategy, p.DatabaseBackup)
	}
	report, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true})
	if err != nil {
		t.Fatal("automatic PostgreSQL dump and inplace conversion", err)
	}
	if report.Phase != "completed" || report.Protection != snapshot.Captured || report.DatabaseBackup != operation.BackupAuto {
		t.Fatalf("automatic protection report is incomplete: %+v", report)
	}
	plan, journal, err := operation.Load(options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.LoadBaseline(options.Directory, plan, journal); err != nil {
		t.Fatal(err)
	}
	backup, err := LoadBackupReceipt(ctx, journal.BackupDirectory, plan)
	if err != nil {
		t.Fatal(err)
	}
	if backup.State != snapshot.Captured || backup.Database.Name != "database.pgdump" || backup.Database.Size == 0 {
		t.Fatalf("automatic archive receipt is incomplete: %+v", backup.ProtectionReceipt)
	}
	if _, err := VerifyOperation(ctx, options.Directory); err != nil {
		t.Fatal(err)
	}
	if err := assertPostgresConvertedBusiness(ctx, cfg, keys, plan); err != nil {
		t.Fatal(err)
	}
	if err := assertPostgresOldAccessTokenRejected(ctx, cfg, keys); err != nil {
		t.Fatal(err)
	}
	assertPostgresOriginalArtifacts(t, originals)
	t.Log("automatic custom pg_dump archive captured without pg_restore; protected credential, history, policy, original files and all released sequence high-water marks verified")
}

func postgresOriginalArtifacts(t *testing.T, options PlanOptions, cfg *config.ServerConfig) map[string]operation.Artifact {
	t.Helper()
	originals := map[string]operation.Artifact{}
	for _, path := range []string{*options.Inputs.ConfigPath, cfg.Credentials.KeyringFile, filepath.Join(cfg.DataDir, "jobs", "legacy.txt"), filepath.Join(cfg.DataDir, "jobs", "uncertain.txt"), filepath.Join(cfg.DataDir, "objects", "imported.txt"), filepath.Join(cfg.DataDir, "objects", ".linguaflow", "space.json")} {
		proof, err := operation.Hash(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		originals[path] = proof
	}
	return originals
}

func logPostgresReleasedSchemaMismatch(t *testing.T, cfg *config.ServerConfig) {
	t.Helper()
	ctx := t.Context()
	db, _, err := snapshot.Open(ctx, cfg, true)
	if err != nil {
		t.Log("schema diagnostics: cannot open private source", err)
		return
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Log("schema diagnostics: cannot begin inspection", err)
		return
	}
	defer tx.Rollback()
	tables, err := inspectTables(ctx, tx, "postgres")
	if err != nil {
		t.Log("schema diagnostics: cannot inspect tables", err)
		return
	}
	for _, actual := range tables {
		for _, expected := range source.Tables {
			if actual.Name != expected.Name {
				continue
			}
			if err := matchTables([]actualTable{actual}, []*entschema.Table{expected}, "postgres"); err != nil {
				t.Logf("released schema mismatch: table=%s reason=%v actual_indexes=%+v", actual.Name, err, actual.Indexes)
				for _, wantColumn := range expected.Columns {
					if wantColumn.Default == nil {
						continue
					}
					for _, gotColumn := range actual.Columns {
						if gotColumn.Name == wantColumn.Name && !defaultMatches(gotColumn.Default, wantColumn) {
							actualDefault := "<none>"
							if gotColumn.Default != nil {
								actualDefault = *gotColumn.Default
							}
							t.Logf("released default mismatch: column=%s.%s actual=%q declared=%v", actual.Name, wantColumn.Name, actualDefault, wantColumn.Default)
						}
					}
				}
				for _, index := range expected.Indexes {
					if index.Annotation != nil {
						t.Logf("declared index predicate: %s %q", index.Name, index.Annotation.Where)
					}
				}
			}
		}
	}
}

func assertPostgresOriginalArtifacts(t *testing.T, originals map[string]operation.Artifact) {
	t.Helper()
	for path, before := range originals {
		after, err := operation.Hash(context.Background(), path)
		if err != nil || after.Size != before.Size || after.SHA256 != before.SHA256 {
			t.Fatalf("original configuration, keyring or source file changed: %s", filepath.Base(path))
		}
	}
}

func copyPostgresUtility(sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, source)
	return errors.Join(copyErr, target.Sync(), target.Close())
}
