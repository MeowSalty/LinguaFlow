package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageDefaultsAndOfflineDiagnostics(t *testing.T) {
	in := deploymentInputs(t)
	in.Environment["LINGUAFLOW_DATA_DIR"] = filepath.Join(in.WorkingDirectory, "uncreated")
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Config.Storage
	if c.Enabled || c.Maintenance || c.Limits.MaxFileBytes != 100<<20 || c.Limits.MaxTempBytes != 4<<30 || c.Limits.MaxCacheBytes != 0 || c.RetryMaxAttempts != 8 || c.IntentTTL != 24*time.Hour {
		t.Fatalf("incorrect storage defaults: %+v", c)
	}
	if c.WorkDir != filepath.Join(r.Config.DataDir, "tmp") || c.CacheDir != filepath.Join(r.Config.DataDir, "cache") {
		t.Fatal("directories not derived")
	}
	if _, err := os.Stat(r.Config.DataDir); !os.IsNotExist(err) {
		t.Fatal("diagnostic created data directory")
	}
	if explainField(t, r, "server.storage.work_dir").Source != "derived from data_dir" {
		t.Fatal("missing derived source")
	}
}

func TestStorageBackendsStrictAndRedacted(t *testing.T) {
	in := deploymentInputs(t)
	withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  storage:\n    default_site_space: remote\n    backends:\n      - id: remote\n        driver: s3\n        endpoint: https://does-not-resolve.invalid\n        region: test\n        bucket: files\n        access_key_id: top-secret-access\n        secret_access_key: top-secret-material\n    limits:\n      max_temp_bytes: 8589934592\n")
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Storage.Limits.MaxTempBytes != 8<<30 {
		t.Fatal("64-bit capacity was truncated")
	}
	f := explainField(t, r, "server.storage.backends")
	if !f.Sensitive || f.Value != "configured (redacted)" {
		t.Fatalf("backend secrets not redacted: %+v", f)
	}
	for _, f := range r.Fields {
		if strings.Contains(f.Value, "top-secret") {
			t.Fatal("secret appeared in diagnostics")
		}
	}
	for _, body := range []string{
		"    typo: true\n",
		"    backends:\n      - id: remote\n        unexpected: top-secret-material\n",
		"    backends:\n      - id: remote\n        id: repeated\n",
		"    backends:\n      - id: remote\n        path_style: yes\n",
		"    limits:\n      max_file_bytes: 0\n",
		"    transfer_timeout: 2h\n",
		"    network:\n      allowed_cidrs: [bad-cidr]\n",
	} {
		withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  storage:\n"+body)
		if _, err := ResolveServerConfig(in); err == nil || strings.Contains(err.Error(), "top-secret") {
			t.Fatalf("invalid storage configuration accepted/leaked: %v", err)
		}
	}
}

func TestStorageBackendEnvironmentAndRelativeRoot(t *testing.T) {
	in := deploymentInputs(t)
	withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  storage:\n    backends:\n      - id: local\n        driver: local\n        root: file-objects\n")
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Storage.Backends[0].Root != filepath.Join(in.WorkingDirectory, "file-objects") {
		t.Fatal("relative backend root wrong")
	}
	in.Environment["LINGUAFLOW_STORAGE_BACKENDS"] = `[{"id":"local","driver":"local","root":"env-objects"}]`
	r, err = ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Storage.Backends[0].Root != filepath.Join(in.WorkingDirectory, "env-objects") {
		t.Fatal("environment backend root wrong")
	}
	if !strings.HasPrefix(explainField(t, r, "server.storage.backends").Source, "env:") {
		t.Fatal("environment source missing")
	}
	if _, err := os.Stat(r.Config.Storage.Backends[0].Root); !os.IsNotExist(err) {
		t.Fatal("diagnostic created backend root")
	}
}
