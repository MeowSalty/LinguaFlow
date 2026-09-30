package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestAdministratorInitializationCommands(t *testing.T) {
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
	run := func(args ...string) {
		t.Helper()
		root, _ := newRoot()
		root.SetIn(strings.NewReader("administrator-password\n"))
		root.SetOut(new(bytes.Buffer))
		root.SetErr(new(bytes.Buffer))
		root.SetArgs(append([]string{"admin"}, args...))
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	run("initialize", "--username", "initial-admin", "--email", "initial@test.invalid", "--password-stdin")
	run("initialize")
	run("create", "--username", "backup-admin", "--email", "backup@test.invalid", "--password-stdin")
	resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: config.ModeServer, Environment: config.Environment()})
	if err != nil {
		t.Fatal(err)
	}
	_, client, cleanup, err := prepareDatabase(context.Background(), &resolved.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	initial := client.User.Query().Where(user.UsernameEQ("initial-admin")).OnlyX(ctx)
	client.User.UpdateOne(initial).SetRole(service.SystemRoleUser).SetActive(false).SaveX(ctx)
	run("recover", "--username", "initial-admin", "--password-stdin")
	recovered := client.User.GetX(ctx, initial.ID)
	if !recovered.Active || recovered.Role != service.SystemRoleAdmin {
		t.Fatal("recover command did not restore the selected administrator")
	}
	if got := client.User.Query().CountX(ctx); got != 2 {
		t.Fatalf("expected initial and backup administrators, got %d", got)
	}
	settings, err := service.NewSettingsService(client).Get(ctx)
	if err != nil || settings.RegistrationEnabled {
		t.Fatalf("maintenance changed registration policy: %+v %v", settings, err)
	}
	if got := client.InstanceInitialization.Query().CountX(ctx); got != 1 {
		t.Fatalf("expected one initialization marker, got %d", got)
	}
	if got := client.ActivityLog.Query().CountX(ctx); got != 3 {
		t.Fatalf("expected initialization, create and recovery audits, got %d", got)
	}
}
