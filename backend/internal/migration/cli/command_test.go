package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	entschema "entgo.io/ent/dialect/sql/schema"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	storagemigrate "github.com/MeowSalty/LinguaFlow/backend/internal/migration/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015"
	source "github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015/source"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

func clearDeploymentEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "LINGUAFLOW_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func execute(t *testing.T, args ...string) string {
	t.Helper()
	var output bytes.Buffer
	cmd := NewCommand()
	cmd.SetArgs(args)
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("command %v: %v", args, err)
	}
	return output.String()
}

func TestMigrationHelpAndListNeverPrepareInputs(t *testing.T) {
	clearDeploymentEnvironment(t)
	root := filepath.Join(t.TempDir(), "absent")
	t.Setenv("LINGUAFLOW_DATA_DIR", root)
	t.Setenv("LINGUAFLOW_DATABASE_DSN_FILE", filepath.Join(root, "missing-secret"))
	for _, args := range [][]string{{"--help"}, {"upgrade", "plan", "--help"}, {"storage", "apply", "--help"}, {"resume", "--help"}, {"list"}} {
		if output := execute(t, args...); len(output) == 0 {
			t.Fatal("empty help/catalog")
		}
	}
	if output := execute(t, "--help"); strings.Contains(output, "backup") {
		t.Fatalf("general backup command remains exposed: %s", output)
	}
	if output := execute(t, "list"); strings.Contains(output, "backup-current") {
		t.Fatalf("general backup route remains cataloged: %s", output)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("help or list prepared input directories")
	}
}

func TestUpgradePlanFlagsAndDefaultInplacePlan(t *testing.T) {
	configPath, sourceDir := cliReleaseFixture(t)
	operationDir := filepath.Join(t.TempDir(), "inplace-operation")
	before, err := operation.Hash(t.Context(), filepath.Join(sourceDir, "linguaflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	output := execute(t, "upgrade", "plan", "--config", configPath, "--operation-dir", operationDir)
	var plan operation.Plan
	if err := json.Unmarshal([]byte(output), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Strategy != operation.Inplace || plan.DatabaseBackup != operation.BackupAuto || plan.Target.DataDir != "" || plan.Target.ConfigPath != "" {
		t.Fatalf("default CLI plan is not inplace: %+v", plan)
	}
	after, err := operation.Hash(t.Context(), filepath.Join(sourceDir, "linguaflow.db"))
	if err != nil || after != before {
		t.Fatal("planning changed source bytes", err)
	}

	for _, args := range [][]string{{"upgrade", "plan", "--help"}, {"upgrade", "apply", "--help"}, {"upgrade", "rehearse", "--help"}} {
		var output bytes.Buffer
		cmd := NewCommand()
		cmd.SetArgs(args)
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatalf("help %v: %v", args, err)
		}
		wants := []string{"--strategy", "--database-backup"}
		if args[1] != "plan" {
			wants = []string{"--backup-reference"}
		}
		for _, want := range wants {
			if !strings.Contains(output.String(), want) {
				t.Fatalf("%v help omits %s: %s", args, want, output.String())
			}
		}
	}
}

func TestLegacyStatusIsReadOnlyAndOtherActionsNameRecordedBuild(t *testing.T) {
	directory := t.TempDir()
	dataDir := filepath.Join(directory, "source")
	targetDir := filepath.Join(directory, "target")
	id, err := operation.NewID()
	if err != nil {
		t.Fatal(err)
	}
	plan := &operation.Plan{
		Version: operation.Version, Strategy: operation.Clone, DatabaseBackup: operation.BackupAuto,
		ID: id, Kind: "upgrade", Route: "upgrade-v015-auth1", Build: "v0.16.0+recorded-build",
		CreatedAt:         time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
		Source:            operation.Endpoint{DataDir: dataDir, Driver: "sqlite", Mode: "serve", Identity: "source-id", InputDigest: operation.Digest("input")},
		SourceFingerprint: operation.Digest("fingerprint"), SourceSchema: operation.Digest("schema"),
		ObjectsDigest: operation.Digest("objects"), TargetSignature: operation.Digest("target"),
		Target:   operation.Target{DataDir: targetDir, ParentIdentity: "parent-id"},
		Accounts: []operation.Account{{UserID: 1, Username: "admin"}},
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["version"] = json.RawMessage("1")
	delete(document, "strategy")
	delete(document, "database_backup")
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(directory, "plan.json")
	if err := os.WriteFile(planPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	statusOutput := execute(t, "status", "--operation-dir", directory)
	if !strings.Contains(statusOutput, `"expected_build": "v0.16.0+recorded-build"`) || !strings.Contains(statusOutput, `"plan"`) || strings.Contains(statusOutput, `"journal": {`) {
		t.Fatalf("legacy status omitted its plan/build or implied journal evidence: %s", statusOutput)
	}
	cmd := NewCommand()
	cmd.SetArgs([]string{"resume", "--operation-dir", directory, "--offline"})
	err = cmd.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "v0.16.0+recorded-build") {
		t.Fatalf("legacy resume guidance = %v", err)
	}
	after, err := os.ReadFile(planPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("legacy status/recovery routing changed the plan", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "journal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy action created a journal", err)
	}
}

func TestInspectRequiresExplicitSourceBeforeOpeningDefaultDatabase(t *testing.T) {
	clearDeploymentEnvironment(t)
	cmd := NewCommand()
	cmd.SetArgs([]string{"inspect"})
	if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "explicit source") {
		t.Fatal("inspect accepted an implicit source", err)
	}
}

func TestServiceDependenciesExcludeMigration(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/linguaflow")
	cmd.Dir = filepath.Join("..", "..", "..")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dependency check: %v %s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "/internal/migration") {
			t.Fatalf("runtime depends on offline package: %s", line)
		}
	}
}

func cliReleaseFixture(t *testing.T) (string, string) {
	return cliReleaseFixtureMode(t, "serve")
}

func cliReleaseFixtureMode(t *testing.T, mode string) (string, string) {
	t.Helper()
	clearDeploymentEnvironment(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "linguaflow.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := entschema.NewMigrate(entsql.OpenDB("sqlite3", db))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Create(context.Background(), source.Tables...); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(timeutil.SQLiteLayout)
	hash, err := bcrypt.GenerateFromPassword([]byte("original-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"PRAGMA user_version=1"} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	username := "OriginalAdmin"
	var localID any
	if mode == "local" {
		username, localID = "local", 1
	}
	if _, err := db.Exec("INSERT INTO users(id,created_at,updated_at,username,password_hash,email,role,active) VALUES(1,?,?,?,?, 'admin@example.invalid','admin',true)", now, now, username, string(hash)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO instance_initializations(id,created_at,updated_at,version,data_version,mode,local_user_id) VALUES(1,?,?,1,2,?,?)", now, now, mode, localID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO system_settings(created_at,updated_at,key,value) VALUES(?,?,'registration_enabled','false')", now, now); err != nil {
		t.Fatal(err)
	}
	keys, _, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	data, err := credential.EncodeKeyring(keys)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "keyring.json")
	if _, err := credential.PublishPrivateFile(keyPath, data); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultServerConfig()
	if mode == "local" {
		cfg.Mode = config.ModeLocal
		cfg.Host = "127.0.0.1"
	}
	cfg.DataDir = dir
	cfg.Database.DSN = path
	cfg.SSE.MaxReplayEvents = 1000
	cfg.JWTSecret = "cli-test-deployment-secret-at-least-32-bytes"
	cfg.Credentials.KeyringFile = keyPath
	cfg.Storage.Initialization.CapacityBytes = config.QuotaInput{Set: true}
	cfg.Storage.Initialization.LogicalLimitBytes = config.QuotaInput{Set: true}
	data, err = config.EncodeServerDocument(*cfg, config.LogConfig{Level: "info", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "source.yml")
	if _, err := credential.PublishPrivateFile(configPath, data); err != nil {
		t.Fatal(err)
	}
	return configPath, dir
}

func currentFixture(t *testing.T) (string, *config.ResolvedServer) {
	return currentFixtureMode(t, "serve")
}

func currentFixtureMode(t *testing.T, mode string) (string, *config.ResolvedServer) {
	t.Helper()
	ctx := context.Background()
	configPath, _ := cliReleaseFixtureMode(t, mode)
	parent := t.TempDir()
	operationDir := filepath.Join(parent, "upgrade")
	target := filepath.Join(parent, "current")
	execute(t, "upgrade", "plan", "--mode", mode, "--config", configPath, "--operation-dir", operationDir, "--target-data-dir", target)
	execute(t, "upgrade", "apply", "--operation-dir", operationDir, "--offline")
	configPath = filepath.Join(target, "deployment.yaml")
	resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: mode, ConfigPath: &configPath, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	db, client, err := snapshot.Open(ctx, &resolved.Config, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := client.Project.Create().SetName("legacy project").SetSourceLang("ja").SetTargetLang("en").SetOwnerUserID(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := client.Resource.Create().SetPath("source.txt").SetFormat("txt").SetStoragePath("source.txt").SetProjectID(p.ID).SetTotalSegments(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Segment.Create().SetResourceID(r.ID).SetSegmentIndex(0).SetSourceText("hello").SetTargetText("preserve translated text").Save(ctx); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(resolved.Config.DataDir, "jobs")
	if err := credential.CreatePrivateDirectory(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := credential.PublishPrivateFile(filepath.Join(legacy, "source.txt"), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	return configPath, resolved
}

func TestUnifiedStorageRecoversResourceCommitAndKeepsPublishedManifest(t *testing.T) {
	for _, legacyManifest := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-plan", true: "manifest-v1"}[legacyManifest], func(t *testing.T) {
			ctx := context.Background()
			configPath, resolved := currentFixture(t)
			opdir := filepath.Join(t.TempDir(), "operation")
			args := []string{"storage", "plan", "--config", configPath, "--operation-dir", opdir}
			var manifestPath, digest string
			if legacyManifest {
				db, client, err := snapshot.Open(ctx, &resolved.Config, true)
				if err != nil {
					t.Fatal(err)
				}
				m, err := storagemigrate.New(db, client, "sqlite", storagemigrate.Options{LegacyRoot: filepath.Join(resolved.Config.DataDir, "jobs"), DefaultRoot: filepath.Join(resolved.Config.DataDir, "objects"), CapacitySet: true, LogicalSet: true})
				if err != nil {
					t.Fatal(err)
				}
				manifest, err := m.Inventory(ctx)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
				manifestPath = filepath.Join(t.TempDir(), "published-manifest.json")
				if err := storagemigrate.SaveManifest(manifestPath, manifest, true); err != nil {
					t.Fatal(err)
				}
				proof, err := operation.Hash(ctx, manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				digest = proof.SHA256
				args = append(args, "--manifest", manifestPath)
			}
			execute(t, args...)
			calls := 0
			injected := errors.New("resource journal lag")
			_, err := runStorageOperation(ctx, opdir, "resume", true, func(at string) error {
				if at == "resource_commit" {
					calls++
					if calls == 2 {
						return injected
					}
				}
				return nil
			})
			if !errors.Is(err, injected) {
				t.Fatalf("fault: %v", err)
			}
			p, j, err := operation.Load(opdir)
			if err != nil {
				t.Fatal(err)
			}
			if j.BackupDirectory == "" {
				t.Fatal("no actual backup")
			}
			if db, _, err := database.Open(ctx, &resolved.Config); err == nil {
				db.Close()
				t.Fatal("runtime opened preparing target")
			}
			execute(t, "resume", "--operation-dir", opdir, "--offline")
			execute(t, "verify", "--operation-dir", opdir)
			execute(t, "resume", "--operation-dir", opdir, "--offline")
			if p.Kind != "storage" {
				t.Fatal(p.Kind)
			}
			if legacyManifest {
				proof, err := operation.Hash(ctx, manifestPath)
				if err != nil || proof.SHA256 != digest {
					t.Fatal("published manifest rewritten")
				}
			}
			execute(t, "rollback", "--operation-dir", opdir, "--offline")
			execute(t, "rollback", "--operation-dir", opdir, "--offline")
			if _, err := v015.LoadBackupReceipt(ctx, j.BackupDirectory, p); err != nil {
				t.Fatal("rollback lost recovery materials", err)
			}
		})
	}
}

func TestInspectUnknownSourceRejectsBeforeCredentialInputs(t *testing.T) {
	clearDeploymentEnvironment(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "unknown.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE unrelated(id integer)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Setenv("LINGUAFLOW_DATABASE_DSN", dbPath)
	t.Setenv("LINGUAFLOW_DATA_DIR", dir)
	t.Setenv("LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE", filepath.Join(dir, "missing-credential"))
	before, err := operation.Hash(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := NewCommand()
	cmd.SetArgs([]string{"inspect"})
	err = cmd.ExecuteContext(context.Background())
	var known *operation.Error
	if !errors.As(err, &known) || known.Code != operation.UnsupportedSource {
		t.Fatalf("unknown source: %v", err)
	}
	after, err := operation.Hash(context.Background(), dbPath)
	if err != nil || after.SHA256 != before.SHA256 {
		t.Fatal("inspect wrote unknown source")
	}
}
