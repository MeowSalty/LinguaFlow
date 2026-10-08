package migrationcli

import (
	"bytes"
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
)

func TestMigrateV013LocalCommandAndStartup(t *testing.T) {
	clearDeploymentEnvironment(t)
	dir := filepath.Join(t.TempDir(), "local")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "linguaflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../migration/v013/testdata/v013-sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,created_at,updated_at,username,password_hash,email,role,active) VALUES(1,'2026-09-29 11:00:00.123456789 +0800 CST','2026-09-29 11:00:00.123456789 +0800 CST','local','preserved-old-hash','local@linguaflow.local','admin',true)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO projects(id,created_at,updated_at,name,config,owner_user_id) VALUES(1,$1,$1,'old-project','{}',1)`,
		`INSERT INTO backends(id,created_at,updated_at,name,scope,backend_type,options,owner_user_id) VALUES(1,$1,$1,'old-backend','user','openai','{"model":"old-model","api_key":"backend-original-key"}',1)`,
	} {
		if _, err := db.Exec(statement, "2026-09-29 11:00:00.123456789 +0800 CST"); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	seedLegacyRunnableJob(t, db, false)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	resourceDir := filepath.Join(dir, "jobs", "resources")
	if err := os.MkdirAll(resourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resourceDir, "migration.txt"), []byte("old source\nremaining source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := NewCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"v013", "local", "--data-dir", dir, "--apply"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Original directory backup:") || !strings.Contains(output.String(), "Migration published") {
		t.Fatalf("missing publication or backup output: %s", output.String())
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	assertMigratedServiceStarts(t, dir, port, []string{"LINGUAFLOW_MIGRATION_STARTUP_MODE=local"})
	cfg := config.DefaultServerConfig()
	cfg.DataDir, cfg.AutoMigrate = dir, false
	_, client, err := database.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	keys, err := credential.LoadKeyring(filepath.Join(dir, "credentials-keyring.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertMigratedJobReadableAndResumable(t, client, keys)
}
