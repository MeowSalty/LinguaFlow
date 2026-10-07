package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/credentialversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func masterKeyRuntimeInputs(t *testing.T, dir string) config.ServerInputs {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return config.ServerInputs{Mode: config.ModeServer, WorkingDirectory: dir, Environment: map[string]string{
		"LINGUAFLOW_DATA_DIR":                                   filepath.Join(dir, "data"),
		"LINGUAFLOW_HOST":                                       "127.0.0.1",
		"LINGUAFLOW_PORT":                                       strconv.Itoa(port),
		"LINGUAFLOW_JWT_SECRET":                                 strings.Repeat("runtime-jwt-", 3),
		"LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME":                   "admin",
		"LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL":                      "admin@test.invalid",
		"LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD":                   "runtime-admin-password",
		"LINGUAFLOW_STORAGE_INITIALIZATION_CAPACITY_BYTES":      "null",
		"LINGUAFLOW_STORAGE_INITIALIZATION_LOGICAL_LIMIT_BYTES": "null",
	}}
}

func masterKeyRuntimeResolve(t *testing.T, input config.ServerInputs) *config.ResolvedServer {
	t.Helper()
	resolved, err := config.ResolveServerConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func masterKeyRuntimeBoot(t *testing.T, resolved *config.ResolvedServer) func() {
	t.Helper()
	server, listener, cleanup, err := bootstrapServer(context.Background(), BootOptions{
		Resolved: resolved, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	var once sync.Once
	stop := func() {
		t.Helper()
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(ctx); err != nil {
				t.Errorf("shutdown: %v", err)
			}
			if err := listener.Close(); err != nil {
				t.Errorf("close listener: %v", err)
			}
			if err := cleanup(); err != nil {
				t.Errorf("close bootstrap database: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func masterKeyRuntimeClient(t *testing.T, cfg *config.ServerConfig) (*ent.Client, func()) {
	t.Helper()
	_, client, err := database.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	closeClient := func() {
		t.Helper()
		once.Do(func() {
			if err := client.Close(); err != nil {
				t.Errorf("close credential test database: %v", err)
			}
		})
	}
	t.Cleanup(closeClient)
	return client, closeClient
}

func masterKeyRuntimeCreate(t *testing.T, client *ent.Client, keys *credential.Keyring, secret string) *service.CredentialRecord {
	t.Helper()
	ctx := context.Background()
	admin, err := client.User.Query().Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.NewCredentialService(client, keys, nil).Create(ctx, admin.ID, service.CreateCredentialInput{
		Scope: service.ScopeUser, OwnerID: admin.ID, Provider: "openai", Secret: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestMasterKeyRuntimeRestart(t *testing.T) {
	for _, source := range []string{"environment", "file"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			input := masterKeyRuntimeInputs(t, dir)
			master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{27}, 32))
			if source == "environment" {
				input.Environment["LINGUAFLOW_CREDENTIALS_MASTER_KEY"] = master
			} else {
				path := filepath.Join(dir, "master-key")
				if _, err := credential.PublishPrivateFile(path, []byte(master+"\r\n")); err != nil {
					t.Fatal(err)
				}
				input.Environment["LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE"] = path
			}
			first := masterKeyRuntimeResolve(t, input)
			stop := masterKeyRuntimeBoot(t, first)
			client, closeClient := masterKeyRuntimeClient(t, &first.Config)
			created := masterKeyRuntimeCreate(t, client, first.CredentialKeys, "persisted-provider-secret")
			closeClient()
			stop()

			second := masterKeyRuntimeResolve(t, input)
			if first.CredentialKeys.ActiveKeyID() != second.CredentialKeys.ActiveKeyID() {
				t.Fatal("master key ID changed across resolutions")
			}
			stop = masterKeyRuntimeBoot(t, second)
			client, closeClient = masterKeyRuntimeClient(t, &second.Config)
			secret, err := service.NewCredentialService(client, second.CredentialKeys, nil).Resolve(context.Background(), credential.Binding{ID: created.ID, Version: created.CurrentVersion}, created.Provider, created.Endpoint)
			if err != nil || secret != "persisted-provider-secret" {
				t.Fatalf("credential did not survive restart: %v", err)
			}
			closeClient()
			stop()
			if first.Config.Credentials.KeyringFile != "" || second.KeyringPending {
				t.Fatal("master-key deployment retained file-generation configuration")
			}
			if _, err := os.Stat(filepath.Join(first.Config.DataDir, "credentials-keyring.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("master-key deployment generated a keyring file")
			}
		})
	}
}

func TestMasterKeyRuntimeLegacyTransition(t *testing.T) {
	clearDeploymentEnvironment(t)
	ctx := context.Background()
	dir := t.TempDir()
	input := masterKeyRuntimeInputs(t, dir)
	master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{61}, 32))
	legacyJSON, err := json.Marshal(map[string]any{"version": 1, "active_key_id": "legacy-manual-id", "keys": map[string]string{"legacy-manual-id": master}})
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(dir, "legacy.json")
	if _, err := credential.PublishPrivateFile(legacyPath, legacyJSON); err != nil {
		t.Fatal(err)
	}
	input.Environment["LINGUAFLOW_CREDENTIALS_KEYRING_FILE"] = legacyPath
	legacy := masterKeyRuntimeResolve(t, input)
	stop := masterKeyRuntimeBoot(t, legacy)
	client, closeClient := masterKeyRuntimeClient(t, &legacy.Config)
	created := masterKeyRuntimeCreate(t, client, legacy.CredentialKeys, "revoked-provider-secret")
	credentials := service.NewCredentialService(client, legacy.CredentialKeys, nil)
	current, err := credentials.Rotate(ctx, created.OwnerID, created.ID, "current-provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	oldBinding := credential.Binding{ID: created.ID, Version: 1}
	if err := credentials.Revoke(ctx, created.OwnerID, oldBinding); err != nil {
		t.Fatal(err)
	}
	before := client.CredentialVersion.Query().Order(ent.Asc(credentialversion.FieldVersion)).AllX(ctx)
	backend := client.Backend.Create().SetName("retained-backend").SetScope(service.ScopeUser).SetOwnerUserID(created.OwnerID).SetBackendType("openai").SetCredentialID(created.ID).SaveX(ctx)
	project := client.Project.Create().SetName("retained-project").SetOwnerUserID(created.OwnerID).SaveX(ctx)
	job := client.Job.Create().SetProjectID(project.ID).SetExecutionPlanID(1).SetStatus("completed").SaveX(ctx)
	reference := client.CredentialJobReference.Create().SetJobID(job.ID).SetCredentialVersionID(before[0].ID).SaveX(ctx)
	closeClient()
	stop()

	delete(input.Environment, "LINGUAFLOW_CREDENTIALS_KEYRING_FILE")
	input.Environment["LINGUAFLOW_CREDENTIALS_MASTER_KEY"] = master
	bare := masterKeyRuntimeResolve(t, input)
	failed, listener, cleanup, err := bootstrapServer(ctx, BootOptions{Resolved: bare, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil {
		_ = failed.Shutdown(ctx)
		_ = listener.Close()
		_ = cleanup()
		t.Fatal("bare master key silently decrypted a credential with a legacy key ID")
	}
	if !errors.Is(err, credential.ErrKeyUnavailable) {
		t.Fatalf("expected legacy key ID rejection, got %v", err)
	}
	transition, err := legacy.CredentialKeys.WithMasterKey(master)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := credential.EncodeKeyring(transition)
	if err != nil {
		t.Fatal(err)
	}
	transitionPath := filepath.Join(dir, "transition.json")
	if _, err := credential.PublishPrivateFile(transitionPath, encoded); err != nil {
		t.Fatal(err)
	}
	delete(input.Environment, "LINGUAFLOW_CREDENTIALS_MASTER_KEY")
	input.Environment["LINGUAFLOW_CREDENTIALS_KEYRING_FILE"] = transitionPath
	stop = masterKeyRuntimeBoot(t, masterKeyRuntimeResolve(t, input))
	stop()
	for name, value := range input.Environment {
		t.Setenv(name, value)
	}
	for _, count := range []string{"2", "0"} {
		root, _ := newRoot()
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetErr(&output)
		root.SetArgs([]string{"admin", "credentials", "reencrypt"})
		if err := root.ExecuteContext(ctx); err != nil || !strings.Contains(output.String(), "Re-encrypted "+count+" credential versions") {
			t.Fatalf("reencrypt command: %v; %s", err, output.String())
		}
	}
	delete(input.Environment, "LINGUAFLOW_CREDENTIALS_KEYRING_FILE")
	input.Environment["LINGUAFLOW_CREDENTIALS_MASTER_KEY"] = master
	final := masterKeyRuntimeResolve(t, input)
	stop = masterKeyRuntimeBoot(t, final)
	defer stop()
	client, closeClient = masterKeyRuntimeClient(t, &final.Config)
	defer closeClient()
	after := client.CredentialVersion.Query().Order(ent.Asc(credentialversion.FieldVersion)).AllX(ctx)
	if len(after) != len(before) {
		t.Fatal("rotation changed credential version count")
	}
	for i, row := range after {
		if row.ID != before[i].ID || row.CredentialID != before[i].CredentialID || row.Version != before[i].Version || row.Revoked != before[i].Revoked || row.KeyID != final.CredentialKeys.ActiveKeyID() || bytes.Equal(row.Nonce, before[i].Nonce) {
			t.Fatal("rotation altered a logical binding or failed to refresh encryption")
		}
	}
	retained := client.CredentialJobReference.GetX(ctx, reference.ID)
	retainedBackend := client.Backend.GetX(ctx, backend.ID)
	if retained.JobID != job.ID || retained.CredentialVersionID != before[0].ID || retainedBackend.CredentialID == nil || *retainedBackend.CredentialID != created.ID || client.Credential.GetX(ctx, created.ID).CurrentVersion != current.Version {
		t.Fatal("rotation changed task or backend credential bindings")
	}
	credentials = service.NewCredentialService(client, final.CredentialKeys, nil)
	if secret, err := credentials.Resolve(ctx, current, created.Provider, created.Endpoint); err != nil || secret != "current-provider-secret" {
		t.Fatalf("bare master key cannot decrypt after transition: %v", err)
	}
	if _, err := credentials.Resolve(ctx, oldBinding, created.Provider, created.Endpoint); !errors.Is(err, credential.ErrRevoked) {
		t.Fatalf("rotation changed revocation behavior: %v", err)
	}
	retainedKeys, err := credential.LoadKeyring(transitionPath)
	if err != nil || !retainedKeys.HasKey("legacy-manual-id") {
		t.Fatal("reencryption removed the legacy key needed by backups")
	}
}

func TestMasterKeyRuntimeResolvedFileSnapshot(t *testing.T) {
	dir := t.TempDir()
	input := masterKeyRuntimeInputs(t, dir)
	path := filepath.Join(dir, "keyring.json")
	if _, err := credential.PrepareKeyring(path, true); err != nil {
		t.Fatal(err)
	}
	input.Environment["LINGUAFLOW_CREDENTIALS_KEYRING_FILE"] = path
	first := masterKeyRuntimeResolve(t, input)
	stop := masterKeyRuntimeBoot(t, first)
	client, closeClient := masterKeyRuntimeClient(t, &first.Config)
	created := masterKeyRuntimeCreate(t, client, first.CredentialKeys, "snapshot-provider-secret")
	closeClient()
	stop()
	resolved := masterKeyRuntimeResolve(t, input)
	if err := os.WriteFile(path, []byte("invalid replacement keyring"), 0600); err != nil {
		t.Fatal(err)
	}
	stop = masterKeyRuntimeBoot(t, resolved)
	defer stop()
	client, closeClient = masterKeyRuntimeClient(t, &resolved.Config)
	defer closeClient()
	secret, err := service.NewCredentialService(client, resolved.CredentialKeys, nil).Resolve(context.Background(), credential.Binding{ID: created.ID, Version: 1}, created.Provider, created.Endpoint)
	if err != nil || secret != "snapshot-provider-secret" {
		t.Fatalf("bootstrap reread the keyring after resolution: %v", err)
	}
	if _, err := config.ResolveServerConfig(input); err == nil {
		t.Fatal("new resolution ignored a corrupt keyring file")
	}
}

func TestMasterKeyRuntimeLocalMissingExistingKey(t *testing.T) {
	initial := localResolution(t)
	stop := masterKeyRuntimeBoot(t, initial)
	stop()
	path := initial.Config.Credentials.KeyringFile
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	resolved := masterKeyRuntimeResolve(t, config.ServerInputs{Mode: config.ModeLocal, WorkingDirectory: initial.Config.DataDir, Environment: map[string]string{
		"LINGUAFLOW_DATA_DIR": initial.Config.DataDir, "LINGUAFLOW_PORT": "0",
	}})
	server, listener, cleanup, err := bootstrapServer(context.Background(), BootOptions{Resolved: resolved, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil {
		_ = server.Shutdown(context.Background())
		_ = listener.Close()
		_ = cleanup()
		t.Fatal("existing local instance silently generated a replacement keyring")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed local restart published a replacement keyring")
	}
}
