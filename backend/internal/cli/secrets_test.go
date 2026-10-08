package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

func executeSecrets(args ...string) (string, error) {
	root, _ := newRoot()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(append([]string{"secrets"}, args...))
	err := root.Execute()
	return output.String(), err
}

func TestSecretsGenerateIsOfflineAndRequiresExplicitOutput(t *testing.T) {
	clearDeploymentEnvironment(t)
	dir := t.TempDir()
	t.Setenv("LINGUAFLOW_SERVER_CONFIG", filepath.Join(dir, "missing.yaml"))
	t.Setenv("LINGUAFLOW_JWT_SECRET", "too-short")
	t.Setenv("LINGUAFLOW_JWT_SECRET_FILE", filepath.Join(dir, "missing-jwt"))
	t.Setenv("LINGUAFLOW_DATABASE_DRIVER", "invalid")
	t.Setenv("LINGUAFLOW_CREDENTIALS_MASTER_KEY", "invalid-master-key")
	output, err := executeSecrets("generate", "--stdout", "--config", filepath.Join(dir, "also-missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	value := strings.TrimSuffix(output, "\n")
	if output != value+"\n" || strings.Contains(value, "\n") {
		t.Fatal("stdout must contain only one key followed by one newline")
	}
	if _, err := credential.FromMasterKey(value); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secret")
	output, err = executeSecrets("generate", "--output", path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fileValue := strings.TrimSuffix(string(data), "\n")
	if fileValue == value {
		t.Fatal("independent invocations reused a key")
	}
	if _, err := credential.FromMasterKey(fileValue); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, fileValue) || !strings.Contains(output, path) {
		t.Fatal("file output must report its path without disclosing key material")
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("secret file is not private: %04o", info.Mode().Perm())
	}
	if _, err := executeSecrets("generate", "--output", path); err == nil {
		t.Fatal("existing secret was overwritten")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatalf("refused overwrite changed output: %v", err)
	}
	for _, args := range [][]string{
		{"generate"}, {"generate", "--stdout=false"},
		{"generate", "--output="}, {"generate", "--output", " "},
		{"generate", "--output", filepath.Join(dir, "invalid"), "--stdout"},
		{"generate", "--output=", "--stdout"},
		{"generate", "--stdout", "--master-key", "forbidden-secret-argument"},
	} {
		if _, err := executeSecrets(args...); err == nil {
			t.Fatalf("invalid output or plaintext argument accepted: %v", args)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "invalid")); !os.IsNotExist(err) {
		t.Fatal("conflicting output flags created a file")
	}
}

func TestSecretsKeyringImportAndRotatePreserveOldCiphertext(t *testing.T) {
	clearDeploymentEnvironment(t)
	dir := t.TempDir()
	oldValue := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	newValue := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	legacyPath := filepath.Join(dir, "legacy.json")
	legacyData, err := json.Marshal(map[string]any{
		"version": 1, "active_key_id": "operator-chosen-old-id", "keys": map[string]string{"operator-chosen-old-id": oldValue},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credential.PublishPrivateFile(legacyPath, legacyData); err != nil {
		t.Fatal(err)
	}
	old, err := credential.LoadKeyring(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	aad := credential.AssociatedData{ID: 1, Version: 1, Provider: "openai", Scope: "user", OwnerID: 1}
	encrypted, err := old.Encrypt("existing-provider-key", aad)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LINGUAFLOW_CREDENTIALS_MASTER_KEY", newValue)
	t.Setenv("LINGUAFLOW_CREDENTIALS_KEYRING_FILE", filepath.Join(dir, "unrelated-missing.json"))
	t.Setenv("LINGUAFLOW_SERVER_CONFIG", filepath.Join(dir, "missing-config.yaml"))
	t.Setenv("LINGUAFLOW_JWT_SECRET", "bad")
	initializedPath := filepath.Join(dir, "initialized.json")
	output, err := executeSecrets("keyring", "init", "--output", initializedPath, "--from-master-key-env")
	if err != nil {
		t.Fatal(err)
	}
	single, err := credential.FromMasterKey(newValue)
	if err != nil {
		t.Fatal(err)
	}
	initialized, err := credential.LoadKeyring(initializedPath)
	if err != nil || initialized.ActiveKeyID() != single.ActiveKeyID() {
		t.Fatalf("keyring init did not use imported master key: %v", err)
	}
	if strings.Contains(output, newValue) || !strings.Contains(output, single.ActiveKeyID()) {
		t.Fatal("init output must show only public key ID and path")
	}
	rotatedPath := filepath.Join(dir, "rotated.json")
	output, err = executeSecrets("keyring", "rotate", "--input", legacyPath, "--output", rotatedPath, "--from-master-key-env")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := credential.LoadKeyring(rotatedPath)
	if err != nil || rotated.ActiveKeyID() != single.ActiveKeyID() || !rotated.HasKey(old.ActiveKeyID()) {
		t.Fatalf("rotation lost an old ID or did not activate imported key: %v", err)
	}
	if plain, err := rotated.Decrypt(encrypted, aad); err != nil || plain != "existing-provider-key" {
		t.Fatalf("rotation cannot decrypt old ciphertext: %v", err)
	}
	if strings.Contains(output, oldValue) || strings.Contains(output, newValue) {
		t.Fatal("rotation output exposed key material")
	}
	unchanged, err := os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(legacyData, unchanged) {
		t.Fatalf("rotation changed original file: %v", err)
	}
	for _, args := range [][]string{
		{"keyring", "init", "--output", initializedPath},
		{"keyring", "rotate", "--input", legacyPath, "--output", legacyPath},
		{"keyring", "rotate", "--input", legacyPath, "--output", rotatedPath},
	} {
		if _, err := executeSecrets(args...); err == nil {
			t.Fatalf("existing keyring overwritten: %v", args)
		}
	}
	unchanged, err = os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(legacyData, unchanged) {
		t.Fatalf("refused overwrite changed original file: %v", err)
	}
}

func TestSecretsKeyringImportFileAndErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		direct   *string
		fileData *string
		valid    bool
	}{
		{name: "missing"},
		{name: "empty", direct: stringPointer("")},
		{name: "invalid", direct: stringPointer("invalid-sensitive-value")},
		{name: "both", direct: stringPointer("invalid-sensitive-value"), fileData: stringPointer("file-sensitive-value\n")},
		{name: "bad_file", fileData: stringPointer("file-sensitive-value\n")},
		{name: "crlf_file", fileData: stringPointer(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32)) + "\r\n"), valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearDeploymentEnvironment(t)
			dir := t.TempDir()
			if test.direct != nil {
				t.Setenv("LINGUAFLOW_CREDENTIALS_MASTER_KEY", *test.direct)
			}
			if test.fileData != nil {
				path := filepath.Join(dir, "master-key")
				if err := os.WriteFile(path, []byte(*test.fileData), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE", path)
			}
			path := filepath.Join(dir, "keyring.json")
			output, err := executeSecrets("keyring", "init", "--output", path, "--from-master-key-env")
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := credential.LoadKeyring(path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("missing or malformed master key accepted")
				}
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatal("rejected source created output")
				}
			}
			for _, secret := range []*string{test.direct, test.fileData} {
				if secret != nil && *secret != "" && strings.Contains(output+fmt.Sprint(err), strings.TrimSpace(*secret)) {
					t.Fatal("source validation leaked secret material")
				}
			}
		})
	}
}

func TestSecretsKeyringRandomDefaultsAndInvalidFlags(t *testing.T) {
	clearDeploymentEnvironment(t)
	dir := t.TempDir()
	t.Setenv("LINGUAFLOW_CREDENTIALS_MASTER_KEY", "invalid-unused-key")
	t.Setenv("LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE", filepath.Join(dir, "missing-unused-file"))
	firstPath, nextPath := filepath.Join(dir, "first.json"), filepath.Join(dir, "next.json")
	if _, err := executeSecrets("keyring", "init", "--output", firstPath, "--from-master-key-env=false"); err != nil {
		t.Fatal(err)
	}
	if _, err := executeSecrets("keyring", "rotate", "--input", firstPath, "--output", nextPath); err != nil {
		t.Fatal(err)
	}
	first, err := credential.LoadKeyring(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	next, err := credential.LoadKeyring(nextPath)
	if err != nil || next.ActiveKeyID() == first.ActiveKeyID() || !next.HasKey(first.ActiveKeyID()) {
		t.Fatalf("random rotation did not retain old and generate new keys: %v", err)
	}
	for _, args := range [][]string{
		{"keyring", "init"}, {"keyring", "init", "--output="},
		{"keyring", "init", "--output", " "}, {"keyring", "init", "--stdout"},
		{"keyring", "rotate", "--output", filepath.Join(dir, "invalid")},
		{"keyring", "rotate", "--input=", "--output", filepath.Join(dir, "invalid")},
		{"keyring", "rotate", "--input", firstPath},
		{"keyring", "rotate", "--input", firstPath, "--output", nextPath, "--force"},
	} {
		if _, err := executeSecrets(args...); err == nil {
			t.Fatalf("invalid keyring flags accepted: %v", args)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "invalid")); !os.IsNotExist(err) {
		t.Fatal("invalid flags created output")
	}
}

func TestSecretsKeyringRotateRejectsOversizedOutput(t *testing.T) {
	clearDeploymentEnvironment(t)
	dir := t.TempDir()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	largeLegacyID := strings.Repeat("x", 1048300)
	input, err := json.Marshal(map[string]any{
		"version": 1, "active_key_id": "legacy", "keys": map[string]string{"legacy": key, largeLegacyID: key},
	})
	if err != nil {
		t.Fatal(err)
	}
	inputPath, outputPath := filepath.Join(dir, "old.json"), filepath.Join(dir, "new.json")
	if _, err := credential.PublishPrivateFile(inputPath, input); err != nil {
		t.Fatal(err)
	}
	if _, err := credential.LoadKeyring(inputPath); err != nil {
		t.Fatalf("input is not a valid loadable keyring: %v", err)
	}
	output, err := executeSecrets("keyring", "rotate", "--input", inputPath, "--output", outputPath)
	if err == nil || !strings.Contains(err.Error(), "file size limit") {
		t.Fatalf("oversized output accepted or unexplained: %v", err)
	}
	if strings.Contains(output+err.Error(), key) || strings.Contains(output+err.Error(), largeLegacyID) {
		t.Fatal("size error exposed keyring contents")
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatal("rejected oversized keyring created output")
	}
	after, err := os.ReadFile(inputPath)
	if err != nil || !bytes.Equal(input, after) {
		t.Fatalf("rejected rotation changed old file: %v", err)
	}
}

func stringPointer(value string) *string { return &value }
