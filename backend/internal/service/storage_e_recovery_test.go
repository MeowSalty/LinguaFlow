package service

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagemigrationitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
)

// Keep the database and registered providers, but reconstruct all execution locks
// and caches as a new server process would after deployment configuration changes.
func restartStorageE(t *testing.T, old *StorageService, cfg config.StorageConfig) *StorageService {
	t.Helper()
	next, err := NewStorageService(old.client, old.projects, old.workDir)
	if err != nil {
		t.Fatal(err)
	}
	next.Configure(cfg, old.dialect, old.defaultSpaceID)
	old.mu.Lock()
	for id, driver := range old.drivers {
		next.RegisterDriver(id, driver)
	}
	old.mu.Unlock()
	old.resources.SetStorage(next)
	return next
}

func TestStorageEAcceptedRemoteTaskResumesWithOriginalIdentity(t *testing.T) {
	ctx, client, resources, project, owner, driver := storageLifecycleFixture(t)
	s := resources.storage
	space := client.StorageSpace.GetX(ctx, *project.StorageSpaceID)
	client.StorageConnection.UpdateOneID(space.ConnectionID).SetDriver(storageconnection.DriverS3).ExecX(ctx)
	s.cfg.Enabled = true
	input := StorageIntent{Kind: "upload", IdempotencyKey: "deployment-resume", Path: "one.txt", Size: 3}
	task, err := s.Begin(ctx, owner.ID, project.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.cfg
	cfg.Enabled = false
	s = restartStorageE(t, s, cfg)
	if _, err := s.Receive(ctx, owner.ID, project.ID, task.ID, bytes.NewBufferString("one"), 3); !errors.Is(err, ErrStorageDeploymentDisabled) {
		t.Fatalf("accepted task bypassed new deployment: %v", err)
	}
	blocked := client.StorageTask.GetX(ctx, task.ID)
	if blocked.Status != storagetask.StatusNeedsAction || blocked.ErrorCode != "storage_deployment_disabled" || blocked.NextRetryAt != nil {
		t.Fatalf("deployment rejection became automatic retry: %+v", blocked)
	}
	if task.OperationID != blocked.OperationID || !reflect.DeepEqual(task.Deadline, blocked.Deadline) || !reflect.DeepEqual(task.Input, blocked.Input) || task.IdempotencyKey != blocked.IdempotencyKey || task.ExpectedStorageGeneration != blocked.ExpectedStorageGeneration {
		t.Fatal("deployment refusal replaced accepted intent facts")
	}
	if driver.puts != 0 || slices.Contains(s.TaskActions(ctx, owner.ID, blocked), "retry") {
		t.Fatal("disabled task still writes or advertises retry")
	}
	if _, err := s.Retry(ctx, owner.ID, project.ID, task.ID); !errors.Is(err, ErrStorageDeploymentDisabled) {
		t.Fatalf("retry ignored current availability: %v", err)
	}
	if err := s.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if after := client.StorageTask.GetX(ctx, task.ID); after.Attempts != blocked.Attempts || after.NextRetryAt != nil {
		t.Fatal("deployment failure created retry churn")
	}
	cfg.Enabled = true
	s = restartStorageE(t, s, cfg)
	if !slices.Contains(s.TaskActions(ctx, owner.ID, blocked), "retry") {
		t.Fatal("reenabled task did not expose retry")
	}
	if _, err := s.Retry(ctx, owner.ID, project.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Receive(ctx, owner.ID, project.ID, task.ID, bytes.NewBufferString("one"), 3); err != nil {
		t.Fatal(err)
	}
	if err := s.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	completed := client.StorageTask.GetX(ctx, task.ID)
	if completed.Status != storagetask.StatusCompleted || completed.OperationID != task.OperationID || driver.puts != 1 {
		t.Fatalf("recovery did not commit original task: %+v puts=%d", completed, driver.puts)
	}
	cfg.Enabled = false
	s = restartStorageE(t, s, cfg)
	replay, err := s.Begin(ctx, owner.ID, project.ID, input)
	if err != nil || replay.ID != task.ID {
		t.Fatalf("completed Begin replay rejected: %+v %v", replay, err)
	}
	replay, err = s.Receive(ctx, owner.ID, project.ID, task.ID, bytes.NewBufferString("one"), 3)
	if err != nil || replay.ID != task.ID || replay.Status != storagetask.StatusCompleted || driver.puts != 1 {
		t.Fatalf("completed content replay rejected or wrote: %+v %v", replay, err)
	}
}

func TestStorageEPreparedRemoteCandidateSurvivesDisableUntilExpiry(t *testing.T) {
	ctx, client, resources, project, owner, driver := storageLifecycleFixture(t)
	s := resources.storage
	space := client.StorageSpace.GetX(ctx, *project.StorageSpaceID)
	client.StorageConnection.UpdateOneID(space.ConnectionID).SetDriver(storageconnection.DriverS3).ExecX(ctx)
	s.cfg.Enabled = true
	task, err := s.Begin(ctx, owner.ID, project.ID, StorageIntent{Kind: "upload", IdempotencyKey: "prepared-disable", Path: "one.txt", Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Receive(ctx, owner.ID, project.ID, task.ID, bytes.NewBufferString("one"), 3); err != nil {
		t.Fatal(err)
	}
	before := client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID)).OnlyX(ctx)
	cfg := s.cfg
	cfg.Enabled = false
	s = restartStorageE(t, s, cfg)
	if err := s.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	blocked := client.StorageTask.GetX(ctx, task.ID)
	if blocked.Status != storagetask.StatusNeedsAction || blocked.ErrorCode != "storage_deployment_disabled" || blocked.NextRetryAt != nil {
		t.Fatalf("publication failure lost deployment error: %+v", blocked)
	}
	if client.Resource.Query().CountX(ctx) != 0 {
		t.Fatal("prepared object published after deployment disabled")
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	retained := client.StorageWrite.GetX(ctx, before.ID)
	if retained.Phase != "prepared" || retained.ObjectKey != before.ObjectKey || retained.AttemptID != before.AttemptID || !reflect.DeepEqual(retained.ExpiresAt, before.ExpiresAt) || driver.deletes != 0 {
		t.Fatalf("recoverable candidate was replaced or cleaned: %+v", retained)
	}
	client.StorageTask.UpdateOneID(task.ID).SetDeadline(time.Now().Add(-time.Hour)).ExecX(ctx)
	client.StorageWrite.UpdateOneID(before.ID).SetExpiresAt(time.Now().Add(-time.Hour)).ExecX(ctx)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	expired := client.StorageTask.GetX(ctx, task.ID)
	if expired.ErrorCode != "storage_intent_expired" || len(s.TaskActions(ctx, owner.ID, expired)) != 0 {
		t.Fatalf("expired candidate retained retry: %+v", expired)
	}
	if _, err := s.Retry(ctx, owner.ID, project.ID, task.ID); !errors.Is(err, ErrStorageExpired) {
		t.Fatalf("expired candidate resurrected: %v", err)
	}
	if client.StorageWrite.GetX(ctx, before.ID).Phase != "cleaned" || driver.deletes != 1 {
		t.Fatal("deployment disabled blocked registered candidate cleanup")
	}
}

func TestStorageECopyKeepsCandidateAndResumesWithoutDuplicateTransfer(t *testing.T) {
	ctx, client, resources, project, owner, sourceDriver := storageLifecycleFixture(t)
	s := resources.storage
	storageUpload(t, ctx, resources, project, owner, "one.txt", "one")
	storageUpload(t, ctx, resources, project, owner, "two.txt", "two")
	targetDriver := &connectionTestDriver{objects: map[string][]byte{}}
	target, err := s.InstallSiteSpace(ctx, "remote-copy-target", targetDriver)
	if err != nil {
		t.Fatal(err)
	}
	client.StorageConnection.UpdateOneID(target.ConnectionID).SetDriver(storageconnection.DriverS3).ExecX(ctx)
	s.cfg.Enabled, s.cfg.ReconcileBatchSize = true, 1
	task, err := s.StartMigration(ctx, owner.ID, project.ID, target.ID, project.StorageGeneration, "deployment-copy")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ContinueMigration(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	before := client.StorageTask.GetX(ctx, task.ID)
	itemsBefore := client.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(task.ID)).Order(ent.Asc(storagemigrationitem.FieldID)).AllX(ctx)
	if before.Phase != "copy" || before.Deadline == nil || len(itemsBefore) != 2 || itemsBefore[0].Status != storagemigrationitem.StatusVerified || itemsBefore[0].TargetLocationID == nil || itemsBefore[1].Status != storagemigrationitem.StatusPending || itemsBefore[1].TargetLocationID != nil || targetDriver.puts != 1 || targetDriver.deletes != 0 {
		t.Fatalf("fixture did not stop after one remote candidate: phase=%s items=%+v puts=%d deletes=%d", before.Phase, itemsBefore, targetDriver.puts, targetDriver.deletes)
	}
	candidateBefore := client.BlobLocation.GetX(ctx, *itemsBefore[0].TargetLocationID)
	writeBefore := client.StorageWrite.Query().Where(storagewrite.LocationIDEQ(candidateBefore.ID)).OnlyX(ctx)
	projectBefore := client.Project.GetX(ctx, project.ID)
	targetBefore := client.StorageSpace.GetX(ctx, target.ID)
	sourcePuts, sourceDeletes := sourceDriver.puts, sourceDriver.deletes
	cfg := s.cfg
	cfg.Enabled = false
	s = restartStorageE(t, s, cfg)
	if err := s.ContinueMigration(ctx, task.ID); !errors.Is(err, ErrStorageDeploymentDisabled) {
		t.Fatalf("copy bypassed deployment: %v", err)
	}
	if err := s.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	blocked := client.StorageTask.GetX(ctx, task.ID)
	if blocked.Phase != "copy" || blocked.Status != storagetask.StatusNeedsAction || blocked.ErrorCode != "storage_deployment_disabled" || blocked.NextRetryAt != nil || blocked.OperationID != before.OperationID || blocked.IdempotencyKey != before.IdempotencyKey || blocked.RequestHash != before.RequestHash || blocked.ExpectedStorageGeneration != before.ExpectedStorageGeneration || !reflect.DeepEqual(blocked.Deadline, before.Deadline) || !reflect.DeepEqual(blocked.Input, before.Input) || !reflect.DeepEqual(blocked.TargetSpaceID, before.TargetSpaceID) || blocked.CleanupStatus != before.CleanupStatus {
		t.Fatalf("deployment rejection lost copy recovery facts: %+v", blocked)
	}
	itemsAfter := client.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(task.ID)).Order(ent.Asc(storagemigrationitem.FieldID)).AllX(ctx)
	if len(itemsAfter) != len(itemsBefore) {
		t.Fatal("disabled copy changed manifest length")
	}
	for i, item := range itemsAfter {
		old := itemsBefore[i]
		if item.ID != old.ID || item.BlobID != old.BlobID || item.SourceLocationID != old.SourceLocationID || item.ExpectedLocationGeneration != old.ExpectedLocationGeneration || item.Status != old.Status || !reflect.DeepEqual(item.TargetLocationID, old.TargetLocationID) {
			t.Fatal("disabled copy rewrote the migration manifest")
		}
	}
	candidateAfter := client.BlobLocation.GetX(ctx, candidateBefore.ID)
	writeAfter := client.StorageWrite.GetX(ctx, writeBefore.ID)
	if candidateAfter.Status != bloblocation.StatusCandidate || candidateAfter.ObjectKey != candidateBefore.ObjectKey || writeAfter.Phase != "prepared" || writeAfter.AttemptID != writeBefore.AttemptID || writeAfter.ObjectKey != writeBefore.ObjectKey || !reflect.DeepEqual(writeAfter.LocationID, writeBefore.LocationID) || !reflect.DeepEqual(writeAfter.ExpiresAt, writeBefore.ExpiresAt) {
		t.Fatal("disabled copy replaced or cleaned its prepared candidate")
	}
	projectAfter := client.Project.GetX(ctx, project.ID)
	targetAfter := client.StorageSpace.GetX(ctx, target.ID)
	if projectAfter.StorageState != projectBefore.StorageState || projectAfter.StorageGeneration != projectBefore.StorageGeneration || !reflect.DeepEqual(projectAfter.StorageMigrationTaskID, projectBefore.StorageMigrationTaskID) || !reflect.DeepEqual(projectAfter.StorageSpaceID, projectBefore.StorageSpaceID) || targetAfter.CandidateBytes != targetBefore.CandidateBytes || targetAfter.LiveBytes != targetBefore.LiveBytes {
		t.Fatal("disabled copy changed its project barrier, binding, or candidate accounting")
	}
	if targetDriver.puts != 1 || targetDriver.deletes != 0 || sourceDriver.puts != sourcePuts || sourceDriver.deletes != sourceDeletes {
		t.Fatal("disabled copy wrote to or deleted from a provider")
	}
	if slices.Contains(s.TaskActions(ctx, owner.ID, blocked), "retry") {
		t.Fatal("disabled copy advertised retry")
	}
	if _, err := s.Retry(ctx, owner.ID, project.ID, task.ID); !errors.Is(err, ErrStorageDeploymentDisabled) {
		t.Fatalf("disabled copy retried: %v", err)
	}
	cfg.Enabled = true
	s = restartStorageE(t, s, cfg)
	if !slices.Contains(s.TaskActions(ctx, owner.ID, blocked), "retry") {
		t.Fatal("restored copy did not advertise retry")
	}
	if _, err := s.Retry(ctx, owner.ID, project.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8 && client.StorageTask.GetX(ctx, task.ID).Status != storagetask.StatusCompleted; i++ {
		if err := s.ContinueMigration(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	done := client.StorageTask.GetX(ctx, task.ID)
	published := client.Project.GetX(ctx, project.ID)
	if done.Status != storagetask.StatusCompleted || done.OperationID != before.OperationID || targetDriver.puts != 2 || targetDriver.deletes != 0 || published.StorageState != "active" || *published.StorageSpaceID != target.ID {
		t.Fatalf("copy did not resume once under its original identity: %+v puts=%d deletes=%d", done, targetDriver.puts, targetDriver.deletes)
	}
	if first := client.StorageMigrationItem.GetX(ctx, itemsBefore[0].ID); first.Status != storagemigrationitem.StatusCommitted || !reflect.DeepEqual(first.TargetLocationID, itemsBefore[0].TargetLocationID) {
		t.Fatal("recovery replaced the first prepared candidate instead of publishing it")
	}
}

func TestStorageECutoverKeepsBarrierAndCandidateIdentity(t *testing.T) {
	ctx, client, resources, project, owner, _ := storageLifecycleFixture(t)
	s := resources.storage
	storageUpload(t, ctx, resources, project, owner, "one.txt", "one")
	storageUpload(t, ctx, resources, project, owner, "two.txt", "two")
	targetDriver := &connectionTestDriver{objects: map[string][]byte{}}
	target, err := s.InstallSiteSpace(ctx, "remote-target", targetDriver)
	if err != nil {
		t.Fatal(err)
	}
	client.StorageConnection.UpdateOneID(target.ConnectionID).SetDriver(storageconnection.DriverS3).ExecX(ctx)
	s.cfg.Enabled, s.cfg.ReconcileBatchSize = true, 1
	task, err := s.StartMigration(ctx, owner.ID, project.ID, target.ID, project.StorageGeneration, "deployment-cutover")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8 && client.StorageTask.GetX(ctx, task.ID).Phase != "cutover"; i++ {
		if err := s.ContinueMigration(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	before := client.StorageTask.GetX(ctx, task.ID)
	if before.Phase != "cutover" {
		t.Fatalf("did not reach durable partial cutover: %+v", before)
	}
	projectBefore := client.Project.GetX(ctx, project.ID)
	itemsBefore := client.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(task.ID)).Order(ent.Asc(storagemigrationitem.FieldID)).AllX(ctx)
	committed, verified := 0, 0
	for _, item := range itemsBefore {
		if item.TargetLocationID == nil {
			t.Fatal("cutover item has no prepared target location")
		}
		blob := client.Blob.GetX(ctx, item.BlobID)
		location := client.BlobLocation.GetX(ctx, *item.TargetLocationID)
		switch item.Status {
		case storagemigrationitem.StatusCommitted:
			committed++
			if !reflect.DeepEqual(blob.ActiveLocationID, item.TargetLocationID) || blob.LocationGeneration != item.ExpectedLocationGeneration+1 || location.Status != bloblocation.StatusLive {
				t.Fatal("committed cutover item did not publish its target location")
			}
		case storagemigrationitem.StatusVerified:
			verified++
			if blob.ActiveLocationID == nil || *blob.ActiveLocationID != item.SourceLocationID || blob.LocationGeneration != item.ExpectedLocationGeneration || location.Status != bloblocation.StatusCandidate {
				t.Fatal("uncommitted cutover item no longer retains its original source and candidate")
			}
		}
	}
	if len(itemsBefore) != 2 || committed != 1 || verified != 1 || targetDriver.puts != 2 || targetDriver.deletes != 0 {
		t.Fatalf("cutover was not partially published: committed=%d verified=%d puts=%d deletes=%d", committed, verified, targetDriver.puts, targetDriver.deletes)
	}
	puts, deletes := targetDriver.puts, targetDriver.deletes
	cfg := s.cfg
	cfg.Enabled = false
	s = restartStorageE(t, s, cfg)
	if err := s.ContinueMigration(ctx, task.ID); !errors.Is(err, ErrStorageDeploymentDisabled) {
		t.Fatalf("cutover bypassed deployment: %v", err)
	}
	blocked := client.StorageTask.GetX(ctx, task.ID)
	if blocked.Phase != "cutover" || blocked.Status != storagetask.StatusNeedsAction || blocked.OperationID != before.OperationID || blocked.ErrorCode != "storage_deployment_disabled" {
		t.Fatalf("cutover recovery identity lost: %+v", blocked)
	}
	client.StorageWrite.Update().Where(storagewrite.TaskIDEQ(task.ID), storagewrite.PhaseEQ("prepared")).SetExpiresAt(time.Now().Add(-time.Hour)).ExecX(ctx)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	after := client.Project.GetX(ctx, project.ID)
	if after.StorageState != projectBefore.StorageState || after.StorageGeneration != projectBefore.StorageGeneration || !reflect.DeepEqual(after.StorageMigrationTaskID, projectBefore.StorageMigrationTaskID) || *after.StorageSpaceID != *projectBefore.StorageSpaceID {
		t.Fatal("disabled cutover removed migration barrier or changed binding")
	}
	itemsAfter := client.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(task.ID)).Order(ent.Asc(storagemigrationitem.FieldID)).AllX(ctx)
	for i, item := range itemsAfter {
		if item.Status != itemsBefore[i].Status || !reflect.DeepEqual(item.TargetLocationID, itemsBefore[i].TargetLocationID) {
			t.Fatal("disabled cutover rewrote migration manifest")
		}
	}
	if targetDriver.puts != puts || targetDriver.deletes != deletes || len(s.TaskActions(ctx, owner.ID, blocked)) != 0 {
		t.Fatal("disabled cutover wrote, deleted protected candidates, or exposed an action")
	}
	if _, err := s.Cancel(ctx, owner.ID, project.ID, task.ID); !errors.Is(err, ErrStorageConflict) {
		t.Fatalf("cancelled irreversible cutover: %v", err)
	}
	if _, err := s.Retry(ctx, owner.ID, project.ID, task.ID); !errors.Is(err, ErrStorageDeploymentDisabled) {
		t.Fatalf("disabled cutover retried: %v", err)
	}
	cfg.Enabled = true
	s = restartStorageE(t, s, cfg)
	if !slices.Contains(s.TaskActions(ctx, owner.ID, blocked), "retry") {
		t.Fatal("restored cutover cannot retry")
	}
	if _, err := s.Retry(ctx, owner.ID, project.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ContinueMigration(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if done := client.StorageTask.GetX(ctx, task.ID); done.Status != storagetask.StatusCompleted || done.OperationID != task.OperationID {
		t.Fatalf("restored cutover failed: %+v", done)
	}
}

func TestStorageERemoteMigrationToLocalStillChecksPolicy(t *testing.T) {
	ctx, client, resources, project, owner, _ := storageLifecycleFixture(t)
	s := resources.storage
	source := client.StorageSpace.GetX(ctx, *project.StorageSpaceID)
	client.StorageConnection.UpdateOneID(source.ConnectionID).SetDriver(storageconnection.DriverS3).ExecX(ctx)
	s.cfg.Enabled = true
	storageUpload(t, ctx, resources, project, owner, "one.txt", "one")
	target, err := s.InstallSiteSpace(ctx, "local-target", &connectionTestDriver{objects: map[string][]byte{}})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Enabled = false
	client.SystemSetting.Create().SetKey(storagePolicyKey).SetValue(`{"mode":"user_required","default_choice":"user","generation":9,"logical_limit_bytes":100000}`).ExecX(ctx)
	if _, err := s.StartMigration(ctx, owner.ID, project.ID, target.ID, project.StorageGeneration, "blocked-move"); !errors.Is(err, ErrStoragePolicy) {
		t.Fatalf("migration escape bypassed user_required: %v", err)
	}
	client.SystemSetting.Update().Where(systemsetting.KeyEQ(storagePolicyKey)).SetValue(`{"mode":"both","default_choice":"site","generation":10,"logical_limit_bytes":100000}`).ExecX(ctx)
	view, err := s.ProjectStorage(ctx, owner.ID, project.ID)
	if err != nil || view.Binding == nil || view.Binding.Historical || !slices.Contains(view.ReasonCodes, "storage_deployment_disabled") {
		t.Fatalf("deployment refusal became historical policy binding: %+v %v", view, err)
	}
	task, err := s.StartMigration(ctx, owner.ID, project.ID, target.ID, project.StorageGeneration, "allowed-move")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8 && client.StorageTask.GetX(ctx, task.ID).Status != storagetask.StatusCompleted; i++ {
		if err := s.ContinueMigration(ctx, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := client.Project.GetX(ctx, project.ID); *got.StorageSpaceID != target.ID || got.StorageState != "active" {
		t.Fatalf("valid remote-to-local migration blocked by deployment: %+v", got)
	}
}

func TestStorageEDeploymentErrorSurvivesSanitizeAndDiagnostics(t *testing.T) {
	ctx, client, connections, owner, _, space, _ := storageConnectionFixture(t)
	wrapped := fmt.Errorf("provider-secret: %w", ErrStorageDeploymentDisabled)
	if safe := connections.sanitize(wrapped); !errors.Is(safe, ErrStorageDeploymentDisabled) || StorageErrorCode(safe) != "storage_deployment_disabled" {
		t.Fatalf("deployment error sanitized into a transient failure: %v", safe)
	}
	client.User.UpdateOneID(owner.ID).SetRole(SystemRoleAdmin).ExecX(ctx)
	s, err := NewStorageService(client, NewProjectService(client, NewUserService(client, nil)), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	location := client.BlobLocation.Create().SetSpaceID(space.ID).SetObjectKey("registered-candidate").SetStatus(bloblocation.StatusRetired).SaveX(ctx)
	client.DeletionEntry.Create().SetLocationID(location.ID).SetNotBefore(time.Now()).SetStatus(deletionentry.StatusBlocked).SetErrorCode(StorageErrorCode(wrapped)).ExecX(ctx)
	diagnostic, err := s.Diagnostics(ctx, owner.ID, 0, 50)
	if err != nil || diagnostic.BlockedCleanupByCode["storage_deployment_disabled"] != 1 || diagnostic.BlockedCleanupByCode["other"] != 0 {
		t.Fatalf("diagnostics lost deployment error: %+v %v", diagnostic, err)
	}
}

func TestStorageEBatchDeploymentFailureAndCompletedReplayStayFixed(t *testing.T) {
	ctx, client, resources, project, owner, driver := storageLifecycleFixture(t)
	s := resources.storage
	space := client.StorageSpace.GetX(ctx, *project.StorageSpaceID)
	client.StorageConnection.UpdateOneID(space.ConnectionID).SetDriver(storageconnection.DriverS3).ExecX(ctx)
	files := func() []UploadedFile {
		return []UploadedFile{contractFile("", "one.txt", "one"), contractFile("", "two.txt", "two")}
	}
	failed, err := resources.UploadResourceBatch(ctx, owner.ID, project.ID, "disabled-batch", files())
	if err != nil || len(failed.Items) != 2 {
		t.Fatalf("batch rejection: %+v %v", failed, err)
	}
	for _, item := range failed.Items {
		if item.Action != "failed" || item.ErrorCode != "storage_deployment_disabled" || item.Error != item.ErrorCode {
			t.Fatalf("batch failure lost deployment error: %+v", item)
		}
	}
	if driver.puts != 0 || client.Resource.Query().CountX(ctx) != 0 {
		t.Fatal("disabled batch wrote objects or resources")
	}
	s.cfg.Enabled = true
	replay, err := resources.UploadResourceBatch(ctx, owner.ID, project.ID, "disabled-batch", files())
	if err != nil || !reflect.DeepEqual(replay, failed) {
		t.Fatalf("reenabling changed fixed batch failure: %+v %v", replay, err)
	}
	completed, err := resources.UploadResourceBatch(ctx, owner.ID, project.ID, "enabled-batch", files())
	if err != nil || len(completed.Items) != 2 {
		t.Fatalf("enabled batch: %+v %v", completed, err)
	}
	for _, item := range completed.Items {
		if item.Action != "created" {
			t.Fatalf("enabled batch still denied: %+v", item)
		}
	}
	puts := driver.puts
	s.cfg.Enabled = false
	replay, err = resources.UploadResourceBatch(ctx, owner.ID, project.ID, "enabled-batch", files())
	if err != nil || !reflect.DeepEqual(replay, completed) || driver.puts != puts || client.Resource.Query().CountX(ctx) != 2 {
		t.Fatalf("completed batch replay changed after disable: %+v %v", replay, err)
	}
}
