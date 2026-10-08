package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"gopkg.in/yaml.v3"
)

func TestAdministratorPasswordInput(t *testing.T) {
	value, err := readAdministratorPassword(strings.NewReader("  password  \r\n"), "", true)
	if err != nil || value != "  password  " {
		t.Fatalf("password was altered: %q %v", value, err)
	}
	file := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(file, []byte("password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := readAdministratorPassword(nil, file, false); err != nil || value != "password" {
		t.Fatalf("file input=%q %v", value, err)
	}
	for _, test := range []struct {
		file  string
		stdin bool
	}{{"", false}, {file, true}, {file + "-missing", false}} {
		if _, err := readAdministratorPassword(strings.NewReader("secret"), test.file, test.stdin); err == nil {
			t.Fatal("invalid password source accepted")
		}
	}
}

func TestAdministratorCredentialReencryptCommand(t *testing.T) {
	for _, field := range config.DeploymentFields() {
		for _, name := range []string{field.Environment, field.Environment + "_FILE"} {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	dir := t.TempDir()
	keyringPath := filepath.Join(dir, "keyring.json")
	writeKeys := func(active string, includeOld bool) {
		t.Helper()
		keys := map[string]string{"new": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))}
		if includeOld {
			keys["old"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
		}
		data, err := json.Marshal(map[string]any{"version": 1, "active_key_id": active, "keys": keys})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(keyringPath); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if _, err := credential.PublishPrivateFile(keyringPath, data); err != nil {
			t.Fatal(err)
		}
	}
	writeKeys("old", true)
	cfg := config.DefaultServerConfig()
	cfg.DataDir = dir
	cfg.JWTSecret = strings.Repeat("j", 32)
	cfg.Credentials.KeyringFile = keyringPath
	ctx := context.Background()
	_, client, cleanup, err := prepareDatabase(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewInitializationService(client).Initialize(ctx, config.ModeServer, config.BootstrapInput{Admin: &config.BootstrapAdmin{Username: "admin", Email: "admin@test.invalid", Password: "password-123"}}); err != nil {
		cleanup()
		t.Fatal(err)
	}
	admin := client.User.Query().OnlyX(ctx)
	oldKeys, err := credential.LoadKeyring(keyringPath)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	created, err := service.NewCredentialService(client, oldKeys, nil).Create(ctx, admin.ID, service.CreateCredentialInput{Scope: service.ScopeUser, OwnerID: admin.ID, Provider: "openai", Secret: "provider-secret"})
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	before := client.CredentialVersion.Query().OnlyX(ctx)
	storageConnection := client.StorageConnection.Create().SetName("stored-cloud").SetDriver("s3").SetOwnerKind("user").SetOwnerID(admin.ID).SetEndpoint("https://s3.example").SetRegion("test").SetAuthSource("stored").SetActiveAuthGeneration(1).SaveX(ctx)
	storageIdentity := storageauth.Identity{ConnectionID: storageConnection.ID, Scope: "user", OwnerID: admin.ID, Driver: "s3", Endpoint: storageConnection.Endpoint, AuthGeneration: 1}
	storageCipher, err := storageauth.EncryptS3(oldKeys, storageIdentity, storageauth.S3Payload{Version: 1, AccessKeyID: "storage-access", SecretAccessKey: "storage-secret"})
	if err != nil {
		t.Fatal(err)
	}
	storageBefore := client.StorageAuthVersion.Create().SetConnectionID(storageConnection.ID).SetGeneration(1).SetKeyID(storageCipher.KeyID).SetNonce(storageCipher.Nonce).SetCiphertext(storageCipher.Data).SetStatus("active").SaveX(ctx)
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "server.yaml")
	document, err := yaml.Marshal(map[string]any{"kind": "server", "version": 1, "server": map[string]any{"data_dir": dir, "jwt_secret": cfg.JWTSecret, "credentials": map[string]any{"keyring_file": keyringPath}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, document, 0600); err != nil {
		t.Fatal(err)
	}
	run := func() (string, error) {
		root, _ := newRoot()
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetErr(&output)
		root.SetArgs([]string{"admin", "credentials", "reencrypt", "--config", configPath})
		err := root.ExecuteContext(ctx)
		return output.String(), err
	}
	// 不完整的密钥环必须在重写任何版本之前先失败。
	writeKeys("new", false)
	if _, err := run(); err == nil {
		t.Fatal("missing old key was accepted")
	}
	writeKeys("new", true)
	if output, err := run(); err != nil || !strings.Contains(output, "Re-encrypted 1 credential versions") || !strings.Contains(output, "Re-encrypted 1 storage authorization versions; 0 unavailable") {
		t.Fatalf("command output=%s err=%v", output, err)
	}
	if output, err := run(); err != nil || !strings.Contains(output, "Re-encrypted 0 credential versions") || !strings.Contains(output, "Re-encrypted 0 storage authorization versions; 0 unavailable") {
		t.Fatalf("repeat output=%s err=%v", output, err)
	}
	_, client, cleanup, err = prepareDatabase(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	after := client.CredentialVersion.Query().OnlyX(ctx)
	storageAfter := client.StorageAuthVersion.GetX(ctx, storageBefore.ID)
	if storageAfter.KeyID != "new" || storageAfter.Generation != storageBefore.Generation || storageAfter.Status != storageBefore.Status || bytes.Equal(storageAfter.Nonce, storageBefore.Nonce) {
		t.Fatal("storage reencryption changed authorization or did not rotate")
	}
	if after.KeyID != "new" || after.Version != before.Version || after.Revoked != before.Revoked || bytes.Equal(after.Nonce, before.Nonce) {
		t.Fatal("command did not preserve binding or refresh encryption")
	}
	keys, err := credential.LoadKeyring(keyringPath)
	if err != nil {
		t.Fatal(err)
	}
	if !keys.HasKey("old") {
		t.Fatal("command removed an old key")
	}
	secret, err := service.NewCredentialService(client, keys, nil).Resolve(ctx, credential.Binding{ID: created.ID, Version: 1}, created.Provider, created.Endpoint)
	if err != nil || secret != "provider-secret" {
		t.Fatalf("binding stopped resolving: %v", err)
	}
}
