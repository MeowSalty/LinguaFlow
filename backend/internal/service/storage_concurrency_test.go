package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagemigrationitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagereservation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func TestStorageConcurrencyActiveTransferSurvivesCleanupAndCancellation(t *testing.T) {
	ctx, c, r, p, u, d := storageLifecycleFixture(t)
	s := r.storage
	task, err := s.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", Path: "one.txt", Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	d.beforePut = func(string) { close(entered); <-release }
	result := make(chan error, 1)
	go func() {
		f, e := s.Stage(ctx, task, bytes.NewBufferString("one"), 3)
		if f != nil {
			_ = f.Close()
		}
		result <- e
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("transfer did not start")
	}
	if err = s.Reconcile(ctx); err != nil {
		close(release)
		t.Fatal(err)
	}
	w := c.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID)).OnlyX(ctx)
	if w.Phase == "cleaned" || d.deletes != 0 {
		close(release)
		t.Fatal("cleaner touched active transfer")
	}
	if _, err = s.Cancel(ctx, u.ID, p.ID, task.ID); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err = <-result; !errors.Is(err, ErrStorageCancelled) {
		t.Fatalf("cancelled transfer: %v", err)
	}
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	sp := c.StorageSpace.GetX(ctx, *p.StorageSpaceID)
	if sp.ReservedBytes != 0 || sp.CandidateBytes != 0 || sp.PendingDeleteBytes != 0 {
		t.Fatalf("leaked accounting: %+v", sp)
	}
	if c.StorageTask.GetX(ctx, task.ID).Status != storagetask.StatusCancelled {
		t.Fatal("cancelled task revived")
	}
}

func TestStorageConcurrencyPreparedWriteIsRecoverableAndCleanupClaimExcludesPublish(t *testing.T) {
	ctx, c, r, p, u, _ := storageLifecycleFixture(t)
	s := r.storage
	task, err := s.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", Path: "one.txt", Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Stage(ctx, task, bytes.NewBufferString("one"), 3)
	if err != nil {
		t.Fatal(err)
	}
	w := f.Write
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if c.StorageWrite.GetX(ctx, w.ID).Phase != "prepared" {
		t.Fatal("durable retry input was collected")
	}
	c.StorageTask.UpdateOneID(task.ID).SetStatus(storagetask.StatusFailed).ExecX(ctx)
	claimed, err := s.claimCleanup(ctx, w.ID)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	err = withOrganizationTransaction(ctx, c, func(tx *ent.Client) error { _, e := s.publish(ctx, tx, w, blob.PurposeSource, nil); return e })
	if !errors.Is(err, ErrStorageConflict) {
		t.Fatalf("published after cleanup claim: %v", err)
	}
	// 模拟在持久化认领之后、提供方删除之前发生重启。
	s.mu.Lock()
	delete(s.inFlight, w.ID)
	s.mu.Unlock()
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if c.StorageWrite.GetX(ctx, w.ID).Phase != "cleaned" {
		t.Fatal("claimed deletion did not recover")
	}
	if c.Blob.Query().CountX(ctx) != 0 {
		t.Fatal("failed publication created a blob")
	}
}

type exactVersionTestDriver struct {
	*connectionTestDriver
	versions     map[string]storage.Object
	lostResponse bool
	bareDeletes  int
}

func (d *exactVersionTestDriver) PutNew(ctx context.Context, key string, r io.Reader, n int64) (storage.Object, error) {
	o, e := d.connectionTestDriver.PutNew(ctx, key, r, n)
	if e != nil {
		return o, e
	}
	o.Version = "v1"
	d.versions[o.Version] = o
	if d.lostResponse {
		d.versions["marker"] = storage.Object{Key: key, Version: "marker", DeleteMarker: true}
		return storage.Object{}, storage.ErrUnavailable
	}
	return o, nil
}
func (d *exactVersionTestDriver) Versions(_ context.Context, key string) ([]storage.Object, error) {
	out := []storage.Object{}
	for _, o := range d.versions {
		if o.Key == key {
			out = append(out, o)
		}
	}
	return out, nil
}
func (d *exactVersionTestDriver) Delete(ctx context.Context, o storage.Object) error {
	if o.Version == "" {
		d.bareDeletes++
		return storage.ErrUnsupported
	}
	delete(d.versions, o.Version)
	if len(d.versions) == 0 {
		return d.connectionTestDriver.Delete(ctx, o)
	}
	return nil
}
func (d *exactVersionTestDriver) Stat(_ context.Context, o storage.Object) (storage.Object, error) {
	if found, ok := d.versions[o.Version]; ok {
		return found, nil
	}
	return storage.Object{}, storage.ErrNotFound
}

func TestStorageConcurrencyUnknownVersionDeletesExactVersionsAndMarkers(t *testing.T) {
	ctx, c, r, p, u, base := storageLifecycleFixture(t)
	s := r.storage
	d := &exactVersionTestDriver{connectionTestDriver: base, versions: map[string]storage.Object{}, lostResponse: true}
	s.RegisterDriver(*p.StorageSpaceID, d)
	task, e := s.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", Path: "one.txt", Size: 3})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Stage(ctx, task, bytes.NewBufferString("one"), 3); !errors.Is(e, storage.ErrUnavailable) {
		t.Fatalf("lost response: %v", e)
	}
	if e = s.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if len(d.versions) != 0 || d.bareDeletes != 0 {
		t.Fatal("version cleanup used an unversioned delete or leaked versions")
	}
	w := c.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID)).OnlyX(ctx)
	res := c.StorageReservation.Query().Where(storagereservation.WriteIDEQ(w.ID)).OnlyX(ctx)
	if w.Phase != "cleaned" || res.State != storagereservation.StateFreed {
		t.Fatal("confirmed version cleanup did not free reservation")
	}
}

type unconfirmedDeleteDriver struct {
	*connectionTestDriver
	ignoreDelete bool
}

func (d *unconfirmedDeleteDriver) Delete(ctx context.Context, o storage.Object) error {
	if d.ignoreDelete {
		return nil
	}
	return d.connectionTestDriver.Delete(ctx, o)
}

func TestStorageConcurrencyCollectorRequiresConfirmedAbsenceAndRetention(t *testing.T) {
	ctx, c, r, p, u, base := storageLifecycleFixture(t)
	s := r.storage
	resource := storageUpload(t, ctx, r, p, u, "one.txt", "one")
	rev := c.SourceRevision.GetX(ctx, *resource.CurrentSourceRevisionID)
	b := c.Blob.GetX(ctx, rev.SourceBlobID)
	old := c.BlobLocation.GetX(ctx, *b.ActiveLocationID)
	if _, e := r.RepairSource(ctx, u.ID, p.ID, resource.ID, rev.ID, old.SpaceID, b.LocationGeneration, UploadedFile{Size: 3, Reader: bytes.NewBufferString("one")}); e != nil {
		t.Fatal(e)
	}
	entry := c.DeletionEntry.Query().Where(deletionentry.LocationIDEQ(old.ID)).OnlyX(ctx)
	c.DeletionEntry.UpdateOneID(entry.ID).SetNotBefore(time.Now().Add(-time.Hour)).ExecX(ctx)
	if e := s.collect(ctx); e != nil {
		t.Fatal(e)
	}
	if c.BlobLocation.GetX(ctx, old.ID).Status != bloblocation.StatusRetired {
		t.Fatal("collector ignored location retention")
	}
	c.BlobLocation.UpdateOneID(old.ID).SetRetainUntil(time.Now().Add(-time.Hour)).ExecX(ctx)
	d := &unconfirmedDeleteDriver{connectionTestDriver: base, ignoreDelete: true}
	s.RegisterDriver(old.SpaceID, d)
	if e := s.collect(ctx); e != nil {
		t.Fatal(e)
	}
	if c.DeletionEntry.GetX(ctx, entry.ID).Status != deletionentry.StatusBlocked || c.StorageSpace.GetX(ctx, old.SpaceID).PendingDeleteBytes != 3 {
		t.Fatal("unconfirmed deletion freed bytes")
	}
	d.ignoreDelete = false
	c.DeletionEntry.UpdateOneID(entry.ID).ClearNextRetryAt().ExecX(ctx)
	if e := s.collect(ctx); e != nil {
		t.Fatal(e)
	}
	if e := s.collect(ctx); e != nil {
		t.Fatal(e)
	}
	sp := c.StorageSpace.GetX(ctx, old.SpaceID)
	if sp.PendingDeleteBytes != 0 || sp.LiveBytes != 3 {
		t.Fatalf("incorrect cleanup accounting: %+v", sp)
	}
}

func TestStorageConcurrencyMigrationBatchesSurviveReconcileAndRejectLateCancel(t *testing.T) {
	ctx, c, r, p, u, _ := storageLifecycleFixture(t)
	s := r.storage
	storageUpload(t, ctx, r, p, u, "one.txt", "one")
	storageUpload(t, ctx, r, p, u, "two.txt", "two")
	target, e := s.InstallSiteSpace(ctx, "target", &connectionTestDriver{objects: map[string][]byte{}})
	if e != nil {
		t.Fatal(e)
	}
	task, e := s.StartMigration(ctx, u.ID, p.ID, target.ID, p.StorageGeneration, "move")
	if e != nil {
		t.Fatal(e)
	}
	same, e := s.StartMigration(ctx, u.ID, p.ID, target.ID, p.StorageGeneration, "move")
	if e != nil || same.ID != task.ID {
		t.Fatalf("idempotent migration: %v", e)
	}
	if _, e = s.StartMigration(ctx, u.ID, p.ID, target.ID, p.StorageGeneration+1, "move"); !errors.Is(e, ErrStorageIdempotency) {
		t.Fatalf("changed generation reused key: %v", e)
	}
	s.cfg.ReconcileBatchSize = 1
	if e = s.ContinueMigration(ctx, task.ID); e != nil {
		t.Fatal(e)
	}
	if c.StorageTask.GetX(ctx, task.ID).Phase != "copy" {
		t.Fatal("transfer overwrote migration orchestration phase")
	}
	if e = s.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.ContinueMigration(ctx, task.ID); e != nil {
		t.Fatal(e)
	}
	if c.StorageTask.GetX(ctx, task.ID).Phase != "cutover" {
		t.Fatal("expected partial cutover")
	}
	if _, e = s.Cancel(ctx, u.ID, p.ID, task.ID); !errors.Is(e, ErrStorageConflict) {
		t.Fatalf("cancelled irreversible cutover: %v", e)
	}
	if e = s.ContinueMigration(ctx, task.ID); e != nil {
		t.Fatal(e)
	}
	after := c.Project.GetX(ctx, p.ID)
	if after.StorageState != "active" || *after.StorageSpaceID != target.ID || c.StorageTask.GetX(ctx, task.ID).Status != storagetask.StatusCompleted {
		t.Fatal("migration did not finish")
	}
}

func TestStorageConcurrencyMigrationCancelUnlocksBeforeFailedCleanup(t *testing.T) {
	ctx, c, r, p, u, _ := storageLifecycleFixture(t)
	s := r.storage
	storageUpload(t, ctx, r, p, u, "one.txt", "one")
	storageUpload(t, ctx, r, p, u, "two.txt", "two")
	d := &connectionTestDriver{objects: map[string][]byte{}, failDeleteResponse: true}
	target, e := s.InstallSiteSpace(ctx, "target", d)
	if e != nil {
		t.Fatal(e)
	}
	task, e := s.StartMigration(ctx, u.ID, p.ID, target.ID, p.StorageGeneration, "move")
	if e != nil {
		t.Fatal(e)
	}
	s.cfg.ReconcileBatchSize = 1
	if e = s.ContinueMigration(ctx, task.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Cancel(ctx, u.ID, p.ID, task.ID); e != nil {
		t.Fatal(e)
	}
	if c.Project.GetX(ctx, p.ID).StorageState != "active" {
		t.Fatal("cancel left project locked")
	}
	if e = s.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if c.StorageSpace.GetX(ctx, target.ID).CandidateBytes != 3 {
		t.Fatal("failed cleanup released charge")
	}
	if e = s.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if c.StorageSpace.GetX(ctx, target.ID).CandidateBytes != 0 {
		t.Fatal("cleanup retry leaked candidate charge")
	}
	loc := c.BlobLocation.Query().Where(bloblocation.SpaceIDEQ(target.ID)).OnlyX(ctx)
	if loc.Status != bloblocation.StatusDeleted {
		t.Fatal("cleaned candidate still eligible for location GC")
	}
}

func TestStorageConcurrencyMigrationResumesPreparedOrphan(t *testing.T) {
	ctx, c, r, p, u, _ := storageLifecycleFixture(t)
	s := r.storage
	storageUpload(t, ctx, r, p, u, "one.txt", "one")
	storageUpload(t, ctx, r, p, u, "two.txt", "two")
	d := &connectionTestDriver{objects: map[string][]byte{}}
	target, e := s.InstallSiteSpace(ctx, "target", d)
	if e != nil {
		t.Fatal(e)
	}
	task, e := s.StartMigration(ctx, u.ID, p.ID, target.ID, p.StorageGeneration, "move")
	if e != nil {
		t.Fatal(e)
	}
	s.cfg.ReconcileBatchSize = 1
	if e = s.ContinueMigration(ctx, task.ID); e != nil {
		t.Fatal(e)
	}
	item := c.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(task.ID), storagemigrationitem.StatusEQ(storagemigrationitem.StatusVerified)).OnlyX(ctx)
	w := c.StorageWrite.Query().Where(storagewrite.LocationIDEQ(*item.TargetLocationID)).OnlyX(ctx)
	// 在 Stage 之后、清单链接之前，立即恢复持久化状态。
	c.StorageMigrationItem.UpdateOneID(item.ID).ClearTargetLocationID().SetStatus(storagemigrationitem.StatusPending).ExecX(ctx)
	c.StorageWrite.UpdateOneID(w.ID).ClearLocationID().ExecX(ctx)
	c.BlobLocation.DeleteOneID(*item.TargetLocationID).ExecX(ctx)
	if e = s.ContinueMigration(ctx, task.ID); e != nil {
		t.Fatal(e)
	}
	if d.puts != 1 {
		t.Fatalf("prepared orphan caused duplicate transfer: %d puts", d.puts)
	}
}

func TestStorageConcurrencyRepairCoordinatesMigrationManifest(t *testing.T) {
	ctx, c, r, p, u, _ := storageLifecycleFixture(t)
	s := r.storage
	first := storageUpload(t, ctx, r, p, u, "one.txt", "one")
	storageUpload(t, ctx, r, p, u, "two.txt", "two")
	target, e := s.InstallSiteSpace(ctx, "target", &connectionTestDriver{objects: map[string][]byte{}})
	if e != nil {
		t.Fatal(e)
	}
	task, e := s.StartMigration(ctx, u.ID, p.ID, target.ID, p.StorageGeneration, "move")
	if e != nil {
		t.Fatal(e)
	}
	s.cfg.ReconcileBatchSize = 1
	if e = s.ContinueMigration(ctx, task.ID); e != nil {
		t.Fatal(e)
	}
	rev := c.SourceRevision.GetX(ctx, *first.CurrentSourceRevisionID)
	b := c.Blob.GetX(ctx, rev.SourceBlobID)
	if _, e = r.RepairSource(ctx, u.ID, p.ID, first.ID, rev.ID, target.ID, b.LocationGeneration, UploadedFile{Size: 3, Reader: bytes.NewBufferString("one")}); e != nil {
		t.Fatal(e)
	}
	item := c.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(task.ID), storagemigrationitem.BlobIDEQ(b.ID)).OnlyX(ctx)
	if item.Status != storagemigrationitem.StatusCommitted {
		t.Fatal("repair did not update fixed manifest")
	}
	if e = s.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if c.StorageTask.GetX(ctx, task.ID).Status != storagetask.StatusRunning {
		t.Fatal("superseded candidate cleanup interrupted migration")
	}
	if e = s.ContinueMigration(ctx, task.ID); e != nil {
		t.Fatal(e)
	}
	if c.StorageTask.GetX(ctx, task.ID).Status != storagetask.StatusCompleted {
		t.Fatal("repair prevented migration completion")
	}
}

func TestStorageConcurrencyIntentChecksGenerationAndOwnership(t *testing.T) {
	ctx, c, r, p, u, _ := storageLifecycleFixture(t)
	s := r.storage
	res := storageUpload(t, ctx, r, p, u, "one.txt", "one")
	other, e := s.projects.CreateProject(ctx, u.ID, CreateProjectInput{Name: "other"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Begin(ctx, u.ID, other.ID, StorageIntent{Kind: "repair", ResourceID: res.ID, SourceRevisionID: *res.CurrentSourceRevisionID, Size: 3}); e == nil {
		t.Fatal("accepted foreign resource and revision")
	}
	c.Project.UpdateOneID(p.ID).AddStorageGeneration(1).ExecX(ctx)
	if _, e = s.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", Size: 3, RequireStorageGeneration: true, StorageGeneration: 0}); !errors.Is(e, ErrStorageConflict) {
		t.Fatalf("stale explicit zero generation: %v", e)
	}
}

func TestStorageConcurrencyOutputLimitPreservesExportPhase(t *testing.T) {
	ctx, c, r, p, u, _ := storageLifecycleFixture(t)
	s := r.storage
	s.maxFileBytes = 3
	s.cfg.Limits.MaxOutputBytes = 10
	task, e := s.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "export", Size: 6})
	if e != nil {
		t.Fatal(e)
	}
	c.StorageTask.UpdateOneID(task.ID).SetPhase("rendering").ExecX(ctx)
	f, e := s.Stage(ctx, task, bytes.NewBufferString("output"), 6)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	if c.StorageTask.GetX(ctx, task.ID).Phase != "rendering" {
		t.Fatal("transfer replaced rendering phase")
	}
	if e = s.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if c.StorageWrite.GetX(ctx, f.Write.ID).Phase != "prepared" {
		t.Fatal("export input was discarded")
	}
}
