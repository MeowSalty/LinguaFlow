package migrationcli

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

func clearDeploymentEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "LINGUAFLOW_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMigrationHelpDoesNotPrepareInstance(t *testing.T) {
	clearDeploymentEnvironment(t)
	dir := filepath.Join(t.TempDir(), "not-created")
	t.Setenv("LINGUAFLOW_DATA_DIR", dir)
	t.Setenv("LINGUAFLOW_DATABASE_DSN_FILE", filepath.Join(dir, "missing-secret"))
	for _, args := range [][]string{{"--help"}, {"v013", "--help"}, {"v013", "postgres", "--help"}, {"v013", "local", "--help"}} {
		root := NewCommand()
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("help %v: %v", args, err)
		}
		if !strings.Contains(output.String(), "Usage:") {
			t.Fatalf("help missing usage: %s", output.String())
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("help created files")
	}
}

func TestMigrationLocalInputValidation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		environment string
		want        string
	}{
		{name: "missing directory", want: "data-dir"},
		{name: "empty directory", args: []string{"--data-dir="}, want: "must not be empty"},
		{name: "external keyring", environment: "LINGUAFLOW_CREDENTIALS_KEYRING_FILE", want: "unset LINGUAFLOW_CREDENTIALS_KEYRING_FILE"},
		{name: "external master key", environment: "LINGUAFLOW_CREDENTIALS_MASTER_KEY", want: "unset LINGUAFLOW_CREDENTIALS_MASTER_KEY"},
		{name: "external master key file", environment: "LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE", want: "unset LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE"},
		{name: "external jwt", environment: "LINGUAFLOW_JWT_SECRET_FILE", want: "unset LINGUAFLOW_JWT_SECRET_FILE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearDeploymentEnvironment(t)
			dir := filepath.Join(t.TempDir(), "not-created")
			args := append([]string{"v013", "local"}, tc.args...)
			if tc.environment != "" {
				t.Setenv(tc.environment, "private-input")
				args = append(args, "--data-dir", dir)
			}
			root := NewCommand()
			root.SetArgs(args)
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %s", err, tc.want)
			}
			if strings.Contains(err.Error(), "private-input") {
				t.Fatal("input error leaked value")
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid input created files")
			}
		})
	}
}

func TestPostgresMasterKeySelection(t *testing.T) {
	master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	want, err := credential.FromMasterKey(master)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"environment", "file"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			dataDir := filepath.Join(dir, "not-created")
			environment := map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master}
			if source == "file" {
				if err := os.WriteFile(filepath.Join(dir, "master-key"), []byte(master+"\r\n"), 0600); err != nil {
					t.Fatal(err)
				}
				environment = map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE": "master-key"}
			}
			keys, path, pending, err := postgresKeyring(environment, dir, dataDir, "", false)
			if err != nil || path != "" || len(pending) != 0 || keys == nil {
				t.Fatalf("master key selection generated another key: path=%q pending=%d error=%v", path, len(pending), err)
			}
			aad := credential.AssociatedData{ID: 1, Version: 1, Provider: "openai", Scope: "user", OwnerID: 1}
			encrypted, err := keys.Encrypt("migration-private-value", aad)
			if err != nil {
				t.Fatal(err)
			}
			if plain, err := want.Decrypt(encrypted, aad); err != nil || plain != "migration-private-value" {
				t.Fatalf("migration did not use the supplied master key: %v", err)
			}
			if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("master key selection created files")
			}
			// An unrelated default keyring is not an explicit configuration source.
			if err := os.Mkdir(dataDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dataDir, "credentials-keyring.json"), []byte("invalid old default"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, path, pending, err := postgresKeyring(environment, dir, dataDir, "", false); err != nil || path != "" || len(pending) != 0 {
				t.Fatalf("master key unexpectedly consulted default keyring: %v", err)
			}
		})
	}
}

func TestLocalMigrationRejectsEmptyMasterKeySources(t *testing.T) {
	for _, name := range []string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY", "LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE"} {
		t.Run(name, func(t *testing.T) {
			clearDeploymentEnvironment(t)
			t.Setenv(name, "")
			dir := filepath.Join(t.TempDir(), "not-created")
			root := NewCommand()
			root.SetArgs([]string{"v013", "local", "--data-dir", dir})
			if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "unset "+name) {
				t.Fatalf("local migration silently ignored external master-key configuration: %v", err)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected local migration created files")
			}
		})
	}
}

func TestPostgresMasterKeyInputValidation(t *testing.T) {
	master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	for _, tc := range []struct {
		name        string
		environment map[string]string
		flags       []string
		want        string
	}{
		{"empty master", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": ""}, nil, "MASTER_KEY"},
		{"invalid master", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": "private-invalid-input"}, nil, "MASTER_KEY"},
		{"empty file", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE": ""}, nil, "secret file path must not be empty"},
		{"two master sources", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master, "LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE": ""}, nil, "conflict"},
		{"empty keyring environment conflict", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master, "LINGUAFLOW_CREDENTIALS_KEYRING_FILE": ""}, nil, "conflict"},
		{"keyring flag conflict", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master}, []string{"--keyring-file=old.json"}, "conflict"},
		{"empty keyring flag conflict", map[string]string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY": master}, []string{"--keyring-file="}, "conflict"},
		{"empty keyring environment", map[string]string{"LINGUAFLOW_CREDENTIALS_KEYRING_FILE": ""}, nil, "must not be empty"},
		{"empty keyring flag", nil, []string{"--keyring-file="}, "must not be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearDeploymentEnvironment(t)
			t.Setenv("LINGUAFLOW_DATABASE_DSN", "unused-private-dsn")
			for name, value := range tc.environment {
				t.Setenv(name, value)
			}
			dir := filepath.Join(t.TempDir(), "not-created")
			root := NewCommand()
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs(append([]string{"v013", "postgres", "--data-dir", dir}, tc.flags...))
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %s", err, tc.want)
			}
			for _, value := range []string{master, "private-invalid-input", "unused-private-dsn"} {
				if strings.Contains(err.Error()+output.String(), value) {
					t.Fatal("invalid credential source exposed secret input")
				}
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid credential source created files")
			}
		})
	}
}

func TestPostgresMasterKeyStartupInstructions(t *testing.T) {
	root := NewCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	if err := printPostgresStartup(root, "data", "", ""); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "same LINGUAFLOW_CREDENTIALS_MASTER_KEY") || !strings.Contains(text, "same LINGUAFLOW_JWT_SECRET") {
		t.Fatalf("missing instructions to retain supplied secrets: %s", text)
	}
	if strings.Contains(text, "LINGUAFLOW_CREDENTIALS_KEYRING_FILE=") || strings.Contains(text, "credentials-keyring.json") {
		t.Fatal("master-key migration instructed serve to use an absent keyring")
	}
}

func TestPostgresSecretSourceValidation(t *testing.T) {
	for _, tc := range []struct{ name, direct, filename, contents, want string }{
		{"conflict", "", "dsn", "sensitive-dsn", "conflicts"},
		{"empty file", "", "dsn", "", "empty"},
		{"missing file", "", "missing", "", "cannot read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearDeploymentEnvironment(t)
			temp := t.TempDir()
			path := filepath.Join(temp, tc.filename)
			if tc.filename != "missing" {
				if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("LINGUAFLOW_DATABASE_DSN_FILE", path)
			if tc.name == "conflict" {
				t.Setenv("LINGUAFLOW_DATABASE_DSN", tc.direct)
			}
			dir := filepath.Join(temp, "not-created")
			root := NewCommand()
			root.SetArgs([]string{"v013", "postgres", "--data-dir", dir})
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %s", err, tc.want)
			}
			if strings.Contains(err.Error(), "sensitive-dsn") {
				t.Fatal("error exposed DSN")
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid secret created files")
			}
		})
	}
}

func TestServiceDependenciesExcludeMigration(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/linguaflow")
	cmd.Dir = filepath.Join("..", "..")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect service dependencies: %v\n%s", err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if strings.Contains(dependency, "/internal/migration/") || strings.HasSuffix(dependency, "/internal/migrationcli") {
			t.Fatalf("normal service imports one-time migration code: %s", dependency)
		}
	}
}

func TestMigrateV013InputValidation(t *testing.T) {
	for _, tc := range []struct{ name, driver, dsn, jwt, want string }{
		{"missing connection", "", "", "", "DATABASE_DSN"},
		{"SQLite rejected", "sqlite", "secret-dsn", "", "only supports PostgreSQL"},
		{"short JWT", "postgres", "secret-dsn", "short-private-value", "at least 32 bytes"},
		{"explicit empty JWT", "postgres", "secret-dsn", "", "at least 32 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearDeploymentEnvironment(t)
			t.Setenv("LINGUAFLOW_DATABASE_DRIVER", tc.driver)
			t.Setenv("LINGUAFLOW_DATABASE_DSN", tc.dsn)
			t.Setenv("LINGUAFLOW_JWT_SECRET", tc.jwt)
			dir := filepath.Join(t.TempDir(), "not-created")
			root := NewCommand()
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs([]string{"v013", "postgres", "--data-dir", dir})
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %s", err, tc.want)
			}
			if strings.Contains(output.String(), "secret-dsn") || strings.Contains(output.String(), "short-private-value") {
				t.Fatal("diagnostics exposed secret input")
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid configuration created files")
			}
		})
	}
}

func TestMigrateV013KeyringIsOnlyPublishedExplicitly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "keyring.json")
	keys, pending, err := migrationKeyring(path)
	if err != nil || keys == nil || len(pending) == 0 {
		t.Fatalf("prepare ephemeral keyring: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rehearsal preparation created directories")
	}
	if published, err := credential.PublishPrivateFile(path, pending); err != nil || !published {
		t.Fatalf("publish keyring: %v", err)
	}
	reused, second, err := migrationKeyring(path)
	if err != nil || len(second) != 0 {
		t.Fatalf("existing keyring was not reused: %v", err)
	}
	aad := credential.AssociatedData{ID: 1, Version: 1, Provider: "openai", Endpoint: "https://api.openai.com/v1", Scope: "user", OwnerID: 1}
	encrypted, err := keys.Encrypt("old-provider-secret", aad)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := reused.Decrypt(encrypted, aad); err != nil || plain != "old-provider-secret" {
		t.Fatal("published keyring cannot decrypt migration output")
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := migrationKeyring(path); err == nil {
		t.Fatal("corrupt existing keyring was silently replaced")
	}
}
