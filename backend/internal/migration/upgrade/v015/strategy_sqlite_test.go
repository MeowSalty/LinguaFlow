package v015

import (
	"bufio"
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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/api"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	storagemigrate "github.com/MeowSalty/LinguaFlow/backend/internal/migration/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestSQLiteDefaultInplaceServeAndLocal(t *testing.T) {
	for _, mode := range []string{"serve", "local"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			options, sourceDir := fixtureDeployment(t, mode)
			cloneTarget := options.TargetDataDir
			options.TargetDataDir = ""
			p, err := NewPlan(ctx, options)
			if err != nil {
				t.Fatal("plan", err)
			}
			if p.Strategy != operation.Inplace || p.DatabaseBackup != operation.BackupAuto || p.Target.DataDir != "" || p.Target.ConfigPath != "" || p.Target.Identity != "" {
				t.Fatalf("unexpected in-place plan: strategy=%q backup=%q target=%+v", p.Strategy, p.DatabaseBackup, p.Target)
			}
			for _, root := range p.LocalRoots {
				if root.Source != root.Target {
					t.Fatalf("in-place plan redirects local storage: %+v", root)
				}
			}
			report, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true})
			if err != nil {
				t.Fatal("in-place apply", err)
			}
			if !report.Verified || report.Phase != "completed" || report.Strategy != operation.Inplace {
				t.Fatalf("apply report: %+v", report)
			}
			if _, err := os.Stat(cloneTarget); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("in-place apply created clone target %q: %v", cloneTarget, err)
			}
			resolved, err := configSource(options)
			if err != nil {
				t.Fatal(err)
			}
			db, _, err := snapshot.Open(ctx, &resolved.Config, true)
			if err != nil {
				t.Fatal(err)
			}
			inspection, probeErr := Probe(ctx, db, "sqlite")
			closeErr := db.Close()
			if errors.Join(probeErr, closeErr) != nil || inspection.Format != "current" {
				t.Fatalf("original database was not upgraded in place: inspection=%+v err=%v", inspection, errors.Join(probeErr, closeErr))
			}
			if mode == "local" {
				deployment := filepath.Join(sourceDir, "source.yml")
				resolvedLocal, err := config.ResolveServerConfig(config.ServerInputs{Mode: mode, ConfigPath: &deployment, Environment: map[string]string{}})
				if err != nil {
					t.Fatal("resolve local deployment", err)
				}
				db, client, err := database.Open(ctx, &resolvedLocal.Config)
				if err != nil {
					t.Fatal("open upgraded local database", err)
				}
				marker, markerErr := client.InstanceInitialization.Get(ctx, 1)
				closeErr := db.Close()
				if errors.Join(markerErr, closeErr) != nil || marker.LocalUserID == nil || *marker.LocalUserID != 1 {
					t.Fatalf("local identity changed: marker=%+v err=%v", marker, errors.Join(markerErr, closeErr))
				}
			}
		})
	}
}

func TestSQLiteRehearsalUsesIndependentCloneAndPreservesSourceFingerprint(t *testing.T) {
	ctx := context.Background()
	options, _ := fixtureDeployment(t, "serve")
	options.TargetDataDir = ""
	p, err := NewPlan(ctx, options)
	if err != nil {
		t.Fatal("plan", err)
	}
	before := inspectSQLiteSource(t, ctx, options)
	identityBefore := p.Source.Identity
	report, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true, Rehearse: true})
	if err != nil {
		t.Fatal("rehearsal", err)
	}
	if !report.Verified || report.Rehearsal != "verified" || report.Strategy != operation.Clone {
		t.Fatalf("rehearsal report: %+v", report)
	}
	var rehearsalPlan operation.Plan
	if err := operation.Read(filepath.Join(options.Directory, "rehearsal", "plan.json"), &rehearsalPlan); err != nil {
		t.Fatal("read independent rehearsal plan", err)
	}
	if rehearsalPlan.Strategy != operation.Clone || rehearsalPlan.Target.DataDir == "" || rehearsalPlan.Target.DataDir == p.Source.DataDir {
		t.Fatalf("rehearsal was not isolated from the in-place source: %+v", rehearsalPlan.Target)
	}
	after := inspectSQLiteSource(t, ctx, options)
	if before.Fingerprint != after.Fingerprint || before.SchemaDigest != after.SchemaDigest || identityBefore != afterSourceIdentity(t, ctx, options) {
		t.Fatalf("rehearsal changed source baseline: before=%+v after=%+v", before, after)
	}
}

func TestSQLiteDurableFaultsResumeInplace(t *testing.T) {
	phases := []string{"baseline", "backup", "preparing", "schema_commit", "accounts_commit", "history_commit", "storage_commit", "ready_commit", "before_report", "before_publish", "publish"}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			options, _ := fixtureDeployment(t, "serve")
			options.TargetDataDir = ""
			p, err := NewPlan(ctx, options)
			if err != nil {
				t.Fatal("plan", err)
			}
			var planOnDisk operation.Plan
			if err := operation.Read(filepath.Join(options.Directory, "plan.json"), &planOnDisk); err != nil || operation.Digest(&planOnDisk) != operation.Digest(p) {
				t.Fatalf("immutable plan missing or changed before execution: err=%v", err)
			}
			injected := errors.New("injected durable boundary interruption")
			fired := false
			_, err = Run(ctx, RunOptions{Directory: options.Directory, Offline: true, Fault: func(at string) error {
				if at == phase {
					fired = true
					return injected
				}
				return nil
			}})
			if !fired || !errors.Is(err, injected) {
				t.Fatalf("fault %q did not interrupt at its durable boundary: fired=%t err=%v", phase, fired, err)
			}
			report, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true})
			if err != nil {
				t.Fatal("resume", err)
			}
			if !report.Verified || report.Phase != "completed" || report.Strategy != operation.Inplace {
				t.Fatalf("resumed report: %+v", report)
			}
			if _, err := VerifyOperation(ctx, options.Directory); err != nil {
				t.Fatal("verify resumed operation", err)
			}
		})
	}
}

func TestSQLiteReadyCommitRebuildsProgressWithoutJournal(t *testing.T) {
	ctx := context.Background()
	options, _ := fixtureDeployment(t, "serve")
	options.TargetDataDir = ""
	if _, err := NewPlan(ctx, options); err != nil {
		t.Fatal("plan", err)
	}
	injected := errors.New("stop after ready database commit")
	_, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true, Fault: func(at string) error {
		if at == "ready_commit" {
			return injected
		}
		return nil
	}})
	if !errors.Is(err, injected) {
		t.Fatalf("ready commit interruption: %v", err)
	}
	if err := os.Remove(filepath.Join(options.Directory, "journal.json")); err != nil {
		t.Fatal("remove lagging journal", err)
	}
	report, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true})
	if err != nil {
		t.Fatal("rebuild journal progress from database receipts", err)
	}
	if !report.Verified || report.Phase != "completed" {
		t.Fatalf("reconstructed run report: %+v", report)
	}
	_, journal, err := operation.Load(options.Directory)
	if err != nil || journal.Phase != "completed" || !contains(journal.Steps, "ready") {
		t.Fatalf("database-ready state was not reconstructed: journal=%+v err=%v", journal, err)
	}
}

func TestSQLiteRollbackResumesEveryRestorePublicationBoundary(t *testing.T) {
	phases := []string{"restore_prepared", "before_restore_displace", "restore_displaced", "restore_published", "restore_verified"}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			options, sourceDir, p, originals := newSQLiteRollbackFixture(t)
			if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
				t.Fatal("in-place apply", err)
			}
			dbPath, err := snapshot.SQLitePath(p.Source.DatabaseDSN)
			if err != nil {
				t.Fatal(err)
			}
			walBefore := leaveSQLiteWALAfterWriterCrash(t, dbPath)
			injected := errors.New("injected SQLite restore interruption")
			fired := false
			_, err = RollbackOperationWithOptions(ctx, options.Directory, RunOptions{Offline: true, Fault: func(at string) error {
				if at == phase {
					fired = true
					return injected
				}
				return nil
			}})
			if !fired || !errors.Is(err, injected) {
				t.Fatalf("restore boundary %q did not interrupt: fired=%t err=%v", phase, fired, err)
			}
			_, interrupted, err := operation.Load(options.Directory)
			if err != nil || interrupted.Restore == nil {
				t.Fatalf("durable restore intent missing after interruption: journal=%+v err=%v", interrupted, err)
			}
			restoreDir := interrupted.Restore.Directory
			failed, err := operation.Hash(ctx, filepath.Join(restoreDir, interrupted.Restore.FailedDatabase.Name))
			if err != nil || failed != interrupted.Restore.FailedDatabase {
				t.Fatalf("failed converted database was not preserved: artifact=%+v err=%v", failed, err)
			}
			preservedWAL, err := operation.Hash(ctx, filepath.Join(restoreDir, "failed-main.sqlite-wal"))
			if err != nil || preservedWAL.SHA256 != walBefore.SHA256 || preservedWAL.Size != walBefore.Size {
				t.Fatalf("raw WAL was not preserved: before=%+v preserved=%+v err=%v", walBefore, preservedWAL, err)
			}
			report, err := RollbackOperationWithOptions(ctx, options.Directory, RunOptions{Offline: true})
			if err != nil {
				t.Fatal("resume SQLite rollback", err)
			}
			if report.Phase != "rolled_back" || report.Restoration != "verified" || report.Recovery == nil {
				t.Fatalf("restoration report: %+v", report)
			}
			assertOriginalSQLiteRestored(t, ctx, options, sourceDir, p, originals, interrupted.Restore.ReplacementIdentity)
		})
	}
}

func TestSQLiteRollbackRefusesBusinessLoginAfterReady(t *testing.T) {
	ctx := context.Background()
	options, _, _, _ := newSQLiteRollbackFixture(t)
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
		t.Fatal("in-place apply", err)
	}
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	db, client, err := database.Open(ctx, &resolved.Config)
	if err != nil {
		t.Fatal("open upgraded service database", err)
	}
	auth := service.NewAuthService(client, service.AuthConfigFromServer(resolved.Config), service.NewSettingsService(client))
	if _, err := auth.Login(ctx, service.LoginInput{Identifier: "OriginalAdmin", Password: "original-password-123"}); err != nil {
		db.Close()
		t.Fatal("business login", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = RollbackOperationWithOptions(ctx, options.Directory, RunOptions{Offline: true})
	var classified *operation.Error
	if !errors.As(err, &classified) || classified.Code != operation.NeedsAction || !strings.Contains(err.Error(), "business writes occurred") {
		t.Fatalf("rollback after login = %v, want explicit business-write refusal", err)
	}
}

func TestSQLiteInplaceUpgradeRejectsOldAccessToken(t *testing.T) {
	ctx := context.Background()
	options, _ := fixtureDeployment(t, "serve")
	options.TargetDataDir = ""
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlan(ctx, options); err != nil {
		t.Fatal("plan", err)
	}
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
		t.Fatal("in-place apply", err)
	}
	db, client, err := database.Open(ctx, &resolved.Config)
	if err != nil {
		t.Fatal("open upgraded service database", err)
	}
	server, err := api.NewServer(&resolved.Config, resolved.CredentialKeys, slog.New(slog.NewTextHandler(io.Discard, nil)), db, client, config.ModeServer, nil)
	if err != nil {
		db.Close()
		t.Fatal("initialize API", err)
	}
	defer server.Shutdown(ctx)
	defer db.Close()
	claims := jwt.MapClaims{"uid": 1, "username": "OriginalAdmin", "role": "admin", "iss": resolved.Config.JWTIssuer, "sub": "user:1", "exp": time.Now().Add(time.Hour).Unix()}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(resolved.Config.JWTSecret))
	if err != nil {
		t.Fatal("sign legacy token", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.GetCurrentUser(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("old access token status=%d, want 401", response.Code)
	}
	auth := service.NewAuthService(client, service.AuthConfigFromServer(resolved.Config), service.NewSettingsService(client))
	session, err := auth.Login(ctx, service.LoginInput{Identifier: "OriginalAdmin", Password: "original-password-123"})
	if err != nil {
		t.Fatal("original credentials after upgrade", err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	request.Header.Set("Authorization", "Bearer "+session.AccessToken)
	response = httptest.NewRecorder()
	server.GetCurrentUser(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("new access token status=%d, want 200: %s", response.Code, response.Body.String())
	}
}

type originalSQLiteArtifact struct {
	Proof    operation.Artifact
	Identity string
}

func newSQLiteRollbackFixture(t *testing.T) (PlanOptions, string, *operation.Plan, map[string]originalSQLiteArtifact) {
	t.Helper()
	ctx := context.Background()
	options, sourceDir := fixtureDeployment(t, "serve")
	options.TargetDataDir = ""
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	db, _, err := snapshot.Open(ctx, &resolved.Config, false)
	if err != nil {
		t.Fatal(err)
	}
	addReleasedBusiness(t, db, resolved.CredentialKeys, sourceDir)
	if _, err := db.ExecContext(ctx, "UPDATE sqlite_sequence SET seq=120 WHERE name='users'"); err != nil {
		db.Close()
		t.Fatal("raise released users sequence high-water", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	originals := map[string]originalSQLiteArtifact{}
	for _, path := range []string{
		filepath.Join(sourceDir, "credentials-keyring.json"),
		filepath.Join(sourceDir, "jobs", "legacy.txt"),
		filepath.Join(sourceDir, "jobs", "uncertain.txt"),
		filepath.Join(sourceDir, "objects", "imported.txt"),
	} {
		artifact, err := operation.Hash(ctx, path)
		if err != nil {
			t.Fatal("hash original protected file", path, err)
		}
		identity, err := operation.Identity(path)
		if err != nil {
			t.Fatal("identify original protected file", path, err)
		}
		originals[path] = originalSQLiteArtifact{Proof: artifact, Identity: identity}
	}
	p, err := NewPlan(ctx, options)
	if err != nil {
		t.Fatal("plan", err)
	}
	if p.Strategy != operation.Inplace || p.Source.Driver != "sqlite" {
		t.Fatalf("fixture did not create SQLite inplace plan: %+v", p)
	}
	return options, sourceDir, p, originals
}

func assertOriginalSQLiteRestored(t *testing.T, ctx context.Context, options PlanOptions, sourceDir string, p *operation.Plan, originals map[string]originalSQLiteArtifact, replacementIdentity string) {
	t.Helper()
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	db, _, err := snapshot.Open(ctx, &resolved.Config, true)
	if err != nil {
		t.Fatal("open restored source", err)
	}
	inspection, probeErr := Probe(ctx, db, "sqlite")
	var sequence int64
	sequenceErr := db.QueryRowContext(ctx, "SELECT seq FROM sqlite_sequence WHERE name='users'").Scan(&sequence)
	keyErr := VerifySourceKeys(ctx, db, resolved.CredentialKeys)
	identity, identityErr := EndpointIdentity(ctx, db, resolved.Config)
	closeErr := db.Close()
	if errors.Join(probeErr, sequenceErr, keyErr, identityErr, closeErr) != nil {
		t.Fatalf("verify restored original database: %v", errors.Join(probeErr, sequenceErr, keyErr, identityErr, closeErr))
	}
	if inspection.Format != "v015" || inspection.Fingerprint != p.SourceFingerprint || inspection.SchemaDigest != p.SourceSchema {
		t.Fatalf("restored source baseline differs: %+v", inspection)
	}
	if sequence < 120 {
		t.Fatalf("restored users sequence=%d, want original high-water >=120", sequence)
	}
	dbPath, err := snapshot.SQLitePath(p.Source.DatabaseDSN)
	if err != nil {
		t.Fatal(err)
	}
	fileIdentity, err := operation.Identity(dbPath)
	if err != nil || fileIdentity != replacementIdentity {
		t.Fatalf("restored database file identity=%q, want recovery replacement %q: %v", fileIdentity, replacementIdentity, err)
	}
	if identity == p.Source.Identity {
		t.Fatal("restored file identity unexpectedly reused the pre-upgrade endpoint identity")
	}
	_, journal, err := operation.Load(options.Directory)
	if err != nil || journal.Restore == nil || journal.Restore.RestoredIdentity != identity {
		t.Fatalf("journal did not register verified restoration identity: restore=%+v err=%v", journal.Restore, err)
	}
	for path, expected := range originals {
		actual, err := operation.Hash(ctx, path)
		if err != nil || actual.Size != expected.Proof.Size || actual.SHA256 != expected.Proof.SHA256 {
			t.Fatalf("original protected file changed at %s: got=%+v want=%+v err=%v", path, actual, expected.Proof, err)
		}
		actualIdentity, err := operation.Identity(path)
		if err != nil {
			t.Fatal("check original file identity", path, err)
		}
		if actualIdentity != expected.Identity {
			t.Fatalf("original file identity changed at %s: before=%q after=%q", path, expected.Identity, actualIdentity)
		}
	}
	if _, err := os.Stat(sourceDir); err != nil {
		t.Fatal("source deployment directory was not retained", err)
	}
}

func inspectSQLiteSource(t *testing.T, ctx context.Context, options PlanOptions) *Inspection {
	t.Helper()
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	db, _, err := snapshot.Open(ctx, &resolved.Config, true)
	if err != nil {
		t.Fatal(err)
	}
	inspection, probeErr := Probe(ctx, db, "sqlite")
	closeErr := db.Close()
	if err := errors.Join(probeErr, closeErr); err != nil {
		t.Fatal("inspect SQLite source", err)
	}
	return inspection
}

func afterSourceIdentity(t *testing.T, ctx context.Context, options PlanOptions) string {
	t.Helper()
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	db, _, err := snapshot.Open(ctx, &resolved.Config, true)
	if err != nil {
		t.Fatal(err)
	}
	identity, identityErr := EndpointIdentity(ctx, db, resolved.Config)
	closeErr := db.Close()
	if err := errors.Join(identityErr, closeErr); err != nil {
		t.Fatal(err)
	}
	return identity
}

func leaveSQLiteWALAfterWriterCrash(t *testing.T, databasePath string) operation.Artifact {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("locate test executable", err)
	}
	cmd := exec.Command(executable, "-test.run=^TestSQLiteWALCrashWriter$")
	cmd.Env = append(os.Environ(), "LINGUAFLOW_SQLITE_WAL_CRASH_PATH="+databasePath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("open WAL helper output", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal("start WAL helper", err)
	}
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && strings.TrimSpace(line) != "ready" {
			err = fmt.Errorf("unexpected WAL helper readiness line %q", strings.TrimSpace(line))
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("wait for WAL writer", err)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("WAL writer did not reach its committed-write boundary")
	}
	if err := cmd.Process.Kill(); err != nil {
		_ = cmd.Wait()
		t.Fatal("terminate WAL writer", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("WAL writer exited normally; expected abrupt process termination")
	}
	wal, err := operation.Hash(context.Background(), databasePath+"-wal")
	if err != nil || wal.Size == 0 {
		t.Fatalf("crashed writer did not leave a raw SQLite WAL: artifact=%+v err=%v", wal, err)
	}
	return wal
}

func TestSQLiteWALCrashWriter(t *testing.T) {
	databasePath := os.Getenv("LINGUAFLOW_SQLITE_WAL_CRASH_PATH")
	if databasePath == "" {
		return
	}
	db, err := sql.Open("sqlite", databasePath+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)")
	if err != nil {
		t.Fatal("open WAL database", err)
	}
	if _, err := db.Exec("UPDATE users SET username=username WHERE id=1"); err != nil {
		t.Fatal("commit WAL fixture update", err)
	}
	if _, err := io.WriteString(os.Stdout, "ready\n"); err != nil {
		t.Fatal("signal WAL writer readiness", err)
	}
	select {}
}

// A killed process does not run Run's deferred journal or connection cleanup.
// The database receipts and original protection must suffice on their own.
func TestSQLiteKilledUpgradeResumesCommittedDatabase(t *testing.T) {
	for _, test := range []struct {
		strategy operation.Strategy
		phase    string
	}{
		{operation.Inplace, "backup"},
		{operation.Inplace, "preparing"},
		{operation.Inplace, "schema_commit"},
		{operation.Inplace, "accounts_commit"},
		{operation.Inplace, "history_commit"},
		{operation.Inplace, "storage_commit"},
		{operation.Inplace, "ready_commit"},
		{operation.Inplace, "before_report"},
		{operation.Clone, "accounts_commit"},
		{operation.Clone, "ready_commit"},
		{operation.Clone, "before_publish"},
		{operation.Clone, "publish"},
	} {
		t.Run(string(test.strategy)+"/"+test.phase, func(t *testing.T) {
			options, _ := fixtureDeployment(t, "serve")
			if test.strategy == operation.Inplace {
				options.TargetDataDir = ""
			}
			p, err := NewPlan(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			sourcePath, err := snapshot.SQLitePath(p.Source.DatabaseDSN)
			if err != nil {
				t.Fatal(err)
			}
			sourceBefore, err := operation.Hash(t.Context(), sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			originalFiles := sqliteSourceFilesEvidence(t, options)
			killSQLiteOperationAt(t, options.Directory, "apply", test.phase)
			assertSQLiteEvidenceUnchanged(t, originalFiles)
			loaded, journal, err := operation.Load(options.Directory)
			if err != nil {
				t.Fatal("read interrupted operation", err)
			}
			if err := operation.LoadBaseline(options.Directory, loaded, journal); err != nil {
				t.Fatal("original baseline after process death", err)
			}
			assertSQLiteBoundaryReceipts(t, options, loaded, journal, test.phase)
			protected := sqliteProtectedEvidence(t, options, loaded, journal)
			baselineBefore, err := operation.Hash(t.Context(), filepath.Join(options.Directory, "baseline.json"))
			if err != nil || journal.BaselineDigest != operation.BaselineDigest(loaded) {
				t.Fatalf("baseline binding: journal=%+v err=%v", journal, err)
			}
			protectionPath := filepath.Join(journal.BackupDirectory, "protection.json")
			protectionBefore, err := operation.Hash(t.Context(), protectionPath)
			if err != nil {
				t.Fatal("original protection after process death", err)
			}
			var protection snapshot.ProtectionReceipt
			if err := operation.Read(protectionPath, &protection); err != nil || protection.BaselineDigest != operation.BaselineDigest(loaded) || protection.PlanDigest != operation.Digest(loaded) || protection.SourceIdentity != p.Source.Identity {
				t.Fatalf("protection lost original baseline/plan/source binding: receipt=%+v err=%v", protection, err)
			}
			// Losing all filesystem progress must not cause a second conversion.
			if test.strategy == operation.Inplace {
				if err := os.Remove(filepath.Join(options.Directory, "journal.json")); err != nil {
					t.Fatal(err)
				}
			}
			report, err := Run(t.Context(), RunOptions{Directory: options.Directory, Offline: true})
			if err != nil || report == nil || !report.Verified || report.Phase != "completed" || report.Strategy != test.strategy {
				t.Fatalf("resume killed process: report=%+v err=%v", report, err)
			}
			baselineAfter, err := operation.Hash(t.Context(), filepath.Join(options.Directory, "baseline.json"))
			if err != nil || baselineBefore != baselineAfter || report.BaselineDigest != operation.BaselineDigest(loaded) {
				t.Fatalf("resume recaptured or lost original baseline: before=%+v after=%+v report=%+v err=%v", baselineBefore, baselineAfter, report, err)
			}
			protectionAfter, err := operation.Hash(t.Context(), protectionPath)
			if err != nil || protectionBefore != protectionAfter {
				t.Fatal("resume replaced original protection after process death/journal loss", err)
			}
			if _, err := VerifyOperation(t.Context(), options.Directory); err != nil {
				t.Fatal("verify after abrupt process termination", err)
			}
			assertSQLiteEvidenceUnchanged(t, protected)
			assertSQLiteEvidenceUnchanged(t, originalFiles)
			if test.strategy == operation.Clone {
				sourceAfter, err := operation.Hash(t.Context(), sourcePath)
				if err != nil || sourceAfter != sourceBefore {
					t.Fatal("clone recovery changed original source bytes", err)
				}
			}
		})
	}
}

func TestSQLiteKilledRestoreResumesOriginalDatabase(t *testing.T) {
	for _, phase := range []string{"restore_prepared", "before_restore_displace", "restore_displaced", "restore_published", "restore_verified"} {
		t.Run(phase, func(t *testing.T) {
			options, sourceDir, p, originals := newSQLiteRollbackFixture(t)
			originalFiles := sqliteSourceFilesEvidence(t, options)
			if _, err := Run(t.Context(), RunOptions{Directory: options.Directory, Offline: true}); err != nil {
				t.Fatal(err)
			}
			loaded, before, err := operation.Load(options.Directory)
			if err != nil {
				t.Fatal(err)
			}
			assertSQLiteBoundaryReceipts(t, options, loaded, before, "ready_commit")
			protected := sqliteProtectedEvidence(t, options, loaded, before)
			killSQLiteOperationAt(t, options.Directory, "rollback", phase)
			_, journal, err := operation.Load(options.Directory)
			if err != nil || journal.Restore == nil {
				t.Fatalf("killed restore lost durable intent: journal=%+v err=%v", journal, err)
			}
			failed := filepath.Join(journal.Restore.Directory, journal.Restore.FailedDatabase.Name)
			failedBefore, err := operation.Hash(t.Context(), failed)
			if err != nil || failedBefore != journal.Restore.FailedDatabase {
				t.Fatalf("killed restore lost failed database: artifact=%+v err=%v", failedBefore, err)
			}
			report, err := RollbackOperationWithOptions(t.Context(), options.Directory, RunOptions{Directory: options.Directory, Offline: true})
			if err != nil || report == nil || report.Phase != "rolled_back" || report.Restoration != "verified" {
				t.Fatalf("resume killed restore: report=%+v err=%v", report, err)
			}
			assertOriginalSQLiteRestored(t, t.Context(), options, sourceDir, p, originals, journal.Restore.ReplacementIdentity)
			assertSQLiteEvidenceUnchanged(t, protected)
			assertSQLiteEvidenceUnchanged(t, originalFiles)
			assertSQLiteEvidenceUnchanged(t, map[string]operation.Artifact{failed: failedBefore})
		})
	}
}

func TestSQLiteCancelledRestoreResumesOriginalDatabase(t *testing.T) {
	for _, phase := range []string{"restore_prepared", "before_restore_displace", "restore_displaced", "restore_published", "restore_verified"} {
		t.Run(phase, func(t *testing.T) {
			options, sourceDir, p, originals := newSQLiteRollbackFixture(t)
			originalFiles := sqliteSourceFilesEvidence(t, options)
			if _, err := Run(t.Context(), RunOptions{Directory: options.Directory, Offline: true}); err != nil {
				t.Fatal(err)
			}
			loaded, before, err := operation.Load(options.Directory)
			if err != nil {
				t.Fatal(err)
			}
			assertSQLiteBoundaryReceipts(t, options, loaded, before, "ready_commit")
			protected := sqliteProtectedEvidence(t, options, loaded, before)
			cancelSQLiteOperationAt(t, options.Directory, "rollback", phase, 1)
			_, journal, err := operation.Load(options.Directory)
			if err != nil || journal.Restore == nil {
				t.Fatalf("cancelled restore lost durable intent: journal=%+v err=%v", journal, err)
			}
			failed := filepath.Join(journal.Restore.Directory, journal.Restore.FailedDatabase.Name)
			failedBefore, err := operation.Hash(t.Context(), failed)
			if err != nil || failedBefore != journal.Restore.FailedDatabase {
				t.Fatalf("cancelled restore lost failed database: artifact=%+v err=%v", failedBefore, err)
			}
			report, err := RollbackOperationWithOptions(t.Context(), options.Directory, RunOptions{Directory: options.Directory, Offline: true})
			if err != nil || report == nil || report.Phase != "rolled_back" || report.Restoration != "verified" {
				t.Fatalf("resume cancelled restore: report=%+v err=%v", report, err)
			}
			assertOriginalSQLiteRestored(t, t.Context(), options, sourceDir, p, originals, journal.Restore.ReplacementIdentity)
			assertSQLiteEvidenceUnchanged(t, protected)
			assertSQLiteEvidenceUnchanged(t, originalFiles)
			assertSQLiteEvidenceUnchanged(t, map[string]operation.Artifact{failed: failedBefore})
		})
	}
}

func TestSQLiteBaselineInterruptionCannotInventOriginalProtection(t *testing.T) {
	for _, mode := range []string{"kill", "cancel"} {
		for _, changed := range []bool{false, true} {
			t.Run(mode+"/source_changed="+strconv.FormatBool(changed), func(t *testing.T) {
				options, _, p, _ := newSQLiteRollbackFixture(t)
				originalFiles := sqliteSourceFilesEvidence(t, options)
				if mode == "kill" {
					killSQLiteOperationAt(t, options.Directory, "apply", "baseline")
				} else {
					cancelSQLiteOperationAt(t, options.Directory, "apply", "baseline", 1)
				}
				loaded, journal, err := operation.Load(options.Directory)
				if err != nil {
					t.Fatal(err)
				}
				assertSQLiteBoundaryReceipts(t, options, loaded, journal, "baseline")
				baseline, err := operation.Hash(t.Context(), filepath.Join(options.Directory, "baseline.json"))
				if err != nil {
					t.Fatal(err)
				}
				assertIncomplete := func() {
					t.Helper()
					for _, path := range []string{filepath.Join(options.Directory, "protection-binding.json"), filepath.Join(options.Directory, "backup", "receipt.json")} {
						if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("pre-backup interruption invented completed original protection: %s err=%v", path, err)
						}
					}
				}
				assertIncomplete()
				for _, action := range []string{"verify", "rollback"} {
					var err error
					if action == "verify" {
						_, err = VerifyOperation(t.Context(), options.Directory)
					} else {
						_, err = RollbackOperationWithOptions(t.Context(), options.Directory, RunOptions{Directory: options.Directory, Offline: true})
					}
					var classified *operation.Error
					if !errors.As(err, &classified) || classified.Code != operation.BackupIncomplete {
						t.Fatalf("%s without original protection=%v, want backup_incomplete", action, err)
					}
				}
				assertIncomplete()
				resolved, err := configSource(options)
				if err != nil {
					t.Fatal(err)
				}
				if changed {
					db, _, err := snapshot.Open(t.Context(), &resolved.Config, false)
					if err != nil {
						t.Fatal(err)
					}
					_, writeErr := db.ExecContext(t.Context(), "UPDATE users SET username='changed-after-baseline' WHERE id=1")
					if err := errors.Join(writeErr, db.Close()); err != nil {
						t.Fatal(err)
					}
				}
				report, err := Run(t.Context(), RunOptions{Directory: options.Directory, Offline: true})
				if changed {
					var classified *operation.Error
					if !errors.As(err, &classified) || classified.Code != operation.BaselineChanged {
						t.Fatalf("resume changed pre-backup source=%v, want baseline_changed", err)
					}
					assertIncomplete()
					assertSQLiteBoundaryReceipts(t, options, loaded, journal, "baseline")
					db, _, err := snapshot.Open(t.Context(), &resolved.Config, true)
					if err != nil {
						t.Fatal(err)
					}
					var username string
					readErr := db.QueryRowContext(t.Context(), "SELECT username FROM users WHERE id=1").Scan(&username)
					if err := errors.Join(readErr, db.Close()); err != nil || username != "changed-after-baseline" {
						t.Fatalf("refused resume changed live business row: username=%q err=%v", username, err)
					}
				} else {
					if err != nil || report == nil || !report.Verified || report.Phase != "completed" {
						t.Fatalf("resume unchanged genuine source for first backup: report=%+v err=%v", report, err)
					}
					if _, err := VerifyOperation(t.Context(), options.Directory); err != nil {
						t.Fatal(err)
					}
					_, journal, err = operation.Load(options.Directory)
					if err != nil {
						t.Fatal(err)
					}
					sqliteProtectedEvidence(t, options, loaded, journal)
				}
				assertSQLiteEvidenceUnchanged(t, originalFiles)
				assertSQLiteEvidenceUnchanged(t, map[string]operation.Artifact{filepath.Join(options.Directory, "baseline.json"): baseline})
				if operation.Digest(p) != operation.Digest(loaded) {
					t.Fatal("interruption changed immutable original plan")
				}
			})
		}
	}
}

func TestSQLiteResourceCommitProcessInterruptionResumesPartialFormat(t *testing.T) {
	for _, mode := range []string{"kill", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			options, sourceDir, p, originals := newSQLiteRollbackFixture(t)
			originalFiles := sqliteSourceFilesEvidence(t, options)
			manifest, err := storagemigrate.ReadManifest(p.StorageManifest)
			if err != nil {
				t.Fatal("read genuine resource subplan", err)
			}
			var pending []storagemigrate.Entry
			for _, entry := range manifest.Entries {
				if !entry.Rejected {
					pending = append(pending, entry)
				}
			}
			if len(pending) < 2 {
				t.Fatal("fixture requires both a committed resource and an unconverted resource")
			}
			manifestBytes, err := operation.Hash(t.Context(), p.StorageManifest)
			if err != nil {
				t.Fatal(err)
			}
			// Occurrence one only prepares import ownership. Two follows the
			// first actual resource transaction, before its file checkpoint.
			if mode == "kill" {
				killSQLiteOperationAtOccurrence(t, options.Directory, "apply", "resource_commit", 2)
			} else {
				cancelSQLiteOperationAt(t, options.Directory, "apply", "resource_commit", 2)
			}
			loaded, journal, err := operation.Load(options.Directory)
			if err != nil {
				t.Fatal(err)
			}
			assertSQLiteBoundaryReceipts(t, options, loaded, journal, "resource_commit")
			protected := sqliteProtectedEvidence(t, options, loaded, journal)
			resolved, err := configSource(options)
			if err != nil {
				t.Fatal(err)
			}
			db, _, err := snapshot.Open(t.Context(), &resolved.Config, true)
			if err != nil {
				t.Fatal(err)
			}
			for i, entry := range pending {
				var revision sql.NullInt64
				var generation int64
				err := db.QueryRowContext(t.Context(), "SELECT current_source_revision_id,source_generation FROM resources WHERE id=?", entry.ResourceID).Scan(&revision, &generation)
				wantGeneration := entry.SourceGeneration
				if i == 0 {
					wantGeneration++
				}
				if err != nil || revision.Valid != (i == 0) || generation != wantGeneration {
					db.Close()
					t.Fatalf("partial resource format at commit: resource=%d revision=%+v generation=%d want=%d err=%v", entry.ResourceID, revision, generation, wantGeneration, err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			checkpoint, err := storagemigrate.ReadManifest(filepath.Join(options.Directory, "storage-journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range checkpoint.Entries {
				if entry.ResourceID == pending[0].ResourceID && entry.Applied {
					t.Fatal("child saved the resource checkpoint before requested interruption")
				}
			}
			report, err := Run(t.Context(), RunOptions{Directory: options.Directory, Offline: true})
			if err != nil || report == nil || !report.Verified || report.Phase != "completed" {
				t.Fatalf("resume partially imported resources: report=%+v err=%v", report, err)
			}
			if _, err := VerifyOperation(t.Context(), options.Directory); err != nil {
				t.Fatal("verify resource recovery", err)
			}
			assertSQLiteEvidenceUnchanged(t, protected)
			assertSQLiteEvidenceUnchanged(t, originalFiles)
			assertSQLiteEvidenceUnchanged(t, map[string]operation.Artifact{p.StorageManifest: manifestBytes})
			if _, err := RollbackOperationWithOptions(t.Context(), options.Directory, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
				t.Fatal("restore original after partial resource recovery", err)
			}
			_, restored, err := operation.Load(options.Directory)
			if err != nil || restored.Restore == nil {
				t.Fatalf("resource recovery restoration intent: journal=%+v err=%v", restored, err)
			}
			assertOriginalSQLiteRestored(t, t.Context(), options, sourceDir, p, originals, restored.Restore.ReplacementIdentity)
		})
	}
}

func TestSQLiteKilledUpgradeWithLostJournalRequiresOriginalProof(t *testing.T) {
	for _, missing := range []string{"baseline", "protection"} {
		t.Run(missing, func(t *testing.T) {
			options, _ := fixtureDeployment(t, "serve")
			options.TargetDataDir = ""
			if _, err := NewPlan(t.Context(), options); err != nil {
				t.Fatal(err)
			}
			killSQLiteOperationAt(t, options.Directory, "apply", "accounts_commit")
			_, journal, err := operation.Load(options.Directory)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := configSource(options)
			if err != nil {
				t.Fatal(err)
			}
			fingerprint := func() string {
				db, _, err := snapshot.Open(t.Context(), &resolved.Config, true)
				if err != nil {
					t.Fatal(err)
				}
				value, fingerprintErr := BusinessFingerprint(t.Context(), db, "sqlite")
				if err := errors.Join(fingerprintErr, db.Close()); err != nil {
					t.Fatal(err)
				}
				return value
			}
			before := fingerprint()
			proofPath := filepath.Join(options.Directory, "baseline.json")
			if missing == "protection" {
				proofPath = filepath.Join(journal.BackupDirectory, "receipt.json")
			}
			for _, path := range []string{filepath.Join(options.Directory, "journal.json"), proofPath} {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			_, err = Run(t.Context(), RunOptions{Directory: options.Directory, Offline: true})
			var classified *operation.Error
			if !errors.As(err, &classified) || (classified.Code != operation.BackupIncomplete && classified.Code != operation.Incompatible) {
				t.Fatalf("resume without original %s evidence = %v, want refusal", missing, err)
			}
			if after := fingerprint(); after != before {
				t.Fatal("refused resume changed committed business data or schema")
			}
			if _, err := os.Stat(proofPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("resume fabricated replacement original %s evidence: %v", missing, err)
			}
		})
	}
}

func TestSQLiteCancelledProcessReleasesLocksAndResumes(t *testing.T) {
	for _, phase := range []string{"backup", "preparing", "schema_commit", "accounts_commit", "history_commit", "storage_commit", "ready_commit", "before_report"} {
		t.Run(phase, func(t *testing.T) {
			options, _ := fixtureDeployment(t, "serve")
			options.TargetDataDir = ""
			if _, err := NewPlan(t.Context(), options); err != nil {
				t.Fatal(err)
			}
			originalFiles := sqliteSourceFilesEvidence(t, options)
			cancelSQLiteOperationAt(t, options.Directory, "apply", phase, 1)
			assertSQLiteEvidenceUnchanged(t, originalFiles)
			p, journal, err := operation.Load(options.Directory)
			if err != nil {
				t.Fatal(err)
			}
			assertSQLiteBoundaryReceipts(t, options, p, journal, phase)
			protected := sqliteProtectedEvidence(t, options, p, journal)
			report, err := Run(t.Context(), RunOptions{Directory: options.Directory, Offline: true})
			if err != nil || report == nil || !report.Verified || report.Phase != "completed" {
				t.Fatalf("resume after context cancellation and lock release: report=%+v err=%v", report, err)
			}
			if _, err := VerifyOperation(t.Context(), options.Directory); err != nil {
				t.Fatal("verify resumed cancelled operation", err)
			}
			assertSQLiteEvidenceUnchanged(t, protected)
			assertSQLiteEvidenceUnchanged(t, originalFiles)
		})
	}
}

func killSQLiteOperationAt(t *testing.T, directory, action, phase string) {
	t.Helper()
	killSQLiteOperationAtOccurrence(t, directory, action, phase, 1)
}

func cancelSQLiteOperationAt(t *testing.T, directory, action, phase string, occurrence int) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	mode := "cancel"
	if action == "rollback" {
		mode = "cancel_rollback"
	}
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSQLiteOperationProcess$")
	cmd.Env = append(os.Environ(), "LINGUAFLOW_SQLITE_OPERATION_DIR="+directory, "LINGUAFLOW_SQLITE_OPERATION_ACTION="+mode, "LINGUAFLOW_SQLITE_OPERATION_BOUNDARY="+phase, "LINGUAFLOW_SQLITE_OPERATION_OCCURRENCE="+strconv.Itoa(occurrence))
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "context cancellation observed") {
		t.Fatalf("actual process cancellation at %s occurrence %d: %v %s", phase, occurrence, err, output)
	}
}

func assertSQLiteBoundaryReceipts(t *testing.T, options PlanOptions, p *operation.Plan, journal *operation.Journal, phase string) {
	t.Helper()
	if err := operation.LoadBaseline(options.Directory, p, journal); err != nil {
		t.Fatal("load original baseline for database receipts", err)
	}
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	cfg := resolved.Config
	if p.Strategy == operation.Clone {
		cfg.DataDir = journal.Staging
		if _, err := os.Stat(p.Target.DataDir); err == nil {
			cfg.DataDir = p.Target.DataDir
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		cfg.Database.DSN = filepath.Join(cfg.DataDir, "linguaflow.db")
	}
	db, _, err := snapshot.Open(t.Context(), &cfg, true)
	if err != nil {
		t.Fatal("open interrupted database for receipts", err)
	}
	defer db.Close()
	if phase == "baseline" || phase == "backup" {
		var reservations int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_schema WHERE name='linguaflow_migration_state'").Scan(&reservations); err != nil || reservations != 0 {
			t.Fatalf("pre-reservation boundary has database ownership: count=%d err=%v", reservations, err)
		}
		return
	}
	want := map[string]string{
		"preparing":       "preparing",
		"schema_commit":   "preparing,schema",
		"accounts_commit": "preparing,schema,accounts",
		"history_commit":  "preparing,schema,accounts,history",
		"resource_commit": "preparing,schema,accounts,history",
		"storage_commit":  "preparing,schema,accounts,history,storage",
		"ready_commit":    "preparing,schema,accounts,history,storage,ready",
		"before_report":   "preparing,schema,accounts,history,storage,ready",
		"before_publish":  "preparing,schema,accounts,history,storage,ready",
		"publish":         "preparing,schema,accounts,history,storage,ready",
	}[phase]
	if want == "" {
		t.Fatalf("no receipt expectation for boundary %q", phase)
	}
	steps, state, err := operation.Steps(t.Context(), db, p)
	if err != nil || strings.Join(steps, ",") != want {
		t.Fatalf("database receipts at %s: steps=%v state=%q want=%q err=%v", phase, steps, state, want, err)
	}
	wantState := "preparing"
	if strings.HasSuffix(want, ",ready") {
		wantState = "ready"
		result, err := operation.ResultDigest(t.Context(), db, p)
		actual, fingerprintErr := BusinessFingerprint(t.Context(), db, "sqlite")
		if err != nil || fingerprintErr != nil || result != actual {
			t.Fatalf("ready receipt does not bind committed business result: result=%q actual=%q err=%v", result, actual, errors.Join(err, fingerprintErr))
		}
	}
	if state != wantState {
		t.Fatalf("database state at %s=%q, want %q", phase, state, wantState)
	}
}

func sqliteSourceFilesEvidence(t *testing.T, options PlanOptions) map[string]operation.Artifact {
	t.Helper()
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	evidence := map[string]operation.Artifact{}
	for _, path := range []string{resolved.ConfigPath, resolved.Config.Credentials.KeyringFile} {
		if path == "" {
			continue
		}
		proof, err := operation.Hash(t.Context(), path)
		if err != nil {
			t.Fatal("hash original deployment/keyring before interruption", path, err)
		}
		evidence[path] = proof
	}
	return evidence
}

func sqliteProtectedEvidence(t *testing.T, options PlanOptions, p *operation.Plan, journal *operation.Journal) map[string]operation.Artifact {
	t.Helper()
	backup, err := loadMaterials(t.Context(), options.Directory, p, journal)
	if err != nil {
		t.Fatal("verify original protection and binding", err)
	}
	if err := VerifyOriginalReferences(t.Context(), p, backup); err != nil {
		t.Fatal("original referenced objects after interruption", err)
	}
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(options.Directory, "baseline.json"),
		filepath.Join(options.Directory, "protection-binding.json"),
		filepath.Join(journal.BackupDirectory, "receipt.json"),
		filepath.Join(journal.BackupDirectory, "protection.json"),
		p.Source.ConfigPath,
		resolved.Config.Credentials.KeyringFile,
	}
	for _, artifact := range []operation.Artifact{backup.Database, backup.Deployment, backup.Keyring} {
		if artifact.Name != "" {
			paths = append(paths, filepath.Join(journal.BackupDirectory, artifact.Name))
		}
	}
	for _, object := range backup.Objects {
		if object.Driver == "local" && object.ObservedStatus == "readable" {
			paths = append(paths, filepath.Join(object.Root, filepath.FromSlash(object.Key)))
		}
	}
	evidence := map[string]operation.Artifact{}
	for _, path := range paths {
		if path == "" {
			continue
		}
		proof, err := operation.Hash(t.Context(), path)
		if err != nil {
			t.Fatal("hash original protection or source bytes", path, err)
		}
		evidence[path] = proof
	}
	return evidence
}

func assertSQLiteEvidenceUnchanged(t *testing.T, evidence map[string]operation.Artifact) {
	t.Helper()
	for path, expected := range evidence {
		actual, err := operation.Hash(t.Context(), path)
		if err != nil || actual != expected {
			t.Fatalf("original protection or retained bytes changed: %s got=%+v want=%+v err=%v", path, actual, expected, err)
		}
	}
}

func killSQLiteOperationAtOccurrence(t *testing.T, directory, action, phase string, occurrence int) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestSQLiteOperationProcess$")
	cmd.Env = append(os.Environ(), "LINGUAFLOW_SQLITE_OPERATION_DIR="+directory, "LINGUAFLOW_SQLITE_OPERATION_ACTION="+action, "LINGUAFLOW_SQLITE_OPERATION_BOUNDARY="+phase, "LINGUAFLOW_SQLITE_OPERATION_OCCURRENCE="+strconv.Itoa(occurrence))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && strings.TrimSpace(line) != "durable boundary: "+phase {
			err = fmt.Errorf("unexpected operation readiness line %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal("process did not reach durable boundary", err)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("process did not reach durable boundary before deadline")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal("kill operation at durable boundary", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("operation exited normally instead of being killed")
	}
}

func TestSQLiteOperationProcess(t *testing.T) {
	directory := os.Getenv("LINGUAFLOW_SQLITE_OPERATION_DIR")
	if directory == "" {
		return
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	action := os.Getenv("LINGUAFLOW_SQLITE_OPERATION_ACTION")
	phase := os.Getenv("LINGUAFLOW_SQLITE_OPERATION_BOUNDARY")
	occurrence := 1
	if value := os.Getenv("LINGUAFLOW_SQLITE_OPERATION_OCCURRENCE"); value != "" {
		var err error
		occurrence, err = strconv.Atoi(value)
		if err != nil || occurrence < 1 {
			t.Fatalf("invalid requested boundary occurrence %q", value)
		}
	}
	observed := 0
	fired := false
	options := RunOptions{Directory: directory, Offline: true, Fault: func(at string) error {
		if at != phase {
			return nil
		}
		observed++
		if observed != occurrence {
			return nil
		}
		fired = true
		if action == "cancel" || action == "cancel_rollback" {
			cancel()
			return nil
		}
		if _, err := fmt.Fprintln(os.Stdout, "durable boundary: "+phase); err != nil {
			return err
		}
		// Keep a timer alive after Run closes its database connections. An
		// empty select can trigger the runtime's own deadlock crash instead
		// of leaving a live process for the parent's deliberate kill.
		for {
			time.Sleep(time.Hour)
		}
	}}
	var err error
	if action == "rollback" || action == "cancel_rollback" {
		_, err = RollbackOperationWithOptions(ctx, directory, options)
	} else {
		_, err = Run(ctx, options)
	}
	if (action != "cancel" && action != "cancel_rollback") || !fired || !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stdout, "operation returned before interruption: fired=%t err=%v\n", fired, err)
		t.Fatalf("operation returned before parent interruption: fired=%t err=%v", fired, err)
	}
	fmt.Fprintln(os.Stdout, "context cancellation observed")
}
