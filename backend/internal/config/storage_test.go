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
	if c.Initialization.CapacityBytes.Set || c.Initialization.LogicalLimitBytes.Set || c.Disk.MinimumFree != "1%" {
		t.Fatalf("initialization defaults must remain omitted: %+v", c)
	}
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

func TestStorageInitializationQuotaInputs(t *testing.T) {
	for _, key := range []string{"capacity_bytes", "logical_limit_bytes"} {
		for _, value := range []string{"null", "1", "107374182401", "9007199254740991"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				in := deploymentInputs(t)
				withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  storage:\n    initialization:\n      "+key+": "+value+"\n")
				r, err := ResolveServerConfig(in)
				if err != nil {
					t.Fatal(err)
				}
				quota := r.Config.Storage.Initialization.CapacityBytes
				if key == "logical_limit_bytes" {
					quota = r.Config.Storage.Initialization.LogicalLimitBytes
				}
				if !quota.Set || (quota.Value == nil) != (value == "null") {
					t.Fatalf("wrong quota: %+v", quota)
				}
				if f := explainField(t, r, "server.storage.initialization."+key); f.Effect != "initialization only" {
					t.Fatalf("wrong effect: %+v", f)
				}
				in.Environment["LINGUAFLOW_STORAGE_INITIALIZATION_"+strings.ToUpper(key)] = "null"
				r, err = ResolveServerConfig(in)
				if err != nil {
					t.Fatal(err)
				}
				quota = r.Config.Storage.Initialization.CapacityBytes
				if key == "logical_limit_bytes" {
					quota = r.Config.Storage.Initialization.LogicalLimitBytes
				}
				if !quota.Set || quota.Value != nil {
					t.Fatal("explicit environment null did not override file")
				}
			})
		}
		for _, value := range []string{"", "0", "-1", "9007199254740992", "9223372036854775808", "1.5", "true", "\"null\"", "[]", "{}"} {
			in := deploymentInputs(t)
			withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  storage:\n    initialization:\n      "+key+": "+value+"\n")
			if _, err := ResolveServerConfig(in); err == nil {
				t.Fatalf("accepted YAML %s=%q", key, value)
			}
		}
		for _, value := range []string{"", "NULL", "Null", " null", "null ", "0", "-1", "+1", "1.0", "9007199254740992"} {
			in := deploymentInputs(t)
			in.Environment["LINGUAFLOW_STORAGE_INITIALIZATION_"+strings.ToUpper(key)] = value
			if _, err := ResolveServerConfig(in); err == nil {
				t.Fatalf("accepted environment %s=%q", key, value)
			}
		}
	}
}

func TestRemovedStorageQuotaConfiguration(t *testing.T) {
	in := deploymentInputs(t)
	withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  storage:\n    limits:\n      capacity_bytes: 107374182400\n")
	if _, err := ResolveServerConfig(in); err == nil || !strings.Contains(err.Error(), "initialization.capacity_bytes") {
		t.Fatalf("missing replacement guidance: %v", err)
	}
	in.ConfigPath = nil
	in.Environment["LINGUAFLOW_STORAGE_LIMITS_CAPACITY_BYTES"] = ""
	if _, err := ResolveServerConfig(in); err == nil || !strings.Contains(err.Error(), "LINGUAFLOW_STORAGE_INITIALIZATION_CAPACITY_BYTES") {
		t.Fatalf("missing environment guidance: %v", err)
	}
}

func TestStorageDiskThresholdConfiguration(t *testing.T) {
	for _, value := range []string{"1%", "0.5%", "1024"} {
		in := deploymentInputs(t)
		withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  storage:\n    disk:\n      minimum_free: "+value+"\n")
		r, err := ResolveServerConfig(in)
		if err != nil {
			t.Fatal(err)
		}
		if r.Config.Storage.Disk.MinimumFree != value {
			t.Fatal("threshold changed")
		}
	}
	for _, value := range []string{"", "0", "-1", "0%", "100%", "NaN%", "1e2", " 1%"} {
		in := deploymentInputs(t)
		in.Environment["LINGUAFLOW_STORAGE_DISK_MINIMUM_FREE"] = value
		if _, err := ResolveServerConfig(in); err == nil {
			t.Fatalf("accepted threshold %q", value)
		}
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
