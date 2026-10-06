package v013

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

const localOldTimestamp = "2026-09-29 11:00:00.123456789 +0800 CST"

func localFixture(t *testing.T, business bool) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "LinguaFlow")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db := openLocalFixture(t, dir)
	data, err := os.ReadFile("testdata/v013-sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(data)); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO users(id,created_at,updated_at,username,password_hash,email,role,active) VALUES(1,?,?,'other-admin','other-password-hash','other@example.com','admin',true)`,
		`INSERT INTO users(id,created_at,updated_at,username,password_hash,email,role,active) VALUES(5,?,?,'local','unchanged-local-password-hash','local@linguaflow.local','admin',true)`,
	}
	if business {
		statements = append(statements,
			`INSERT INTO projects(id,created_at,updated_at,name,config,owner_user_id) VALUES(1,?,?,'old project','{}',5)`,
			`INSERT INTO backends(id,created_at,updated_at,name,scope,backend_type,options,owner_user_id) VALUES(1,?,?,'old backend','user','openai','{"model":"old-model","api_key":"backend-original-key"}',5)`,
			`INSERT INTO resources(id,created_at,updated_at,path,format,storage_path,total_segments,project_id) VALUES(1,?,?,'test.txt','txt','resources/test.txt',1,1)`,
			`INSERT INTO segments(id,created_at,updated_at,segment_index,source_text,target_text,status,resource_id) VALUES(1,?,?,0,'old source','saved target','translated',1)`,
			`INSERT INTO execution_profiles(id,created_at,updated_at,name,scope,owner_user_id,config) VALUES(1,?,?,'old profile','user',5,'{"glossary":{"bootstrap":{"enabled":true,"max_terms_per_1000_chars":3,"min_source_len":2,"inline_conflict_strategy":"rewrite-local"}},"ruby":{"enabled":false}}')`,
			`INSERT INTO jobs(id,created_at,updated_at,project_id,execution_plan_id,status,progress_total,progress_completed,execution_config) VALUES(1,?,?,1,1,'paused',40,17,'{"execution_plan_id":1,"execution_plan_name":"old-plan","source_lang":"ja","target_lang":"en","strategy":{"glossary":{"bootstrap":{"enabled":false,"inline_conflict_strategy":"rewrite-local"}},"context":{"enabled":true,"before":1,"after":1}},"rounds":[{"mode":"translate","backend":{"id":1,"scope":"user","name":"old-backend","type":"openai","options":{"model":"old-model","api_key":"job-original-key"}},"translate":{"prompt":{"template_name":"saved","content":"old custom prompt"},"batch_size":10,"concurrency":1,"fallback_shrink":1,"segment_filter":{"status_filter":"all"},"retry":{"max_attempts":0,"backoff_ms":0,"jitter":false}}}]}')`,
			`INSERT INTO job_resources(id,created_at,updated_at,status,segment_ids,segment_count,completed_segments,output_path,job_job_resources,resource_job_resources) VALUES(1,?,?,'running','[1]',1,0,'outputs/result.txt',1,1)`,
			`INSERT INTO refresh_tokens(id,created_at,updated_at,token_hash,expires_at,user_refresh_tokens) VALUES(1,?,?,'old-refresh-hash','2027-01-01T00:00:00Z',5)`,
		)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement, localOldTimestamp, localOldTimestamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if business {
		for path, data := range map[string]string{"jobs/resources/test.txt": "original input", "jobs/outputs/result.txt": "saved output", "custom/settings.txt": "unknown user file"} {
			absolute := filepath.Join(dir, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(absolute, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

func openLocalFixture(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteURI(filepath.Join(dir, localDatabaseName), "rwc")+"&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func readLocalBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLocalMigrationRehearsalApplyAndPublishedRecovery(t *testing.T) {
	dir := localFixture(t, true)
	oldDatabase := readLocalBytes(t, filepath.Join(dir, localDatabaseName))
	oldSecret, err := config.PrepareLocalSecret(filepath.Join(dir, "instance-secret"))
	if err != nil {
		t.Fatal(err)
	}
	rehearsal, err := RunLocal(context.Background(), LocalOptions{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if rehearsal.Applied || rehearsal.CredentialsCreated != 2 || rehearsal.Profiles != 1 || rehearsal.Jobs != 1 {
		t.Fatalf("rehearsal: %+v", rehearsal)
	}
	if !bytes.Equal(oldDatabase, readLocalBytes(t, filepath.Join(dir, localDatabaseName))) {
		t.Fatal("rehearsal changed original database")
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials-keyring.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rehearsal published a keyring")
	}
	stages, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".LinguaFlow.v013-stage-*"))
	if len(stages) != 0 {
		t.Fatalf("rehearsal left stages: %v", stages)
	}
	result, err := RunLocal(context.Background(), LocalOptions{DataDir: dir, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.BackupDir == "" {
		t.Fatalf("apply: %+v", result)
	}
	if !bytes.Equal(oldDatabase, readLocalBytes(t, filepath.Join(result.BackupDir, localDatabaseName))) {
		t.Fatal("backup differs from old database")
	}
	if secret, err := config.ReadLocalSecret(filepath.Join(dir, "instance-secret")); err != nil || secret != oldSecret {
		t.Fatal("existing local secret was changed", err)
	}
	for path, expected := range map[string]string{"jobs/resources/test.txt": "original input", "jobs/outputs/result.txt": "saved output", "custom/settings.txt": "unknown user file"} {
		if string(readLocalBytes(t, filepath.Join(dir, filepath.FromSlash(path)))) != expected {
			t.Fatalf("file %s changed", path)
		}
		if string(readLocalBytes(t, filepath.Join(result.BackupDir, filepath.FromSlash(path)))) != expected {
			t.Fatalf("backup file %s changed", path)
		}
	}
	cfg := config.DefaultServerConfig()
	cfg.DataDir = dir
	cfg.AutoMigrate = false
	db, client, err := database.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	local, err := service.NewInitializationService(client).Validate(context.Background(), config.ModeLocal)
	if err != nil || local.ID != 5 || local.PasswordHash != "unchanged-local-password-hash" {
		t.Fatalf("local identity: %v %v", local, err)
	}
	var stored string
	if err := db.QueryRow(`SELECT CAST(created_at AS TEXT) FROM users WHERE id=5`).Scan(&stored); err != nil || stored != "2026-09-29T03:00:00.123456789Z" {
		t.Fatalf("time=%s err=%v", stored, err)
	}
	var status, snapshot string
	var total, completed int
	if err := db.QueryRow("SELECT status,progress_total,progress_completed,execution_config FROM jobs WHERE id=1").Scan(&status, &total, &completed, &snapshot); err != nil {
		t.Fatal(err)
	}
	if status != "paused" || total != 40 || completed != 17 || strings.Contains(snapshot, "job-original-key") || !strings.Contains(snapshot, "old custom prompt") {
		t.Fatalf("job not preserved: %s", snapshot)
	}
	keys, err := credential.LoadKeyring(filepath.Join(dir, "credentials-keyring.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.NewCredentialService(client, keys, nil).ValidateKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	// A published directory may already be receiving new work when recovery
	// runs. The persisted receipt, not a database hash, identifies publication.
	if err := os.WriteFile(filepath.Join(dir, "new-work.txt"), []byte("keep new work"), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := RunLocal(context.Background(), LocalOptions{DataDir: dir, Apply: true})
	if err != nil || !again.Applied || again.Recovery != "published" || again.BackupDir != result.BackupDir {
		t.Fatalf("published recovery=%+v err=%v", again, err)
	}
	if string(readLocalBytes(t, filepath.Join(dir, "new-work.txt"))) != "keep new work" {
		t.Fatal("recovery rolled back published data")
	}
}

func TestLocalMigrationFailurePreservesOriginal(t *testing.T) {
	for _, kind := range []string{"invalid-time", "missing-local", "inactive-local", "non-admin-local", "missing-resource", "unsafe-resource", "mixed-schema"} {
		t.Run(kind, func(t *testing.T) {
			dir := localFixture(t, true)
			db := openLocalFixture(t, dir)
			statement := map[string]string{
				"invalid-time":     `UPDATE users SET created_at='2026-01-01 00:00:00' WHERE id=5`,
				"missing-local":    `UPDATE users SET username='renamed-local' WHERE id=5`,
				"inactive-local":   `UPDATE users SET active=false WHERE id=5`,
				"non-admin-local":  `UPDATE users SET role='user' WHERE id=5`,
				"missing-resource": `UPDATE resources SET storage_path='resources/missing.txt'`,
				"unsafe-resource":  `UPDATE resources SET storage_path='../outside.txt'`,
				"mixed-schema":     `CREATE TABLE credentials(id INTEGER PRIMARY KEY)`,
			}[kind]
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := readLocalBytes(t, filepath.Join(dir, localDatabaseName))
			result, err := RunLocal(context.Background(), LocalOptions{DataDir: dir, Apply: true})
			if err == nil || result.Applied {
				t.Fatalf("accepted %s", kind)
			}
			if !bytes.Equal(before, readLocalBytes(t, filepath.Join(dir, localDatabaseName))) {
				t.Fatal("failure changed source")
			}
			backups, _ := filepath.Glob(dir + ".v013-backup-*")
			if len(backups) != 0 {
				t.Fatalf("published backup before validation: %v", backups)
			}
		})
	}
}

func preparedLocalSwitch(t *testing.T) (localJournal, string) {
	t.Helper()
	parent := t.TempDir()
	source := filepath.Join(parent, "LinguaFlow")
	runID := "0123456789abcdef01234567"
	j := localJournal{Version: 1, RunID: runID, SourceDir: source, StageDir: filepath.Join(parent, ".LinguaFlow.v013-stage-"+runID), BackupDir: source + ".v013-backup-" + runID}
	for path, data := range map[string]string{source: "old", j.StageDir: "new"} {
		if err := credential.PreparePrivateDirectory(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "content"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	j.SourceIdentity, err = directoryIdentity(source)
	if err != nil {
		t.Fatal(err)
	}
	j.StageIdentity, err = directoryIdentity(j.StageDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishLocalJSON(filepath.Join(j.StageDir, localReceiptName), localReceipt{1, runID, true}); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(parent, ".LinguaFlow.v013-switch.json")
	if err := publishLocalJSON(journalPath, j); err != nil {
		t.Fatal(err)
	}
	return j, journalPath
}

func TestLocalSwitchRecoveryBoundaries(t *testing.T) {
	for _, phase := range []string{"prepared", "old-renamed", "published", "source-occupied", "stage-missing"} {
		t.Run(phase, func(t *testing.T) {
			j, path := preparedLocalSwitch(t)
			if phase != "prepared" {
				if err := renameAbsent(j.SourceDir, j.BackupDir); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "published" {
				if err := renameAbsent(j.StageDir, j.SourceDir); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "source-occupied" {
				if err := os.Mkdir(j.SourceDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(j.SourceDir, "content"), []byte("unrelated"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "stage-missing" {
				if err := renameAbsent(j.StageDir, j.StageDir+"-moved-by-user"); err != nil {
					t.Fatal(err)
				}
			}
			result, found, err := recoverLocal(j.SourceDir, path, true)
			if !found {
				t.Fatal("recovery journal not recognized")
			}
			switch phase {
			case "published":
				if err != nil || !result.Applied || result.Recovery != "published" {
					t.Fatalf("%+v %v", result, err)
				}
				if string(readLocalBytes(t, filepath.Join(j.SourceDir, "content"))) != "new" {
					t.Fatal("published directory rolled back")
				}
			case "source-occupied", "stage-missing":
				if err == nil {
					t.Fatal("ambiguous recovery accepted")
				}
				if string(readLocalBytes(t, filepath.Join(j.BackupDir, "content"))) != "old" {
					t.Fatal("backup altered")
				}
			default:
				if err != nil || result.Applied || result.Recovery != "restored" {
					t.Fatalf("%+v %v", result, err)
				}
				if string(readLocalBytes(t, filepath.Join(j.SourceDir, "content"))) != "old" {
					t.Fatal("old directory not restored")
				}
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("recovered journal retained")
				}
			}
		})
	}
}

func TestLocalSwitchFailureAndExplicitRecovery(t *testing.T) {
	j, path := preparedLocalSwitch(t)
	calls := 0
	err := switchLocalDirectories(j, func(from, to string) error {
		calls++
		if calls == 2 {
			return errors.New("injected sharing violation")
		}
		return renameAbsent(from, to)
	})
	if err == nil {
		t.Fatal("expected switch failure")
	}
	if _, _, err := recoverLocal(j.SourceDir, path, false); err == nil {
		t.Fatal("rehearsal performed a recovery mutation")
	}
	if exists, _ := pathExists(j.SourceDir); exists {
		t.Fatal("rehearsal restored directory")
	}
	result, _, err := recoverLocal(j.SourceDir, path, true)
	if err != nil || result.Recovery != "restored" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestLocalMigrationLockAndUnsafePaths(t *testing.T) {
	dir := localFixture(t, false)
	lockPath := filepath.Join(filepath.Dir(dir), ".LinguaFlow.v013-migration.lock")
	unlock, err := acquireLocalLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := RunLocal(context.Background(), LocalOptions{DataDir: dir}); err == nil {
		t.Fatal("concurrent migration acquired lock")
	}
	if _, _, err := localPaths(filepath.VolumeName(dir) + string(os.PathSeparator)); err == nil {
		t.Fatal("volume root accepted")
	}
}

func TestLocalPlatformRenameNeverReplacesExistingEmptyDirectory(t *testing.T) {
	parent := t.TempDir()
	source, target := filepath.Join(parent, "source"), filepath.Join(parent, "target")
	for _, path := range []string{source, target} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := directoryIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the platform primitive directly, after the point at which a
	// concurrent process could defeat renameAbsent's friendly preflight.
	if err := renameLocalNoReplace(source, target); err == nil {
		t.Fatal("overwrote existing empty directory")
	}
	after, err := directoryIdentity(target)
	if err != nil || after != identity {
		t.Fatalf("target changed: %v", err)
	}
	if exists, err := pathExists(source); err != nil || !exists {
		t.Fatalf("source disappeared: %v", err)
	}
}
