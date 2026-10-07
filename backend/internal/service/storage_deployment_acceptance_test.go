package service

import (
	"bytes"
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageacceptance"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/localstore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/s3store"
	"github.com/aws/aws-sdk-go-v2/aws"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStorageDeploymentPostgresCapacityAndLeaseConcurrency(t *testing.T) {
	client := storageacceptance.Postgres(t)
	ctx := context.Background()
	driver, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	u := client.User.Create().SetUsername("postgres-storage").SetEmail("postgres-storage@example.test").SetPasswordHash("unused").SaveX(ctx)
	projects := NewProjectService(client, NewUserService(client, nil))
	s, err := NewStorageService(client, projects, t.TempDir())
	if s != nil {
		if initErr := s.EnsureStoragePolicy(context.Background(), true, false, nil, false, nil); initErr != nil {
			t.Fatal(initErr)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	sp, err := s.InstallSiteSpace(ctx, "postgres-local", driver)
	if err != nil {
		t.Fatal(err)
	}
	s.Configure(config.DefaultStorageConfig(), "postgres", sp.ID)
	p, err := projects.CreateProject(ctx, u.ID, CreateProjectInput{Name: "postgres-storage"})
	if err != nil {
		t.Fatal(err)
	}
	client.StorageSpace.UpdateOneID(sp.ID).SetCapacityBytes(50).ExecX(ctx)
	tasks := make([]*ent.StorageTask, 2)
	for i, name := range []string{"a.txt", "b.txt"} {
		tasks[i], err = s.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", IdempotencyKey: name, Path: name, Size: 40})
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func(task *ent.StorageTask) {
			defer wg.Done()
			<-start
			stage, e := s.Stage(ctx, task, strings.NewReader(strings.Repeat("a", 40)), 40)
			if stage != nil {
				_ = stage.Close()
			}
			results <- e
		}(task)
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, storage.ErrLimit) && !errors.Is(e, ErrStorageConflict) {
			t.Fatalf("unexpected reservation error: %v", e)
		}
	}
	space := client.StorageSpace.GetX(ctx, sp.ID)
	if success != 1 || space.ReservedBytes+space.CandidateBytes+space.LiveBytes+space.PendingDeleteBytes != 40 {
		t.Fatal("PostgreSQL reservation CAS overbooked or lost capacity")
	}
	release, err := s.ClaimTaskExecution(ctx, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimTaskExecution(ctx, tasks[0].ID); !errors.Is(err, ErrStorageInProgress) {
		t.Fatal("PostgreSQL admitted overlapping task owners")
	}
	release()
	release, err = s.ClaimTaskExecution(ctx, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

type deploymentTrackingDriver struct {
	*s3store.Store
	tracker *storageacceptance.S3
}

func (d *deploymentTrackingDriver) PutNew(ctx context.Context, key string, r io.Reader, n int64) (storage.Object, error) {
	d.tracker.Track(key)
	return d.Store.PutNew(ctx, key, r, n)
}

func TestStorageDeploymentS3AuthorizationRevocation(t *testing.T) {
	remote := storageacceptance.NewS3(t)
	ctx := context.Background()
	client := testClient(t)
	u := client.User.Create().SetUsername("s3-deployment").SetEmail("s3-deployment@example.test").SetPasswordHash("unused").SaveX(ctx)
	cfg := config.DefaultStorageConfig()
	cfg.Enabled = true
	s := NewStorageConnectionService(client, credentialTestKeyring(t, "one", "one"), cfg, nil)
	endpoint := remote.Options.Endpoint
	if endpoint == "" {
		endpoint = "https://s3." + remote.Options.Region + ".amazonaws.com"
	}
	c, err := s.Create(ctx, u.ID, CreateStorageConnectionInput{Name: "S3 acceptance", Scope: "user", OwnerID: u.ID, Endpoint: endpoint, Region: remote.Options.Region, PathStyle: remote.Options.PathStyle})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := s.CreateSpace(ctx, u.ID, c.ID, CreateStorageSpaceInput{Name: "S3 test", Bucket: remote.Options.Bucket, Prefix: remote.Options.Prefix})
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.Get(ctx, u.ID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.factory = func(_ context.Context, _ *ent.StorageConnection, _ *ent.StorageSpace, p storageauth.S3Payload) (storage.Driver, error) {
		opts := remote.Options
		opts.Credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: p.AccessKeyID, SecretAccessKey: p.SecretAccessKey, SessionToken: p.SessionToken}, nil
		})
		d, e := s3store.New(opts)
		if e != nil {
			return nil, e
		}
		return &deploymentTrackingDriver{d, remote}, nil
	}
	credentials, err := remote.Options.Credentials.Retrieve(ctx)
	if err != nil {
		t.Fatal("test credentials unavailable")
	}
	in := AuthorizeStorageInput{Payload: storageauth.S3Payload{Version: 1, AccessKeyID: credentials.AccessKeyID, SecretAccessKey: credentials.SecretAccessKey, SessionToken: credentials.SessionToken}, WriteCheck: true, ExpectedManagementGeneration: c.ManagementGeneration}
	active, check, err := s.AuthorizeWithCheck(ctx, u.ID, c.ID, in)
	if err != nil {
		t.Fatalf("real S3 admission failed: %v", err)
	}
	if !check.AuthorizationActivated || check.CleanupStatus != "done" {
		t.Fatal("admission did not verify exact probe deletion")
	}
	guarded, err := s.ResolveDriver(ctx, sp.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Revoke(ctx, u.ID, c.ID, active.ManagementGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err = guarded.Stat(ctx, storage.Object{Key: storageMarkerKey}); !errors.Is(err, storage.ErrAuthRequired) {
		t.Fatal("existing S3 driver bypassed revoked app authorization")
	}
}

type deploymentDeleteGate struct {
	*storageacceptance.S3
	blocked bool
}

func (d *deploymentDeleteGate) Delete(ctx context.Context, o storage.Object) error {
	if d.blocked {
		return storage.ErrPermission
	}
	return d.S3.Delete(ctx, o)
}

func TestStorageDeploymentS3ReadonlyMigrationCutoverAndCleanup(t *testing.T) {
	remote := storageacceptance.NewS3(t)
	ctx, client, r, p, u, _ := storageLifecycleFixture(t)
	s := r.storage
	s.deleteGrace = 0
	s.cfg.ReconcileBatchSize = 1
	local, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	s.RegisterDriver(*p.StorageSpaceID, local)
	first := storageUpload(t, ctx, r, p, u, "one.txt", "one\n")
	storageUpload(t, ctx, r, p, u, "two.txt", "two\n")
	gate := &deploymentDeleteGate{S3: remote}
	target, err := s.InstallSiteSpace(ctx, "acceptance-s3", gate)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := remote.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client.StorageSpace.UpdateOneID(target.ID).SetVersioned(caps.Versioned).ExecX(ctx)
	task, err := s.StartMigration(ctx, u.ID, p.ID, target.ID, p.StorageGeneration, "real-s3-move")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10 && client.StorageTask.GetX(ctx, task.ID).Phase != "cutover"; i++ {
		if err = s.ContinueMigration(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	if client.StorageTask.GetX(ctx, task.ID).Phase != "cutover" {
		t.Fatal("did not persist partial cutover")
	}
	// Reconstruct service state at the durable cutover boundary, without in-memory locks.
	resumed, err := NewStorageService(client, s.projects, t.TempDir())
	if resumed != nil {
		if initErr := resumed.EnsureStoragePolicy(context.Background(), true, false, nil, false, nil); initErr != nil {
			t.Fatal(initErr)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	resumed.Configure(s.cfg, "sqlite", *p.StorageSpaceID)
	resumed.deleteGrace = 0
	resumed.RegisterDriver(*p.StorageSpaceID, local)
	resumed.RegisterDriver(target.ID, gate)
	r.SetStorage(resumed)
	s = resumed
	for i := 0; i < 10 && client.StorageTask.GetX(ctx, task.ID).Status != storagetask.StatusCompleted; i++ {
		if err = s.ContinueMigration(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	current := client.Project.GetX(ctx, p.ID)
	if current.StorageState != "active" || *current.StorageSpaceID != target.ID {
		t.Fatal("cutover restart did not publish target binding")
	}
	f, err := r.OriginalFile(ctx, u.ID, p.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || !bytes.Equal(data, []byte("one\n")) {
		t.Fatal("S3 migration changed original bytes")
	}
	client.StorageSpace.UpdateOneID(target.ID).SetStatus("read_only").ExecX(ctx)
	if _, err = s.driver(ctx, target.ID, true); !errors.Is(err, ErrStorageMaintenance) {
		t.Fatal("read-only source remained writable")
	}
	destination, err := s.InstallSiteSpace(ctx, "acceptance-return", local)
	if err != nil {
		t.Fatal(err)
	}
	back, err := s.StartMigration(ctx, u.ID, p.ID, destination.ID, current.StorageGeneration, "real-s3-return")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10 && client.StorageTask.GetX(ctx, back.ID).Status != storagetask.StatusCompleted; i++ {
		if err = s.ContinueMigration(ctx, back.ID); err != nil {
			t.Fatal(err)
		}
	}
	if client.StorageTask.GetX(ctx, back.ID).Status != storagetask.StatusCompleted {
		t.Fatal("read-only source could not migrate out")
	}
	gate.blocked = true
	client.BlobLocation.Update().Where(bloblocation.SpaceIDEQ(target.ID), bloblocation.StatusEQ(bloblocation.StatusRetired)).SetRetainUntil(time.Now().Add(-time.Hour)).ExecX(ctx)
	client.DeletionEntry.Update().SetNotBefore(time.Now().Add(-time.Hour)).ExecX(ctx)
	for i := 0; i < 10; i++ {
		if err = s.collect(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if client.StorageSpace.GetX(ctx, target.ID).PendingDeleteBytes == 0 || !client.DeletionEntry.Query().Where(deletionentry.StatusEQ(deletionentry.StatusBlocked)).ExistX(ctx) {
		t.Fatal("failed S3 cleanup freed pending capacity")
	}
	gate.blocked = false
	client.DeletionEntry.Update().ClearNextRetryAt().ExecX(ctx)
	for i := 0; i < 10; i++ {
		if err = s.collect(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if client.StorageSpace.GetX(ctx, target.ID).PendingDeleteBytes != 0 {
		t.Fatal("confirmed exact S3 cleanup did not settle quota")
	}
}
