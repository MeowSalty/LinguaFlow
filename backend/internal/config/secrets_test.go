package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentSecretPresenceAndFiles(t *testing.T) {
	const name = "SECRET"
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	for _, content := range []string{" keep spaces \r\n", "keep-cr\r", "line\n\n"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		value, present, err := EnvironmentSecret(map[string]string{name + "_FILE": "secret"}, name, dir)
		want := strings.TrimSuffix(content, "\n")
		if strings.HasSuffix(content, "\r\n") {
			want = strings.TrimSuffix(content, "\r\n")
		}
		if err != nil || !present || value != want {
			t.Fatalf("file resolution: present=%v err=%v", present, err)
		}
	}
	if _, present, err := EnvironmentSecret(nil, name, dir); present || err != nil {
		t.Fatal("absence not preserved")
	}
	if value, present, err := EnvironmentSecret(map[string]string{name: ""}, name, dir); value != "" || !present || err != nil {
		t.Fatal("explicit empty value not preserved")
	}
	if _, _, err := EnvironmentSecret(map[string]string{name: "", name + "_FILE": "secret"}, name, dir); err == nil {
		t.Fatal("explicit empty direct value must conflict with file")
	}
	if _, _, err := EnvironmentSecret(map[string]string{name + "_FILE": "missing-private-path"}, name, dir); err == nil || strings.Contains(err.Error(), "missing-private-path") {
		t.Fatal("file read errors must be redacted")
	}
}

func TestPrepareLocalSecretPreservesExistingValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance-secret")
	first, err := PrepareLocalSecret(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareLocalSecret(path)
	if err != nil || first != second || len(first) != 64 {
		t.Fatal("persistent secret was not reused")
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareLocalSecret(path); err == nil {
		t.Fatal("invalid secret must not be replaced")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "invalid" {
		t.Fatal("invalid existing secret changed")
	}
}
