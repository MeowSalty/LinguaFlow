package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func localResolution(t *testing.T) *config.ResolvedServer {
	t.Helper()
	dir := t.TempDir()
	r, err := config.ResolveServerConfig(config.ServerInputs{Mode: config.ModeLocal, WorkingDirectory: dir, UserConfigDir: dir, Environment: map[string]string{
		"LINGUAFLOW_DATA_DIR": filepath.Join(dir, "instance"), "LINGUAFLOW_PORT": "0",
		"LINGUAFLOW_DATABASE_DRIVER": "postgres", "LINGUAFLOW_DATABASE_DSN": "postgres://localhost/ignored",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestBootstrapServerLocalPreservesResolution(t *testing.T) {
	r := localResolution(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, ln, cleanup, err := bootstrapServer(context.Background(), BootOptions{Resolved: r, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	if server == nil {
		t.Fatal("missing server")
	}
	defer func() { _ = cleanup() }()
	defer func() { _ = ln.Close() }()
	if r.Config.Port != 0 || r.Config.JWTSecret != "" {
		t.Fatal("startup mutated read-only resolution")
	}
	if ln.Addr().(*net.TCPAddr).Port == 0 {
		t.Fatal("actual port not allocated")
	}
	if _, err := os.Stat(filepath.Join(r.Config.DataDir, "linguaflow.db")); err != nil {
		t.Fatal(err)
	}
	first, err := config.ReadLocalSecret(r.LocalSecretPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareLocalSecret(r.LocalSecretPath)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatal("persistent local secret changed")
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup unstarted server: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("repeated cleanup: %v", err)
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("cleanup left listener open: %v", err)
	}
}

func TestPrepareLocalSecretAtomicConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance-secret")
	const count = 8
	var wg sync.WaitGroup
	results := make(chan string, count)
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); secret, err := prepareLocalSecret(path); results <- secret; errs <- err }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first string
	for value := range results {
		if first == "" {
			first = value
		}
		if value != first {
			t.Fatal("concurrent startup used different secrets")
		}
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLocalSecret(path); err == nil {
		t.Fatal("corrupt secret was regenerated")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "corrupt" {
		t.Fatal("corrupt secret was overwritten")
	}
}

func TestBindListenerKeepsRequestedPort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	requested := occupied.Addr().(*net.TCPAddr).Port
	if requested > 65526 {
		t.Skip("allocated port leaves no sequential candidate range")
	}
	cfg := config.DefaultServerConfig()
	cfg.Mode = config.ModeLocal
	cfg.Host = "127.0.0.1"
	cfg.Port = requested
	ln, err := bindListener(context.Background(), cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if cfg.Port != requested || ln.Addr().(*net.TCPAddr).Port <= requested {
		t.Fatal("request port mutated or occupied port reused")
	}
}
func TestBindListenerIPv6AndBrowserURL(t *testing.T) {
	cfg := config.DefaultServerConfig()
	cfg.Mode = config.ModeLocal
	cfg.Host = "::1"
	cfg.Port = 0
	ln, err := bindListener(context.Background(), cfg, false)
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer ln.Close()
	if got := browserURL(ln.Addr().(*net.TCPAddr)); !strings.HasPrefix(got, "http://[::1]:") {
		t.Fatalf("browser URL=%s", got)
	}
}

func TestLocalOfflineMissingAndCorruptSecrets(t *testing.T) {
	r := localResolution(t)
	if _, err := os.Stat(r.Config.DataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("resolution wrote data directory")
	}
	if err := os.MkdirAll(r.Config.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.LocalSecretPath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.ResolveServerConfig(config.ServerInputs{Mode: config.ModeLocal, WorkingDirectory: r.Config.DataDir, UserConfigDir: r.Config.DataDir, Environment: map[string]string{"LINGUAFLOW_DATA_DIR": r.Config.DataDir}})
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("corrupt secret accepted: %v", err)
	}
}
