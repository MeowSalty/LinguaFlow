package storageguard_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storagebackup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageguard"
	_ "modernc.org/sqlite"
	"testing"
	"time"
)

func fixture(t *testing.T) (*ent.Client, *ent.StorageConnection, *ent.StorageSpace) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	c := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = c.Close() })
	if err = c.Schema.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	conn := c.StorageConnection.Create().SetName("test").SetDriver("s3").SetRemoteUseState("never").SaveX(t.Context())
	sp := c.StorageSpace.Create().SetConnectionID(conn.ID).SetName("source").SetBucket("bucket").SetPrefix("source").SetIdentity("original-identity").SetMarkerNonce("nonce").SetRemoteUseState("never").SaveX(t.Context())
	return c, conn, sp
}

func TestActualReadLifetimeSurvivesLeaseAndExecutionGenerationChanges(t *testing.T) {
	c, conn, sp := fixture(t)
	ctx := t.Context()
	g := storageguard.New()
	if _, _, err := storageguard.Check(ctx, c, sp.ID, storageguard.Read, 0); err != nil {
		t.Fatal(err)
	}
	if c.StorageSpace.GetX(ctx, sp.ID).RemoteUseState != "never" {
		t.Fatal("metadata check recorded a provider attempt")
	}
	release, err := g.Acquire(ctx, c, sp.ID, storageguard.Read, 0, &storageguard.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	if c.StorageSpace.GetX(ctx, sp.ID).RemoteUseState != "attempted" || c.StorageConnection.GetX(ctx, conn.ID).RemoteUseState != "attempted" {
		t.Fatal("missing durable attempt evidence")
	}
	r := c.StorageRetirement.Create().SetTargetKind("space").SetTargetID(sp.ID).SetTargetIdentity(sp.Identity).SetConfirmedScope(json.RawMessage(`{}`)).SetIdempotencyKey("retire").SetRequestHash("hash").SetPhase("finalizing").SetLeaseUntil(time.Now().Add(-time.Hour)).SaveX(ctx)
	c.StorageSpace.UpdateOneID(sp.ID).SetRetirementID(r.ID).SetLifecycle("retiring").AddExecutionGeneration(1).ExecX(ctx)
	if !g.Busy([]int{sp.ID}) {
		t.Fatal("expired durable lease discarded active request")
	}
	if _, err = g.Acquire(ctx, c, sp.ID, storageguard.Read, 0, nil); !errors.Is(err, storageguard.ErrRetiring) {
		t.Fatalf("ordinary read during sealing: %v", err)
	}
	if _, err = g.Acquire(ctx, c, sp.ID, storageguard.Cleanup, r.ID+1, nil); err == nil {
		t.Fatal("foreign finalizer admitted")
	}
	cleanup, err := g.Acquire(ctx, c, sp.ID, storageguard.Cleanup, r.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	release()
	release()
	if g.Busy([]int{sp.ID}) {
		t.Fatal("reader release was not idempotent")
	}
	c.StorageSpace.UpdateOneID(sp.ID).SetLifecycle("removed").ExecX(ctx)
	if _, err = g.Acquire(ctx, c, sp.ID, storageguard.Cleanup, r.ID, nil); !errors.Is(err, storageguard.ErrRemoved) {
		t.Fatalf("removed identity allowed cleanup: %v", err)
	}
}

func TestBackupCaptureRejectsRetiringLocationWithoutPartialManifest(t *testing.T) {
	c, _, sp := fixture(t)
	ctx := context.Background()
	p := c.Project.Create().SetName("backup-source").SaveX(ctx)
	b := c.Blob.Create().SetIdentity("blob").SetProjectID(p.ID).SetPurpose("source").SetStatus("ready").SetSize(1).SetSha256("hash").SaveX(ctx)
	l := c.BlobLocation.Create().SetBlobID(b.ID).SetSpaceID(sp.ID).SetObjectKey("objects/original").SetStatus("live").SetSize(1).SaveX(ctx)
	c.Blob.UpdateOneID(b.ID).SetActiveLocationID(l.ID).ExecX(ctx)
	c.StorageSpace.UpdateOneID(sp.ID).SetLifecycle("retiring").ExecX(ctx)
	_, _, err := storagebackup.CaptureMetadata(ctx, c, time.Now().Add(time.Hour))
	if !errors.Is(err, storageguard.ErrRetiring) {
		t.Fatalf("backup did not share lifecycle gate: %v", err)
	}
	if c.BackupPin.Query().CountX(ctx) != 0 || c.StorageBackup.Query().CountX(ctx) != 0 {
		t.Fatal("rejected backup left partial protection")
	}
}
