package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
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

func TestConfigCommandsDoNotPrepareInstance(t *testing.T) {
	clearDeploymentEnvironment(t)
	for _, action := range []string{"check", "explain"} {
		t.Run(action, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "not-created")
			root, _ := newRoot()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"config", action, "--mode", "local", "--data-dir", dir, "--port", "0"})
			if err := root.Execute(); err != nil {
				t.Fatalf("%v: %s", err, out.String())
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("offline command created instance: %v", err)
			}
			if action == "explain" && !strings.Contains(out.String(), "generated at startup") {
				t.Fatalf("missing pending secret status: %s", out.String())
			}
		})
	}
}

func TestConfigCommandsRespectExplicitFlags(t *testing.T) {
	clearDeploymentEnvironment(t)
	t.Setenv("LINGUAFLOW_HOST", "0.0.0.0")
	t.Setenv("LINGUAFLOW_PORT", "18090")
	dir := filepath.Join(t.TempDir(), "instance")
	root, _ := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"config", "explain", "--mode", "local", "--data-dir", dir})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "allow-network") {
		t.Fatalf("network boundary=%v", err)
	}
	root, _ = newRoot()
	out.Reset()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"config", "explain", "--mode", "local", "--data-dir", dir, "--allow-network", "--verbose"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "18090") || !strings.Contains(out.String(), "debug") || !strings.Contains(out.String(), "administrator") {
		t.Fatalf("missing resolved values: %s", out.String())
	}
	root, _ = newRoot()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"config", "check", "--mode", "serve", "--allow-network=false"})
	if err := root.Execute(); err == nil {
		t.Fatal("serve accepted local flag")
	}
}

func TestRemovedSecretFlagRejected(t *testing.T) {
	root, _ := newRoot()
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"serve", "--jwt-secret", "should-not-be-used"})
	if err := root.Execute(); err == nil {
		t.Fatal("removed secret flag accepted")
	}
}

func TestInitWritesCompleteReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "translation.yaml")
	cmd := newInitCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"--path", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"prompts/default_translation.tmpl", "prompts/default_bootstrap.tmpl", "profiles/default.yaml"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), relative)); err != nil {
			t.Fatal(err)
		}
	}
	// A missing main document must not authorize overwriting existing assets.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cmd = newInitCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"--path", path})
	if err := cmd.Execute(); err == nil {
		t.Fatal("existing reference overwritten without --force")
	}
}

func TestInitServerTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	cmd := newInitCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"--kind", "server", "--path", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(string(data), "\r\n", "\n"), "kind: server\nversion: 1") {
		t.Fatal("wrong deployment document")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "linguaflow.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("init created database")
	}
	if fields := config.DeploymentFields(); len(fields) == 0 {
		t.Fatal("deployment field contract missing")
	}
}
