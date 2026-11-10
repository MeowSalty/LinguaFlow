package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

const originalV016SHA256 = "efe9a620bfc2a1fea3bb3892c4b82d58f38215fe44376f25527963b12c663fe7"

// Only the preserved prechange executable may author or resume this operation.
// Retain its real interrupted documents even if a subsequent assertion fails.
func TestExactLegacyV1BinaryInterruptedOperationAcceptance(t *testing.T) {
	binary, err := filepath.Abs(filepath.Join("..", "..", "..", "bin", "legacy-v016", "linguaflow-migrate.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(binary); errors.Is(err, os.ErrNotExist) {
		t.Skip("preserved original v0.16 binary explicitly missing in external clean CI")
	} else if err != nil {
		t.Fatal(err)
	}
	proof, err := operation.Hash(t.Context(), binary)
	if err != nil || proof.SHA256 != originalV016SHA256 {
		t.Fatalf("original executable identity mismatch: artifact=%+v err=%v", proof, err)
	}
	root, err := os.MkdirTemp(filepath.Dir(binary), "v1-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained exact-binary fixture: %s", root)
	configPath, sourceDir := retainedLegacyReleaseFixture(t, root)
	directory, target := filepath.Join(root, "operation"), filepath.Join(root, "target")
	runOriginalLegacy(t, binary, "upgrade", "plan", "--config", configPath, "--operation-dir", directory, "--target-data-dir", target)
	planBytes, err := os.ReadFile(filepath.Join(directory, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Version int    `json:"version"`
		ID      string `json:"operation_id"`
		Build   string `json:"build_commit"`
	}
	if err := json.Unmarshal(planBytes, &plan); err != nil || plan.Version != 1 || !strings.HasSuffix(plan.Build, ":"+originalV016SHA256) {
		t.Fatalf("fixture was not authored by exact original v1 binary: plan=%+v err=%v", plan, err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, planBytes); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(compact.Bytes())
	planDigest := hex.EncodeToString(digest[:])
	interruptOriginalLegacyAtPreparing(t, binary, directory)
	// Save the actual interrupted state separately before the original resumes.
	interruptedDir := filepath.Join(root, "interrupted-documents")
	if err := credential.CreatePrivateDirectory(interruptedDir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plan.json", "journal.json"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := credential.PublishPrivateFile(filepath.Join(interruptedDir, name), data); err != nil {
			t.Fatal(err)
		}
	}
	before := legacyAcceptanceTree(t, root)
	statusBytes := []byte(execute(t, "status", "--operation-dir", directory))
	var status struct {
		Version int             `json:"version"`
		Build   string          `json:"expected_build"`
		Plan    json.RawMessage `json:"plan"`
		Journal struct {
			Digest string `json:"plan_digest"`
			Phase  string `json:"phase"`
		} `json:"journal"`
	}
	if err := json.Unmarshal(statusBytes, &status); err != nil || status.Version != 1 || status.Build != plan.Build || status.Journal.Digest != planDigest || status.Journal.Phase == "completed" {
		t.Fatalf("new read-only status cannot bind genuine interrupted v1: status=%+v err=%v", status, err)
	}
	compact.Reset()
	if err := json.Compact(&compact, status.Plan); err != nil {
		t.Fatal(err)
	}
	statusDigest := sha256.Sum256(compact.Bytes())
	if hex.EncodeToString(statusDigest[:]) != planDigest {
		t.Fatal("frozen v1 DTO changed original field ordering or digest")
	}
	for _, args := range [][]string{
		{"upgrade", "apply", "--offline"},
		{"upgrade", "rehearse", "--offline"},
		{"resume", "--offline"},
		{"verify"},
		{"rollback", "--offline"},
		{"storage", "apply", "--offline"},
	} {
		var output bytes.Buffer
		cmd := NewCommand()
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		cmd.SetArgs(append(args, "--operation-dir", directory))
		err := cmd.ExecuteContext(t.Context())
		if err == nil || !strings.Contains(err.Error(), plan.Build) {
			t.Fatalf("new action %v did not refuse and name original recorded build: %v %s", args, err, output.String())
		}
		if after := legacyAcceptanceTree(t, root); !reflect.DeepEqual(before, after) {
			t.Fatalf("new action %v changed retained v1 operation/source/target bytes or file identity", args)
		}
	}
	runOriginalLegacy(t, binary, "resume", "--operation-dir", directory, "--offline")
	runOriginalLegacy(t, binary, "verify", "--operation-dir", directory)
	completedBefore := legacyAcceptanceTree(t, root)
	completedStatus := execute(t, "status", "--operation-dir", directory)
	if err := json.Unmarshal([]byte(completedStatus), &status); err != nil || status.Journal.Phase != "completed" || status.Journal.Digest != planDigest || status.Build != plan.Build {
		t.Fatalf("new reader rejected genuine original completion report/journal: status=%+v err=%v", status, err)
	}
	if after := legacyAcceptanceTree(t, root); !reflect.DeepEqual(completedBefore, after) {
		t.Fatal("new completed-v1 status changed operation/source/target evidence")
	}
	var report struct {
		Phase    string `json:"phase"`
		Verified bool   `json:"verified"`
	}
	data, err := os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil || json.Unmarshal(data, &report) != nil || report.Phase != "completed" || !report.Verified {
		t.Fatalf("original completion report: %+v err=%v", report, err)
	}
	afterPlan, err := os.ReadFile(filepath.Join(directory, "plan.json"))
	if err != nil || !bytes.Equal(planBytes, afterPlan) {
		t.Fatal("original resume changed immutable v1 plan bytes", err)
	}
	for path, original := range before {
		if !strings.HasPrefix(path, sourceDir+string(os.PathSeparator)) {
			continue
		}
		if actual := legacyAcceptanceFile(t, path); actual != original {
			t.Fatalf("original clone changed released source %s", path)
		}
	}
	deployment := filepath.Join(target, "deployment.yaml")
	resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: "serve", ConfigPath: &deployment, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	db, client, err := database.Open(t.Context(), &resolved.Config)
	if err != nil {
		t.Fatal("normal startup after original binary resumed", err)
	}
	defer db.Close()
	auth := service.NewAuthService(client, service.AuthConfigFromServer(resolved.Config), service.NewSettingsService(client))
	session, err := auth.Login(t.Context(), service.LoginInput{Identifier: "OriginalAdmin", Password: "original-password-123"})
	if err != nil || session.User.ID != 1 || session.User.Role != "admin" {
		t.Fatal("original binary did not preserve administrator credentials/ID", err)
	}
	// Keep a second operation genuinely interrupted, including its original
	// staging database/WAL and path identities. The first proves resume; this
	// one remains available for later independent inspection or original resume.
	retainedDirectory := filepath.Join(root, "interrupted-operation")
	runOriginalLegacy(t, binary, "upgrade", "plan", "--config", configPath, "--operation-dir", retainedDirectory, "--target-data-dir", filepath.Join(root, "interrupted-target"))
	interruptOriginalLegacyAtPreparing(t, binary, retainedDirectory)
	retainedBefore := legacyAcceptanceTree(t, root)
	retainedStatus := execute(t, "status", "--operation-dir", retainedDirectory)
	if err := json.Unmarshal([]byte(retainedStatus), &status); err != nil || status.Version != 1 || status.Journal.Phase == "completed" || status.Build != plan.Build {
		t.Fatalf("preserved actual interrupted-v1 status: status=%+v err=%v", status, err)
	}
	if after := legacyAcceptanceTree(t, root); !reflect.DeepEqual(retainedBefore, after) {
		t.Fatal("status changed the retained resumable interrupted operation")
	}
	t.Logf("retained resumable interrupted v1 operation: %s (plan digest %s)", retainedDirectory, status.Journal.Digest)
	finalProof, err := operation.Hash(t.Context(), binary)
	if err != nil || finalProof != proof {
		t.Fatal("preserved original executable changed during acceptance", err)
	}
}

func retainedLegacyReleaseFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	configPath, temporarySource := cliReleaseFixture(t)
	resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: "serve", ConfigPath: &configPath, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "source")
	if err := credential.CreatePrivateDirectory(sourceDir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"linguaflow.db", "keyring.json"} {
		data, err := os.ReadFile(filepath.Join(temporarySource, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := credential.PublishPrivateFile(filepath.Join(sourceDir, name), data); err != nil {
			t.Fatal(err)
		}
	}
	resolved.Config.DataDir = sourceDir
	resolved.Config.Database.DSN = filepath.Join(sourceDir, "linguaflow.db")
	resolved.Config.Credentials.KeyringFile = filepath.Join(sourceDir, "keyring.json")
	data, err := config.EncodeServerDocument(resolved.Config, config.LogConfig{Level: "info", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(sourceDir, "source.yml")
	if _, err := credential.PublishPrivateFile(configPath, data); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", resolved.Config.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	// Enough real account work for the external observer to reliably see the
	// reservation. No fault hook, modified schema, or fake journal is used.
	_, insertErr := db.Exec(`WITH RECURSIVE ids(id) AS (SELECT 2 UNION ALL SELECT id+1 FROM ids WHERE id<3001)
		INSERT INTO users(id,created_at,updated_at,username,password_hash,email,role,active)
		SELECT ids.id,u.created_at,u.updated_at,'legacy-user-'||ids.id,u.password_hash,'legacy-'||ids.id||'@example.invalid','user',true FROM ids CROSS JOIN users u WHERE u.id=1`)
	if err := errors.Join(insertErr, db.Close()); err != nil {
		t.Fatal("create released account workload", err)
	}
	return configPath, sourceDir
}

func runOriginalLegacy(t *testing.T, binary string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("exact original binary %v: %v %s", args, err, output)
	}
	return output
}

func interruptOriginalLegacyAtPreparing(t *testing.T, binary, directory string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "upgrade", "apply", "--operation-dir", directory, "--offline")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var staging string
	for {
		select {
		case err := <-done:
			t.Fatalf("original process ended before observable preparing reservation: %v %s", err, output.String())
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-done
			t.Fatalf("original process interruption boundary unachievable: %s", output.String())
		case <-ticker.C:
			data, err := os.ReadFile(filepath.Join(directory, "journal.json"))
			var journal struct {
				Staging string `json:"staging"`
			}
			if err != nil || json.Unmarshal(data, &journal) != nil || journal.Staging == "" {
				continue
			}
			staging = filepath.Join(journal.Staging, "linguaflow.db")
			if legacyReservationState(staging) != "preparing" {
				continue
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("original process exited normally instead of abrupt interruption")
			}
			if state := legacyReservationState(staging); state != "preparing" {
				t.Fatalf("original process raced past interrupted preparing state: %q", state)
			}
			t.Logf("original executable killed with actual preparing reservation at %s", staging)
			return
		}
	}
}

func legacyReservationState(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return ""
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var state string
	if err := db.QueryRowContext(ctx, "SELECT state FROM linguaflow_migration_state WHERE id=1").Scan(&state); err != nil {
		return ""
	}
	return state
}

type legacyFileEvidence struct {
	Proof    operation.Artifact
	Identity string
	Modified time.Time
}

func legacyAcceptanceFile(t *testing.T, path string) legacyFileEvidence {
	t.Helper()
	proof, err := operation.Hash(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := operation.Identity(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return legacyFileEvidence{proof, identity, info.ModTime()}
}

func legacyAcceptanceTree(t *testing.T, root string) map[string]legacyFileEvidence {
	t.Helper()
	evidence := map[string]legacyFileEvidence{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			evidence[path] = legacyAcceptanceFile(t, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(fmt.Errorf("retained fixture evidence: %w", err))
	}
	return evidence
}
