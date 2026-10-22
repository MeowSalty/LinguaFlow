package database

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestSQLiteConcurrentOpenAcrossProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	path, err := filepath.Abs(filepath.Join(t.TempDir(), "concurrent.sqlite"))
	if err != nil {
		t.Fatalf("resolve SQLite path: %v", err)
	}
	type process struct {
		cmd             *exec.Cmd
		stdin           io.WriteCloser
		stdout, stderr  bytes.Buffer
		started, waited bool
	}
	processes := make([]process, 4)
	defer func() {
		cancel()
		for i := range processes {
			p := &processes[i]
			if p.stdin != nil {
				_ = p.stdin.Close()
			}
			if p.started && !p.waited {
				_ = p.cmd.Wait()
			}
		}
	}()
	for i := range processes {
		p := &processes[i]
		p.cmd = exec.CommandContext(ctx, executable, "-test.run=^TestSQLiteConcurrentOpenProcessHelper$", "-test.count=1")
		p.cmd.Env = append(os.Environ(), "LINGUAFLOW_SQLITE_OPEN_HELPER="+path)
		p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
		p.stdin, err = p.cmd.StdinPipe()
		if err != nil {
			t.Fatalf("process %d stdin pipe: %v", i, err)
		}
		if err = p.cmd.Start(); err != nil {
			t.Fatalf("process %d start: %v\nstdout:\n%s\nstderr:\n%s", i, err, p.stdout.String(), p.stderr.String())
		}
		p.started = true
	}
	for i := range processes {
		p := &processes[i]
		if _, err = p.stdin.Write([]byte{1}); err != nil {
			t.Errorf("process %d start signal: %v", i, err)
		}
		if err = p.stdin.Close(); err != nil {
			t.Errorf("process %d close stdin: %v", i, err)
		}
	}
	for i := range processes {
		p := &processes[i]
		err = p.cmd.Wait()
		p.waited = true
		if err != nil {
			t.Errorf("process %d wait: %v\nstdout:\n%s\nstderr:\n%s", i, err, p.stdout.String(), p.stderr.String())
		}
	}
	if t.Failed() {
		return
	}
	cfg := config.DefaultServerConfig()
	cfg.DataDir = filepath.Dir(path)
	cfg.Database.DSN = path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	cfg.Database.MaxOpenConns = 1
	db, client, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("parent open: %v", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Errorf("parent close: %v", err)
		}
	}()
	var userVersion int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		t.Fatalf("parent read user_version: %v", err)
	}
	if userVersion != 1 {
		t.Errorf("user_version = %d, want 1", userVersion)
	}
	var count, authVersion int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(MAX(version), 0) FROM linguaflow_auth_format").Scan(&count, &authVersion); err != nil {
		t.Fatalf("parent read auth format: %v", err)
	}
	if count != 1 || authVersion != AuthFormatVersion {
		t.Errorf("auth format count/version = %d/%d, want 1/%d", count, authVersion, AuthFormatVersion)
	}
}

func TestSQLiteConcurrentOpenProcessHelper(t *testing.T) {
	path := os.Getenv("LINGUAFLOW_SQLITE_OPEN_HELPER")
	if path == "" {
		t.Skip("SQLite subprocess helper")
	}
	if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
		t.Fatalf("helper start signal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = filepath.Dir(path)
	cfg.Database.DSN = path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	cfg.Database.MaxOpenConns = 1
	_, client, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("helper open: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("helper close: %v", err)
	}
}
