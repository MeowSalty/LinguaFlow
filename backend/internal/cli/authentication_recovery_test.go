package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func executeAdmin(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root, _ := newRoot()
	var out bytes.Buffer
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"admin"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestAuthenticationRecoveryIssueFileAndRestoreContracts(t *testing.T) {
	clearDeploymentEnvironment(t)
	dir := t.TempDir()
	keyringPath := filepath.Join(dir, "keyring.json")
	if _, err := credential.PrepareKeyring(keyringPath, true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LINGUAFLOW_DATA_DIR", dir)
	t.Setenv("LINGUAFLOW_JWT_SECRET", strings.Repeat("s", 32))
	t.Setenv("LINGUAFLOW_CREDENTIALS_KEYRING_FILE", keyringPath)
	t.Setenv("LINGUAFLOW_BOOTSTRAP_REGISTRATION_ENABLED", "false")
	if out, err := executeAdmin(t, "initial-password\n", "initialize", "--username", "recovery-admin", "--password-stdin"); err != nil {
		t.Fatalf("initialize: %v %s", err, out)
	}
	tokenPath := filepath.Join(dir, "recovery-token")
	out, err := executeAdmin(t, "", "authentication-recovery", "issue", "--username", "recovery-admin", "--output", tokenPath)
	if err != nil {
		t.Fatalf("issue: %v %s", err, out)
	}
	published, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, strings.TrimSpace(string(published))) {
		t.Fatal("issue leaked token to command output")
	}
	if info, err := os.Stat(tokenPath); err == nil && runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("token file is not private: %04o", info.Mode().Perm())
	}
	before, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeAdmin(t, "", "authentication-recovery", "issue", "--username", "recovery-admin", "--output", tokenPath); err == nil {
		t.Fatal("existing token file was overwritten")
	}
	after, err := os.ReadFile(tokenPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected overwrite changed token file: %v", err)
	}
	if _, err := executeAdmin(t, "new-password\n", "authentication-recovery", "restore-local-login", "--token-file", tokenPath); err == nil {
		t.Fatal("restore accepted both password sources")
	}
	if _, err := executeAdmin(t, "", "authentication-recovery", "restore-local-login", "--token-file", tokenPath, "--password-file", filepath.Join(dir, "missing-password")); err == nil {
		t.Fatal("restore accepted missing password file")
	}
	resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: config.ModeServer, Environment: config.Environment()})
	if err != nil {
		t.Fatal(err)
	}
	_, client, cleanup, err := prepareDatabase(context.Background(), &resolved.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	admin := client.User.Query().Where(user.UsernameEQ("recovery-admin")).OnlyX(context.Background())
	passwordFile := filepath.Join(dir, "password")
	if err := os.WriteFile(passwordFile, []byte("file-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = executeAdmin(t, "", "authentication-recovery", "restore-local-login", "--token-file", tokenPath, "--password-file", passwordFile)
	if err != nil {
		t.Fatalf("restore: %v %s", err, out)
	}
	if !strings.Contains(out, "site-wide password login") || !strings.Contains(out, "username login") || !strings.Contains(out, "disabled local signup") {
		t.Fatalf("restore output did not describe site-wide effect: %s", out)
	}
	recovered := client.User.GetX(context.Background(), admin.ID)
	if !recovered.Active || recovered.Role != service.SystemRoleAdmin {
		t.Fatalf("restore did not retain target administrator: %+v", recovered)
	}
	p, err := client.LocalAccountPolicy.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !p.PasswordLoginEnabled || !p.UsernameLoginEnabled || p.SignupEnabled {
		t.Fatalf("restore did not apply site-wide policy: %+v", p)
	}
}
