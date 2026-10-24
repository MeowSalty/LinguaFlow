package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"golang.org/x/crypto/bcrypt"
)

// The dedicated Task builds the shipped executable and invokes each operation
// in a fresh process, including plan/build binding across successive commands.
func TestBuiltCLIReleaseAcceptance(t *testing.T) {
	binary := os.Getenv("MIGRATION_ACCEPTANCE_BINARY")
	if binary == "" {
		t.Skip("run task backend:migrate:test:binary to validate the built executable")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("built CLI %v: %v %s", args, err, output)
		}
		if strings.Contains(string(output), "cli-test-deployment-secret") {
			t.Fatal("CLI exposed deployment secret")
		}
		return output
	}
	for _, mode := range []string{"serve", "local"} {
		t.Run(mode, func(t *testing.T) {
			configPath, sourceDir := cliReleaseFixtureMode(t, mode)
			parent := t.TempDir()
			opdir, target := filepath.Join(parent, "operation"), filepath.Join(parent, "target")
			before, err := operation.Hash(t.Context(), filepath.Join(sourceDir, "linguaflow.db"))
			if err != nil {
				t.Fatal(err)
			}
			run(t, "list")
			run(t, "inspect", "--mode", mode, "--config", configPath)
			output := run(t, "upgrade", "plan", "--mode", mode, "--config", configPath, "--operation-dir", opdir, "--target-data-dir", target)
			var plan operation.Plan
			if err := json.Unmarshal(output, &plan); err != nil {
				t.Fatal(err)
			}
			proof, err := operation.Hash(t.Context(), binary)
			if err != nil || !strings.HasSuffix(plan.Build, ":"+proof.SHA256) {
				t.Fatal("plan does not bind the executing binary", err)
			}
			run(t, "upgrade", "rehearse", "--operation-dir", opdir, "--offline")
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("rehearsal occupied final publication")
			}
			run(t, "upgrade", "apply", "--operation-dir", opdir, "--offline")
			run(t, "status", "--operation-dir", opdir)
			run(t, "verify", "--operation-dir", opdir)
			run(t, "resume", "--operation-dir", opdir, "--offline")
			deployment := filepath.Join(target, "deployment.yaml")
			resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: mode, ConfigPath: &deployment, Environment: map[string]string{}})
			if err != nil {
				t.Fatal(err)
			}
			db, client, err := database.Open(t.Context(), &resolved.Config)
			if err != nil {
				t.Fatal("normal startup of published target", err)
			}
			if mode == "serve" {
				auth := service.NewAuthService(client, service.AuthConfigFromServer(resolved.Config), service.NewSettingsService(client))
				session, err := auth.Login(t.Context(), service.LoginInput{Identifier: plan.Accounts[0].Username, Password: "original-password-123"})
				if err != nil || session.User.ID != 1 || session.User.Role != "admin" {
					t.Fatal("built CLI lost original administrator", err)
				}
			} else {
				marker, err := client.InstanceInitialization.Get(t.Context(), 1)
				if err != nil || marker.LocalUserID == nil || *marker.LocalUserID != 1 {
					t.Fatal("built CLI changed local identity", err)
				}
			}
			db.Close()
			run(t, "rollback", "--operation-dir", opdir, "--offline")
			run(t, "rollback", "--operation-dir", opdir, "--offline")
			after, err := operation.Hash(t.Context(), filepath.Join(sourceDir, "linguaflow.db"))
			if err != nil || before != after {
				t.Fatal("built CLI wrote source", err)
			}
		})
	}
	for _, mode := range []string{"serve", "local"} {
		t.Run("inplace-"+mode, func(t *testing.T) { builtCLIInplaceAcceptance(t, binary, mode, run) })
	}
}

func builtCLIInplaceAcceptance(t *testing.T, binary, mode string, run func(*testing.T, ...string) []byte) {
	t.Helper()
	configPath, _ := cliReleaseFixtureMode(t, mode)
	opdir := filepath.Join(t.TempDir(), "operation")
	configProof, err := operation.Hash(t.Context(), configPath)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"upgrade", "plan", "--mode", mode, "--config", configPath, "--operation-dir", opdir}
	var plan operation.Plan
	if err := json.Unmarshal(run(t, args...), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Version != 2 || plan.Strategy != operation.Inplace || plan.DatabaseBackup != operation.BackupAuto || plan.Target.DataDir != "" {
		t.Fatal("built CLI did not select default inplace", plan.Strategy)
	}
	proof, err := operation.Hash(t.Context(), binary)
	if err != nil || !strings.HasSuffix(plan.Build, ":"+proof.SHA256) {
		t.Fatal("inplace plan omitted executing binary binding", err)
	}
	run(t, "upgrade", "rehearse", "--operation-dir", opdir, "--offline")
	var report operation.Report
	if err := json.Unmarshal(run(t, "upgrade", "apply", "--operation-dir", opdir, "--offline"), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Verified || report.Strategy != operation.Inplace || report.Protection != snapshot.Captured || report.BaselineDigest == "" || report.ResultDigest == "" || report.Rehearsal != "verified" {
		t.Fatal("built inplace CLI omitted durable evidence", report)
	}
	run(t, "status", "--operation-dir", opdir)
	run(t, "verify", "--operation-dir", opdir)
	run(t, "resume", "--operation-dir", opdir, "--offline")
	resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: mode, ConfigPath: &configPath, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	resolved.Config.AutoMigrate = false
	db, client, err := database.Open(t.Context(), &resolved.Config)
	if err != nil {
		t.Fatal("startup of original upgraded path", err)
	}
	user, err := client.User.Get(t.Context(), 1)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("original-password-123")) != nil {
		t.Fatal("original password hash was lost", err)
	}
	if mode == "local" {
		marker, err := client.InstanceInitialization.Get(t.Context(), 1)
		if err != nil || marker.LocalUserID == nil || *marker.LocalUserID != 1 {
			t.Fatal("original local identity changed", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(run(t, "rollback", "--operation-dir", opdir, "--offline"), &report); err != nil {
		t.Fatal(err)
	}
	if report.Phase != "rolled_back" || report.Restoration != "verified" || report.Recovery == nil || report.Recovery.RestoredIdentity == "" {
		t.Fatal("built rollback did not verify actual original restoration", report)
	}
	run(t, "rollback", "--operation-dir", opdir, "--offline")
	var original struct{ Format, Fingerprint string }
	if err := json.Unmarshal(run(t, "inspect", "--mode", mode, "--config", configPath), &original); err != nil {
		t.Fatal(err)
	}
	if original.Format != "v015" || original.Fingerprint != plan.SourceFingerprint {
		t.Fatal("built rollback did not restore original business baseline", original.Format)
	}
	actual, err := operation.Hash(t.Context(), configPath)
	if err != nil || actual != configProof {
		t.Fatal("built inplace flow changed original deployment file", err)
	}
}

func TestUnifiedStorageLocalPolicyAndRestore(t *testing.T) {
	configPath, resolved := currentFixtureMode(t, "local")
	opdir := filepath.Join(t.TempDir(), "operation")
	execute(t, "storage", "plan", "--mode", "local", "--config", configPath, "--operation-dir", opdir)
	execute(t, "storage", "apply", "--operation-dir", opdir, "--offline")
	execute(t, "verify", "--operation-dir", opdir)
	execute(t, "resume", "--operation-dir", opdir, "--offline")
	db, client, err := database.Open(t.Context(), &resolved.Config)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := client.InstanceInitialization.Get(t.Context(), 1)
	if err != nil || marker.LocalUserID == nil || *marker.LocalUserID != 1 {
		t.Fatal("storage import changed local identity", err)
	}
	policy, err := client.LocalAccountPolicy.Get(t.Context(), 1)
	if err != nil || policy.EmailLoginEnabled || !policy.UsernameLoginEnabled {
		t.Fatal("storage import changed local login policy", err)
	}
	db.Close()
	execute(t, "rollback", "--operation-dir", opdir, "--offline")
}
