package v015

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// Populate using only released columns. No current ORM user DTO is used to
// fabricate a source that could silently acquire the target account format.
func addReleasedBusiness(t *testing.T, db *sql.DB, keys *credential.Keyring, dir string) {
	t.Helper()
	now := time.Date(2026, 10, 22, 1, 2, 3, 456789123, time.UTC).Format(timeutil.SQLiteLayout)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("release fixture %s: %v", q, err)
		}
	}
	exec("INSERT INTO organizations(id,created_at,updated_at,name,slug) VALUES(7,?,?,'original-org','original-org')", now, now)
	exec("INSERT INTO org_memberships(id,created_at,updated_at,role,organization_memberships,user_memberships) VALUES(8,?,?,'owner',7,1)", now, now)
	exec("INSERT INTO projects(id,created_at,updated_at,name,config,owner_user_id) VALUES(10,?,?,'personal','{}',1)", now, now)
	exec("INSERT INTO projects(id,created_at,updated_at,name,config,owner_org_id) VALUES(11,?,?,'organization','{}',7)", now, now)
	for id, key := range map[int]string{21: "legacy.txt", 22: "missing.txt", 23: "uncertain.txt", 24: "imported.txt"} {
		exec("INSERT INTO resources(id,created_at,updated_at,path,format,storage_path,total_segments,project_id) VALUES(?,?,?,?,'txt',?,1,?)", id, now, now, key, key, 10+id%2)
		exec("INSERT INTO segments(id,created_at,updated_at,segment_index,source_text,target_text,status,resource_id,user_reviewed_segments) VALUES(?,?,?,0,'hello','existing translation','approved',?,1)", id+10, now, now, id)
	}
	exec("INSERT INTO jobs(id,created_at,updated_at,status,execution_plan_id,execution_config,resource_count,project_id,user_created_jobs) VALUES(40,?,?,'paused',70,?,1,11,1)", now, now, `{"schema_version":1,"credential_id":50,"original_payload":"preserve"}`)
	exec("INSERT INTO job_resources(id,created_at,updated_at,status,segment_ids,segment_count,job_job_resources,resource_job_resources) VALUES(41,?,?,'pending','[31]',1,40,21)", now, now)
	exec("INSERT INTO credentials(id,created_at,updated_at,scope,owner_id,provider,endpoint,current_version) VALUES(50,?,?,'user',1,'openai','https://api.example.invalid',1)", now, now)
	encrypted, err := keys.Encrypt("historical-llm-secret", credential.AssociatedData{ID: 50, Version: 1, Provider: "openai", Endpoint: "https://api.example.invalid", Scope: "user", OwnerID: 1})
	if err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO credential_versions(id,created_at,updated_at,version,encryption_version,key_id,nonce,ciphertext,credential_id) VALUES(51,?,?,1,?,?,?,?,50)", now, now, encrypted.Version, encrypted.KeyID, encrypted.Nonce, encrypted.Data)
	exec("INSERT INTO credential_job_references(id,credential_version_id,job_id) VALUES(52,51,40)")
	exec("INSERT INTO refresh_tokens(id,created_at,updated_at,token_hash,expires_at,user_refresh_tokens) VALUES(60,?,?,'old-token-hash',?,1)", now, now, now)
	exec("INSERT INTO activity_logs(id,created_at,updated_at,action,resource_type,resource_id,metadata,project_activity_logs,user_activity_logs) VALUES(61,?,?,'resource_upload','resource',21,'{}',11,1)", now, now)
	exec("INSERT INTO usage_records(id,created_at,updated_at,source,project_usage_records,user_usage_records) VALUES(62,?,?,'job',11,1)", now, now)
	exec("INSERT INTO system_settings(id,created_at,updated_at,key,value) VALUES(63,?,?,'storage_policy',?)", now, now, `{"mode":"site_only","default_choice":"site","generation":7,"historical_rule":{"enabled":false}}`)
	if err := credential.CreatePrivateDirectory(filepath.Join(dir, "jobs")); err != nil {
		t.Fatal(err)
	}
	for key, data := range map[string]string{"legacy.txt": "hello", "uncertain.txt": "bytes changed before inventory"} {
		if _, err := credential.PublishPrivateFile(filepath.Join(dir, "jobs", key), []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	// An existing revision includes retained history and a known immutable
	// logical digest. It must survive unchanged and stay out of the subplan.
	objectDir := filepath.Join(dir, "objects")
	if err := credential.CreatePrivateDirectory(objectDir); err != nil {
		t.Fatal(err)
	}
	if _, err := credential.PublishPrivateFile(filepath.Join(objectDir, "imported.txt"), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("hello"))
	digest := hex.EncodeToString(sum[:])
	exec("INSERT INTO storage_connections(id,created_at,updated_at,name,backend_id,driver) VALUES(71,?,?,'original-local','local','local')", now, now)
	exec("INSERT INTO storage_spaces(id,created_at,updated_at,name,identity,marker_nonce,verified,connection_id) VALUES(72,?,?,'original-space','original-space-identity','original-nonce',true,71)", now, now)
	exec("INSERT INTO blobs(id,created_at,updated_at,identity,project_id,owner_kind,owner_id,purpose,size,sha256,status,location_generation) VALUES(73,?,?,'original-blob',10,'user',1,'source',5,?,'ready',4)", now, now, digest)
	exec("INSERT INTO blob_locations(id,created_at,updated_at,object_key,size,status,integrity,blob_id,space_id) VALUES(74,?,?,'imported.txt',5,'live','available',73,72)", now, now)
	exec("UPDATE blobs SET active_location_id=74 WHERE id=73")
	exec("INSERT INTO source_revisions(id,created_at,updated_at,resource_id,project_id,format,parser_version,size,sha256,current,deleted,source_blob_id) VALUES(75,?,?,24,10,'txt','release-parser-v1',5,?,true,false,73)", now, now, digest)
	exec("UPDATE resources SET current_source_revision_id=75,source_generation=6 WHERE id=24")
}

func TestReleasedBusinessSnapshotCredentialsFilesAndDomainFacts(t *testing.T) {
	for _, mode := range []string{"serve", "local"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			options, dir := fixtureDeployment(t, mode)
			narrow, err := configSource(options)
			if err != nil {
				t.Fatal(err)
			}
			db, _, err := snapshot.Open(ctx, &narrow.Config, false)
			if err != nil {
				t.Fatal(err)
			}
			addReleasedBusiness(t, db, narrow.CredentialKeys, dir)
			db.Close()
			p, err := NewPlan(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("resource committed before checkpoint")
			calls := 0
			_, err = Run(ctx, RunOptions{Directory: options.Directory, Offline: true, Fault: func(at string) error {
				if at == "resource_commit" {
					calls++
					if calls == 2 {
						return injected
					}
				}
				return nil
			}})
			if !errors.Is(err, injected) {
				t.Fatalf("resource fault: %v", err)
			}
			if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
				t.Fatal("resume", err)
			}
			if _, err := VerifyOperation(ctx, options.Directory); err != nil {
				t.Fatal("verify", err)
			}
			cfg := narrow.Config
			cfg.DataDir = options.TargetDataDir
			cfg.Database.DSN = ""
			target, client, err := snapshot.Open(ctx, &cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			if c, err := client.SourceRevision.Query().Count(ctx); err != nil || c != 4 {
				t.Fatalf("revision count=%d err=%v", c, err)
			}
			revision, err := client.SourceRevision.Get(ctx, 75)
			if err != nil || revision.ParserVersion != "release-parser-v1" || revision.SourceBlobID != 73 {
				t.Fatal("existing SourceRevision replaced", err)
			}
			for _, id := range []int{22, 23} {
				r, err := client.Resource.Get(ctx, id)
				if err != nil || r.CurrentSourceRevisionID == nil {
					t.Fatal("explicit domain fact missing", err)
				}
				rev, err := client.SourceRevision.Get(ctx, *r.CurrentSourceRevisionID)
				if err != nil || rev.VerificationState != "legacy_unverified" {
					t.Fatal("unknown source invented", err)
				}
			}
			job, err := client.Job.Get(ctx, 40)
			if err != nil || job.ExecutionConfig["original_payload"] != "preserve" || job.Status != "failed" {
				t.Fatal("historical job payload lost", err)
			}
			if _, err := operation.Hash(ctx, filepath.Join(options.TargetDataDir, "objects", "imported.txt")); err != nil {
				t.Fatal("independent current object copy missing", err)
			}
			_, j, err := operation.Load(options.Directory)
			if err != nil {
				t.Fatal(err)
			}
			backup, err := LoadBackupReceipt(ctx, j.BackupDirectory, p)
			if err != nil {
				t.Fatal(err)
			}
			backup.Objects = nil
			if err := VerifyBackupReceipt(ctx, j.BackupDirectory, p, backup); err == nil {
				t.Fatal("omitted object receipt accepted")
			}
		})
	}
}

func configSource(options PlanOptions) (*config.ResolvedServer, error) {
	n, err := config.ResolveMigrationDatabase(options.Inputs)
	if err != nil {
		return nil, err
	}
	return ResolveProbedInputs(options.Inputs, n)
}
