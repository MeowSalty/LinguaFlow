package cli

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigTemplateAcceptsMasterKeyWithoutKeyring(t *testing.T) {
	clearDeploymentEnvironment(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	dataDir := filepath.Join(dir, "not-created")
	master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32))
	t.Setenv("LINGUAFLOW_CREDENTIALS_MASTER_KEY", master)
	t.Setenv("LINGUAFLOW_JWT_SECRET", strings.Repeat("j", 32))
	t.Setenv("LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD", "initial-admin-password")
	t.Setenv("LINGUAFLOW_DATA_DIR", dataDir)
	init := newInitCmd()
	init.SetOut(new(bytes.Buffer))
	init.SetArgs([]string{"--kind", "server", "--path", path})
	if err := init.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"check", "explain"} {
		root, _ := newRoot()
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetErr(&output)
		root.SetArgs([]string{"config", action, "--mode", "serve", "--config", path})
		if err := root.Execute(); err != nil {
			t.Fatalf("template with master key failed %s: %v", action, err)
		}
		if strings.Contains(output.String(), master) || strings.Contains(output.String(), "initial-admin-password") {
			t.Fatal("offline diagnostics exposed a secret")
		}
		if action == "explain" && (!strings.Contains(output.String(), "server.credentials.master_key") || !strings.Contains(output.String(), "configured (redacted)")) {
			t.Fatal("master key source not explained")
		}
		if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
			t.Fatal("offline configuration command prepared instance")
		}
	}
}
