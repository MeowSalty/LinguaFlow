package v015

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
)

func TestS3PathConflictBlocksWholeTargetReadiness(t *testing.T) {
	ctx := t.Context()
	options, _ := fixtureDeployment(t, "serve")
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	source, _, err := snapshot.Open(ctx, &resolved.Config, false)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, statement := range []string{
		"INSERT INTO storage_connections(id,created_at,updated_at,name,driver,backend_id,endpoint) SELECT 80,created_at,updated_at,'original s3','s3','remote','https://s3.example.invalid' FROM users WHERE id=1",
		"INSERT INTO storage_spaces(id,created_at,updated_at,name,identity,marker_nonce,bucket,prefix,connection_id) SELECT 81,created_at,updated_at,'first','original-first','nonce-first','bucket','owned',80 FROM users WHERE id=1",
		"INSERT INTO storage_spaces(id,created_at,updated_at,name,identity,marker_nonce,bucket,prefix,connection_id) SELECT 82,created_at,updated_at,'second','original-second','nonce-second','bucket','owned/nested',80 FROM users WHERE id=1",
	} {
		if _, err := source.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	p, err := NewPlan(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(ctx, RunOptions{Directory: options.Directory, Offline: true})
	var known *operation.Error
	if !errors.As(err, &known) || known.Code != operation.NeedsAction {
		t.Fatal("S3 overlapping claims did not stop the conversion", err)
	}
	if _, err := os.Stat(options.TargetDataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("S3 path conflict published a target")
	}
	_, journal, err := operation.Load(options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.LoadBaseline(options.Directory, p, journal); err != nil {
		t.Fatal(err)
	}
	cfg := resolved.Config
	cfg.DataDir, cfg.Database.DSN = journal.Staging, filepath.Join(journal.Staging, "linguaflow.db")
	target, _, err := snapshot.Open(ctx, &cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	steps, state, err := operation.Steps(ctx, target, p)
	if err != nil || state != "preparing" {
		t.Fatal("S3 conflict lost preparing reservation", err)
	}
	for _, step := range steps {
		if step == "ready" {
			t.Fatal("S3 conflict committed a ready receipt")
		}
	}
	if db, _, err := database.Open(ctx, &cfg); err == nil {
		db.Close()
		t.Fatal("runtime opened S3 conflict target")
	}
	if fp, err := Fingerprint(ctx, source, "sqlite"); err != nil || fp != p.SourceFingerprint {
		t.Fatal("S3 conflict changed source", err)
	}
}
