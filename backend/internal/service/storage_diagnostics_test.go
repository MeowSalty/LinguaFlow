package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagebackup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

type diagnosticNoIODriver struct{}

func (diagnosticNoIODriver) PutNew(context.Context, string, io.Reader, int64) (storage.Object, error) {
	panic("diagnostics performed storage I/O")
}
func (diagnosticNoIODriver) Open(context.Context, storage.Object) (io.ReadCloser, error) {
	panic("diagnostics performed storage I/O")
}
func (diagnosticNoIODriver) Stat(context.Context, storage.Object) (storage.Object, error) {
	panic("diagnostics performed storage I/O")
}
func (diagnosticNoIODriver) Delete(context.Context, storage.Object) error {
	panic("diagnostics performed storage I/O")
}

func TestStorageDiagnosticsPaginationGlobalCountsAndRedaction(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	admin := c.User.Create().SetUsername("admin-diag").SetEmail("diag@example.test").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SetActive(true).SaveX(ctx)
	s, e := NewStorageService(c, NewProjectService(c, NewUserService(c, nil)), t.TempDir())
	if s != nil {
		if initErr := s.EnsureStoragePolicy(context.Background(), true, false, nil, false, nil); initErr != nil {
			t.Fatal(initErr)
		}
	}
	if e != nil {
		t.Fatal(e)
	}
	conn := c.StorageConnection.Create().SetName("secret-connection-name").SetDriver("local").SaveX(ctx)
	first := c.StorageSpace.Create().SetConnectionID(conn.ID).SetIdentity("secret-identity-1").SetMarkerNonce("secret-nonce").SetName("secret-space-name").SetBucket("secret-bucket").SetPrefix("secret-prefix").SetReservedBytes(10).SetCandidateBytes(20).SetLiveBytes(30).SetPendingDeleteBytes(40).SaveX(ctx)
	second := c.StorageSpace.Create().SetConnectionID(conn.ID).SetIdentity("secret-identity-2").SetMarkerNonce("secret-nonce").SetName("second").SaveX(ctx)
	s.RegisterDriver(first.ID, diagnosticNoIODriver{})
	s.RegisterDriver(second.ID, diagnosticNoIODriver{})
	s.SetResolver(func(context.Context, int, bool) (storage.Driver, error) { panic("diagnostics resolved provider") })
	checkedAt := time.Date(2026, 10, 1, 8, 9, 10, 123000000, time.UTC)
	c.BlobLocation.Create().SetSpaceID(first.ID).SetObjectKey("secret/unknown").SetStatus(bloblocation.StatusLive).SetIntegrity(bloblocation.IntegrityUnknown).SaveX(ctx)
	missing := c.BlobLocation.Create().SetSpaceID(first.ID).SetObjectKey("secret/missing").SetStatus(bloblocation.StatusRetired).SetIntegrity(bloblocation.IntegrityMissing).SaveX(ctx)
	corrupt := c.BlobLocation.Create().SetSpaceID(first.ID).SetObjectKey("secret/corrupt").SetStatus(bloblocation.StatusLive).SetIntegrity(bloblocation.IntegrityCorrupt).SaveX(ctx)
	c.BlobLocation.Create().SetSpaceID(first.ID).SetObjectKey("secret/deleted").SetStatus(bloblocation.StatusDeleted).SetIntegrity(bloblocation.IntegrityMissing).SetVerifiedAt(checkedAt.Add(time.Hour)).SaveX(ctx)
	c.BlobLocation.Create().SetSpaceID(first.ID).SetObjectKey("secret/verified").SetStatus(bloblocation.StatusLive).SetIntegrity(bloblocation.IntegrityAvailable).SetVerifiedAt(checkedAt).SaveX(ctx)
	c.BlobLocation.Create().SetSpaceID(second.ID).SetObjectKey("secret/other").SetIntegrity(bloblocation.IntegrityUnknown).SaveX(ctx)
	newTask := func(kind, phase string, status storagetask.Status) *ent.StorageTask {
		key := generateUniqueID()
		return c.StorageTask.Create().SetOperationID(key).SetIdempotencyKey(key).SetRequestHash("secret-hash").SetKind(kind).SetPhase(phase).SetStatus(status).SetInput(map[string]any{"secret": "secret-input"}).SaveX(ctx)
	}
	migration := newTask("migration", "copy", storagetask.StatusRunning)
	newTask("migration", "secret-phase-value", storagetask.StatusNeedsAction)
	newTask("migration", "committed", storagetask.StatusCompleted)
	legacy := newTask("legacy_cleanup", "unverified", storagetask.StatusNeedsAction)
	c.StorageTask.UpdateOneID(legacy.ID).SetCleanupStatus(storagetask.CleanupStatusBlocked).SetErrorCode("legacy_location_unverified").ExecX(ctx)
	newTask("upload", "prepared", storagetask.StatusWaitingRetry)
	newTask("upload", "receiving", storagetask.StatusCancelled)
	c.DeletionEntry.Create().SetLocationID(missing.ID).SetNotBefore(checkedAt).SetStatus(deletionentry.StatusBlocked).SetErrorCode("storage_permission_denied").SaveX(ctx)
	c.DeletionEntry.Create().SetLocationID(corrupt.ID).SetNotBefore(checkedAt).SetStatus(deletionentry.StatusBlocked).SetErrorCode("https://secret-token.example/secret-path").SaveX(ctx)
	oldestAt := checkedAt.Add(-24 * time.Hour)
	for _, phase := range []string{"receiving", "committed", "cleaned"} {
		created := oldestAt
		if phase != "receiving" {
			created = created.Add(-time.Hour)
		}
		c.StorageWrite.Create().SetTaskID(migration.ID).SetSpaceID(first.ID).SetAttemptID(generateUniqueID()).SetObjectKey("secret-write-" + phase).SetMaxBytes(3).SetPhase(phase).SetCreatedAt(created).SaveX(ctx)
	}
	c.StorageBackup.Create().SetIdentity("secret-old-backup").SetStatus(storagebackup.StatusComplete).SetCreatedAt(checkedAt.Add(-time.Hour)).SetExpiresAt(checkedAt.Add(time.Hour)).SaveX(ctx)
	latest := c.StorageBackup.Create().SetIdentity("secret-latest-backup").SetStatus(storagebackup.StatusIncomplete).SetCreatedAt(checkedAt).SetExpiresAt(checkedAt.Add(time.Hour)).SetManifest(map[string]any{"key": "secret-backup-key"}).SaveX(ctx)
	s.mu.Lock()
	s.tempBytes = 42
	s.mu.Unlock()
	c.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) {
			return nil, errors.New("diagnostics must not mutate records")
		})
	})
	got, e := s.Diagnostics(ctx, admin.ID, 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(got.Spaces) != 1 || got.Spaces[0].ID != first.ID || got.NextCursor == nil || *got.NextCursor != first.ID {
		t.Fatalf("incorrect first page: %+v", got)
	}
	space := got.Spaces[0]
	if space.ReservedBytes != 10 || space.CandidateBytes != 20 || space.LiveBytes != 30 || space.PendingDeleteBytes != 40 || space.UncheckedObjects != 1 || space.MissingObjects != 1 || space.CorruptObjects != 1 {
		t.Fatalf("incorrect space counts: %+v", space)
	}
	if space.LastCheckedAt == nil || !space.LastCheckedAt.Equal(checkedAt) {
		t.Fatalf("incorrect verification timestamp: %v", space.LastCheckedAt)
	}
	if got.TemporaryBytes != 42 || got.RecoveryBacklog != 4 || got.OldestIntentAt == nil || !got.OldestIntentAt.Equal(oldestAt) {
		t.Fatalf("incorrect global backlog: %+v", got)
	}
	if got.BlockedCleanupByCode["storage_permission_denied"] != 1 || got.BlockedCleanupByCode["legacy_location_unverified"] != 1 || got.BlockedCleanupByCode["other"] != 1 || got.MigrationsByPhase["copy"] != 1 || got.MigrationsByPhase["other"] != 1 {
		t.Fatalf("incorrect global grouping: %+v", got)
	}
	if got.LatestBackup == nil || got.LatestBackup.ID != latest.ID || got.LatestBackup.Status != "incomplete" {
		t.Fatal("latest backup integrity missing")
	}
	encoded, e := json.Marshal(got)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("diagnostics leaked storage identity: %s", encoded)
	}
	next, e := s.Diagnostics(ctx, admin.ID, *got.NextCursor, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Spaces) != 1 || next.Spaces[0].ID != second.ID || next.Spaces[0].UncheckedObjects != 1 || next.Spaces[0].LastCheckedAt != nil || next.NextCursor != nil || next.RecoveryBacklog != got.RecoveryBacklog {
		t.Fatalf("incorrect next page/global aggregation: %+v", next)
	}
}

func TestStorageDiagnosticsAuthorizationValidationAndEmptyResult(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	s, e := NewStorageService(c, NewProjectService(c, NewUserService(c, nil)), t.TempDir())
	if s != nil {
		if initErr := s.EnsureStoragePolicy(context.Background(), true, false, nil, false, nil); initErr != nil {
			t.Fatal(initErr)
		}
	}
	if e != nil {
		t.Fatal(e)
	}
	member := c.User.Create().SetUsername("member").SetEmail("member@example.test").SetPasswordHash("unused").SaveX(ctx)
	inactive := c.User.Create().SetUsername("inactive").SetEmail("inactive@example.test").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SetActive(false).SaveX(ctx)
	admin := c.User.Create().SetUsername("admin").SetEmail("admin@example.test").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SetActive(true).SaveX(ctx)
	for _, actor := range []int{0, member.ID, inactive.ID} {
		if _, e = s.Diagnostics(ctx, actor, 0, 10); !errors.Is(e, ErrForbidden) {
			t.Fatalf("non-admin diagnostics: %v", e)
		}
	}
	for _, page := range [][2]int{{-1, 10}, {0, -1}, {0, 101}} {
		if _, e = s.Diagnostics(ctx, admin.ID, page[0], page[1]); !errors.Is(e, ErrInvalidInput) {
			t.Fatalf("invalid pagination accepted: %v", e)
		}
	}
	got, e := s.Diagnostics(ctx, admin.ID, 0, 0)
	if e != nil {
		t.Fatal(e)
	}
	if got.Spaces == nil || len(got.Spaces) != 0 || got.NextCursor != nil || got.LatestBackup != nil || got.OldestIntentAt != nil || got.RecoveryBacklog != 0 || got.BlockedCleanupByCode == nil || got.MigrationsByPhase == nil {
		t.Fatalf("incorrect empty diagnostics: %+v", got)
	}
}
