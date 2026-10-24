package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	storagemigrate "github.com/MeowSalty/LinguaFlow/backend/internal/migration/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015"
	"golang.org/x/crypto/bcrypt"
)

// The private PostgreSQL Task allocates these two otherwise unused target DBs.
// Ordinary test runs never select a maintainer database or create a database.
func TestPostgresStorageProtectionAcceptance(t *testing.T) {
	externalDSN := os.Getenv("LINGUAFLOW_MIGRATION_TEST_POSTGRES_TARGET_INPLACE")
	autoDSN := os.Getenv("LINGUAFLOW_MIGRATION_TEST_POSTGRES_TARGET_AUTO")
	toolBin := os.Getenv("LINGUAFLOW_MIGRATION_POSTGRES_BIN")
	if externalDSN == "" || autoDSN == "" || toolBin == "" {
		t.Skip("requires the isolated PostgreSQL acceptance Task")
	}
	for _, test := range []struct{ name, dsn, backup string }{{"external", externalDSN, "external"}, {"auto", autoDSN, "auto"}} {
		t.Run(test.name, func(t *testing.T) {
			clearDeploymentEnvironment(t)
			t.Setenv("LINGUAFLOW_MIGRATION_POSTGRES_BIN", toolBin)
			ctx := context.Background()
			cfgPath, resolved := currentPostgresStorageFixture(t, test.dsn)
			original := filepath.Join(resolved.Config.DataDir, "jobs", "source.txt")
			originalProof, err := operation.Hash(ctx, original)
			if err != nil {
				t.Fatal(err)
			}
			db, client, err := snapshot.Open(ctx, &resolved.Config, true)
			if err != nil {
				t.Fatal(err)
			}
			before, err := v015.Probe(ctx, db, "postgres")
			if err != nil {
				t.Fatal(err)
			}
			root, backend, err := v015.DefaultLocalRoot(resolved.Config)
			if err != nil {
				t.Fatal(err)
			}
			migrator, err := storagemigrate.New(db, client, "postgres", storagemigrate.Options{LegacyRoot: filepath.Dir(original), DefaultRoot: root, DefaultBackendID: backend, CapacitySet: true, LogicalSet: true})
			if err != nil {
				t.Fatal(err)
			}
			published, err := migrator.Inventory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			publishedPath := filepath.Join(t.TempDir(), "manifest-v1.json")
			if err := storagemigrate.SaveManifest(publishedPath, published, true); err != nil {
				t.Fatal(err)
			}
			publishedProof, err := operation.Hash(ctx, publishedPath)
			if err != nil {
				t.Fatal(err)
			}
			if test.backup == "external" {
				t.Setenv("LINGUAFLOW_MIGRATION_POSTGRES_BIN", filepath.Join(t.TempDir(), "no-tools"))
			}
			opdir := filepath.Join(t.TempDir(), "operation")
			p, err := planStorage(ctx, storagePlanOptions{Inputs: config.ServerInputs{Mode: "serve", ConfigPath: &cfgPath, Environment: map[string]string{}}, Directory: opdir, Manifest: publishedPath, DatabaseBackup: test.backup})
			if err != nil {
				t.Fatal(err)
			}
			db, _, err = snapshot.Open(ctx, &resolved.Config, true)
			if err != nil {
				t.Fatal(err)
			}
			afterPlan, err := v015.Probe(ctx, db, "postgres")
			db.Close()
			if err != nil || afterPlan.Fingerprint != before.Fingerprint {
				t.Fatal("planning changed the current source", err)
			}
			reference := ""
			if test.backup == "external" {
				if _, err := runStorageOperation(ctx, opdir, "resume", true, nil); err == nil {
					t.Fatal("accepted missing external declaration")
				}
				reference = "private-pg18.6:storage-source-before-import"
			}
			injected := errors.New("storage resource journal loss")
			calls := 0
			_, err = runStorageOperation(ctx, opdir, "resume", true, func(at string) error {
				if at == "resource_commit" {
					calls++
					if calls == 2 {
						return injected
					}
				}
				return nil
			}, reference)
			if !errors.Is(err, injected) {
				t.Fatal("resource commit boundary was not reached", err)
			}
			if err := os.Remove(filepath.Join(opdir, "journal.json")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(opdir, "storage-journal.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			report, err := runStorageOperation(ctx, opdir, "resume", true, nil)
			if err != nil {
				t.Fatal("resume from database receipts", err)
			}
			if !report.Verified || report.Version != 2 || report.Strategy != operation.Inplace || report.BaselineDigest == "" || report.ResultDigest == "" {
				t.Fatal("missing v2 completion evidence", report)
			}
			if test.backup == "external" && (report.Protection != snapshot.OperatorDeclared || report.BackupReference != reference) {
				t.Fatal("external protection was relabeled", report)
			}
			if test.backup == "auto" && report.Protection != snapshot.Captured {
				t.Fatal("automatic archive missing", report)
			}
			if _, err := runStorageOperation(ctx, opdir, "verify", false, nil); err != nil {
				t.Fatal(err)
			}
			nowProof, err := operation.Hash(ctx, original)
			if err != nil || nowProof != originalProof {
				t.Fatal("original bytes changed", err)
			}
			nowManifest, err := operation.Hash(ctx, publishedPath)
			if err != nil || nowManifest != publishedProof {
				t.Fatal("published manifest-v1 changed", err)
			}
			db, client, err = snapshot.Open(ctx, &resolved.Config, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.AuthConnection.Get(ctx, "password"); err != nil {
				t.Fatal("string-key account configuration was lost", err)
			}
			segments, err := client.Segment.Query().All(ctx)
			if err != nil || len(segments) != 1 || segments[0].TargetText == nil || *segments[0].TargetText != "preserved translation" {
				t.Fatal("translation changed", err)
			}
			if test.backup == "external" {
				if _, err := db.ExecContext(ctx, "UPDATE resources SET source_generation=source_generation+1 WHERE id=1"); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := runStorageOperation(ctx, opdir, "rollback", true, nil); err == nil {
					t.Fatal("rollback accepted a later generation")
				}
			} else {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := runStorageOperation(ctx, opdir, "rollback", true, nil); err != nil {
					t.Fatal("constrained metadata rollback", err)
				}
				db, client, err = snapshot.Open(ctx, &resolved.Config, true)
				if err != nil {
					t.Fatal(err)
				}
				resource, err := client.Resource.Get(ctx, 1)
				if err != nil || resource.CurrentSourceRevisionID != nil || resource.StoragePath != "source.txt" {
					t.Fatal("legacy reference was not restored", err)
				}
				db.Close()
			}
			t.Logf("PostgreSQL 18.6 storage/%s manifest-v1 and journal-loss acceptance passed; operation %s", test.backup, p.ID)
		})
	}
}

func currentPostgresStorageFixture(t *testing.T, dsn string) (string, *config.ResolvedServer) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = dir
	cfg.Database.Driver, cfg.Database.DSN = "postgres", dsn
	cfg.JWTSecret = "isolated-postgres-storage-acceptance-secret"
	cfg.SSE.MaxReplayEvents = 1000
	cfg.Storage.Initialization.CapacityBytes = config.QuotaInput{Set: true}
	cfg.Storage.Initialization.LogicalLimitBytes = config.QuotaInput{Set: true}
	keys, _, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	keyData, err := credential.EncodeKeyring(keys)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Credentials.KeyringFile = filepath.Join(dir, "keyring.json")
	if _, err := credential.PublishPrivateFile(cfg.Credentials.KeyringFile, keyData); err != nil {
		t.Fatal(err)
	}
	data, err := config.EncodeServerDocument(*cfg, config.LogConfig{Level: "info", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "deployment.yaml")
	if _, err := credential.PublishPrivateFile(path, data); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: "serve", ConfigPath: &path, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	db, client, err := snapshot.Open(ctx, &resolved.Config, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := snapshot.EmptyPostgres(ctx, db); err != nil {
		t.Fatal("dedicated storage acceptance database is not empty", err)
	}
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE linguaflow_auth_format (id INTEGER NOT NULL PRIMARY KEY CHECK (id=1), version INTEGER NOT NULL CHECK (version>0))"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO linguaflow_auth_format(id,version) VALUES(1,1)"); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("storage-original-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.User.Create().SetUsername("storageadmin").SetPasswordHash(string(hash)).SetRole("admin").SetActive(true).SetAuthVersion(1).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InstanceInitialization.Create().SetID(1).SetVersion(1).SetDataVersion(3).SetMode("serve").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.LocalAccountPolicy.Create().SetID(1).SetPasswordLoginEnabled(true).SetUsernameLoginEnabled(true).SetSignupEnabled(false).SetSignupRules(authrules.Unrestricted()).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AuthConnection.Create().SetID("password").SetKind("password").Save(ctx); err != nil {
		t.Fatal(err)
	}
	// Singleton inserts use explicit IDs; keep their owned sequences ahead of
	// existing rows just as the current runtime does when initializing them.
	for _, table := range []string{"instance_initializations", "local_account_policies"} {
		if _, err := db.ExecContext(ctx, "SELECT setval(pg_get_serial_sequence($1,'id'),101,true)", table); err != nil {
			t.Fatal(err)
		}
	}
	project, err := client.Project.Create().SetName("PG storage project").SetSourceLang("ja").SetTargetLang("en").SetOwnerUserID(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := client.Resource.Create().SetPath("source.txt").SetFormat("txt").SetStoragePath("source.txt").SetProjectID(project.ID).SetTotalSegments(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Segment.Create().SetResourceID(resource.ID).SetSegmentIndex(0).SetSourceText("hello").SetTargetText("preserved translation").Save(ctx); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "jobs")
	if err := credential.CreatePrivateDirectory(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := credential.PublishPrivateFile(filepath.Join(legacy, "source.txt"), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	return path, resolved
}
