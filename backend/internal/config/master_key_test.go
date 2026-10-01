package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

func masterKeyInputs(t *testing.T, mode string) ServerInputs {
	t.Helper()
	dir := t.TempDir()
	return ServerInputs{Mode: mode, WorkingDirectory: dir, UserConfigDir: dir, Environment: map[string]string{
		"LINGUAFLOW_JWT_SECRET": strings.Repeat("j", 32),
		"LINGUAFLOW_DATA_DIR":   filepath.Join(dir, "instance"),
	}}
}

func TestDeploymentMasterKeySources(t *testing.T) {
	master := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32)))
	want, err := credential.FromMasterKey(master)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{ModeServer, ModeLocal} {
		for _, source := range []string{"env", "file", "yaml", "reference"} {
			t.Run(mode+"/"+source, func(t *testing.T) {
				in := masterKeyInputs(t, mode)
				switch source {
				case "env":
					in.Environment["LINGUAFLOW_CREDENTIALS_MASTER_KEY"] = master
				case "file":
					path := filepath.Join(in.WorkingDirectory, "master")
					// Secret mounts need not satisfy the private keyring ACL policy.
					if err := os.WriteFile(path, []byte(master+"\r\n"), 0644); err != nil {
						t.Fatal(err)
					}
					in.Environment["LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE"] = "master"
				case "yaml":
					withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  credentials:\n    master_key: "+master+"\n")
				case "reference":
					in.Environment["PRIVATE_MASTER"] = master
					withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  credentials:\n    master_key: ${PRIVATE_MASTER}\n")
				}
				r, err := ResolveServerConfig(in)
				if err != nil {
					t.Fatal(err)
				}
				if r.CredentialKeys == nil || r.CredentialKeys.ActiveKeyID() != want.ActiveKeyID() {
					t.Fatal("master key ID depends on input source")
				}
				if r.masterKey != "" || r.KeyringPending || r.Config.Credentials.KeyringFile != "" {
					t.Fatal("master key input retained or file preparation requested")
				}
				if _, err := os.Stat(r.Config.DataDir); !os.IsNotExist(err) {
					t.Fatal("offline master-key resolution created data directory")
				}
				field := explainField(t, r, "server.credentials.master_key")
				if field.Value != "configured (redacted)" {
					t.Fatal("master-key state not explained")
				}
				raw, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw)+fmt.Sprintf("%+v %#v", r, r), master) {
					t.Fatal("resolved configuration exposes master key")
				}
			})
		}
	}
}

func TestDeploymentCredentialSourcesRejectAmbiguity(t *testing.T) {
	master := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32)))
	for _, tc := range []struct {
		name string
		env  map[string]string
		yaml string
		want string
	}{
		{"missing", nil, "", "is required"},
		{"empty", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": ""}, "", "canonical base64"},
		{"invalid", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": "private-invalid-value"}, "", "canonical base64"},
		{"two-env-sources", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master, "LINGUAFLOW_CREDENTIALS_KEYRING_FILE": "missing"}, "", "conflicts"},
		{"empty-file-source", map[string]string{"LINGUAFLOW_CREDENTIALS_KEYRING_FILE": ""}, "", "must not be empty"},
		{"empty-file-conflict", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master, "LINGUAFLOW_CREDENTIALS_KEYRING_FILE": ""}, "", "conflicts"},
		{"direct-and-file", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": "", "LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE": "missing"}, "", "conflicts"},
		{"missing-secret-file", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE": "private-missing-file"}, "", "cannot read secret file"},
		{"yaml-ring-env-master", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master}, "keyring_file: missing", "conflicts"},
		{"yaml-master-env-ring", map[string]string{"LINGUAFLOW_CREDENTIALS_KEYRING_FILE": "missing"}, "master_key: " + master, "conflicts"},
		{"yaml-empty-master", nil, "master_key: \"\"", "canonical base64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := masterKeyInputs(t, ModeServer)
			for key, value := range tc.env {
				in.Environment[key] = value
			}
			if tc.yaml != "" {
				withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  credentials:\n    "+tc.yaml+"\n")
			}
			_, err := ResolveServerConfig(in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s: %v", tc.want, err)
			}
			for _, private := range []string{master, "private-invalid-value", "private-missing-file"} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("credential error exposed private input")
				}
			}
		})
	}
}

func TestDeploymentMasterKeyPrecedence(t *testing.T) {
	in := masterKeyInputs(t, ModeServer)
	master := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32)))
	withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  credentials:\n    master_key: invalid-overridden-value\n")
	in.Environment["LINGUAFLOW_CREDENTIALS_MASTER_KEY"] = master
	if _, err := ResolveServerConfig(in); err != nil {
		t.Fatalf("same-field environment override failed: %v", err)
	}
	in.Environment["LINGUAFLOW_CREDENTIALS_MASTER_KEY"] = ""
	if _, err := ResolveServerConfig(in); err == nil {
		t.Fatal("explicit empty override fell back to YAML")
	}
}

func TestResolvedCredentialKeyringSnapshot(t *testing.T) {
	in := deploymentInputs(t)
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.Config.Credentials.KeyringFile, []byte("corrupted after resolution"), 0600); err != nil {
		t.Fatal(err)
	}
	aad := credential.AssociatedData{ID: 1, Version: 1, OwnerID: 1}
	ciphertext, err := r.CredentialKeys.Encrypt("provider-value", aad)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := r.CredentialKeys.Decrypt(ciphertext, aad); err != nil || value != "provider-value" {
		t.Fatal("resolved keyring changed after file replacement")
	}
	if _, err := ResolveServerConfig(in); err == nil {
		t.Fatal("a new resolution ignored the changed keyring")
	}
}
