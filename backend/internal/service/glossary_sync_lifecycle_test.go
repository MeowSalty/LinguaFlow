package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
)

type syncLifecycleFixture struct {
	client                                *ent.Client
	svc                                   *GlossarySyncService
	owner, projectID, entryID, resourceID int
	segmentIDs                            []int
}

func seedSyncLifecycle(t *testing.T, client *ent.Client, count int) syncLifecycleFixture {
	t.Helper()
	ctx := context.Background()
	u := createTestUser(t, client, fmt.Sprintf("sync-%d", time.Now().UnixNano()))
	projectID := seedProject(t, client, u.ID)
	entry := client.GlossaryEntry.Create().SetProjectID(projectID).SetSourceKey("term").SetSource("term").SetTarget("旧").SaveX(ctx)
	res := client.Resource.Create().SetProjectID(projectID).SetPath("sync.txt").SetFormat("txt").SetStoragePath("unused/sync.txt").SaveX(ctx)
	f := syncLifecycleFixture{client: client, owner: u.ID, projectID: projectID, entryID: entry.ID, resourceID: res.ID}
	for i := 0; i < count; i++ {
		row := client.Segment.Create().SetResourceID(res.ID).SetSegmentIndex(i).SetSourceText("term").SetTargetText("旧").SetStatus(segment.StatusTranslated).SaveX(ctx)
		f.segmentIDs = append(f.segmentIDs, row.ID)
	}
	f.svc = NewGlossarySyncService(client, nil, nil, NewAuditService(client, nil, nil), discardLogger())
	return f
}

func (f syncLifecycleFixture) submit(t *testing.T) *ent.SyncTask {
	t.Helper()
	info, err := f.svc.SubmitSyncTask(context.Background(), f.owner, f.projectID, f.entryID, GlossarySyncExecuteInput{OldTarget: "旧", NewTarget: "旧新"})
	if err != nil {
		t.Fatal(err)
	}
	return f.client.SyncTask.GetX(context.Background(), info.TaskID)
}

func TestGlossarySyncLifecycleResourcesAndAuthorization(t *testing.T) {
	ctx := context.Background()
	f := seedSyncLifecycle(t, testClient(t), 1)
	other := createTestUser(t, f.client, "other-sync-user")
	foreignProject := seedProject(t, f.client, other.ID)
	foreignResource := f.client.Resource.Create().SetProjectID(foreignProject).SetPath("foreign").SetFormat("txt").SetStoragePath("unused").SaveX(ctx)
	for _, resources := range [][]int{{0}, {-1}, {foreignResource.ID}, {f.resourceID, foreignResource.ID}} {
		_, err := f.svc.SubmitSyncTask(ctx, f.owner, f.projectID, f.entryID, GlossarySyncExecuteInput{OldTarget: "旧", NewTarget: "新", ResourceIDs: resources})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("resources=%v err=%v", resources, err)
		}
		_, err = f.svc.AnalyzeSyncImpact(ctx, f.owner, f.projectID, f.entryID, GlossarySyncImpactInput{OldTarget: "旧", ResourceIDs: resources})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("impact resources=%v err=%v", resources, err)
		}
	}
	for _, resources := range [][]int{nil, {f.resourceID, f.resourceID}} {
		info, err := f.svc.SubmitSyncTask(ctx, f.owner, f.projectID, f.entryID, GlossarySyncExecuteInput{OldTarget: "旧", NewTarget: "新", ResourceIDs: resources})
		if err != nil {
			t.Fatal(err)
		}
		task := f.client.SyncTask.GetX(ctx, info.TaskID)
		var actual []int
		if err := json.Unmarshal([]byte(task.ResourceIds), &actual); err != nil || len(actual) != 1 || actual[0] != f.resourceID {
			t.Fatalf("actual resources=%s err=%v", task.ResourceIds, err)
		}
		if _, err := f.svc.GetSyncTaskStatus(ctx, other.ID, f.projectID, task.ID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("foreign read=%v", err)
		}
		if _, err := f.svc.CancelSyncTask(ctx, other.ID, f.projectID, task.ID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("foreign cancel=%v", err)
		}
		if _, err := f.svc.GetSyncTaskStatus(ctx, other.ID, foreignProject, task.ID); !errors.Is(err, ErrSyncTaskNotFound) {
			t.Fatalf("wrong project=%v", err)
		}
	}
	for _, id := range []int{0, -1} {
		if _, err := f.svc.GetSyncTaskStatus(ctx, f.owner, f.projectID, id); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("read ID=%d err=%v", id, err)
		}
		if _, err := f.svc.CancelSyncTask(ctx, f.owner, f.projectID, id); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("cancel ID=%d err=%v", id, err)
		}
	}
}

func TestGlossarySyncLifecycleOrganizationRolesAndRevocation(t *testing.T) {
	ctx := context.Background()
	org := newOrganizationFixture(t)
	f := seedSyncLifecycle(t, org.client, 1)
	org.client.Project.UpdateOneID(f.projectID).ClearOwnerUserID().SetOwnerOrgID(org.org.ID).ExecX(ctx)
	f.owner = org.owner.ID
	task := f.submit(t)
	for _, userID := range []int{org.owner.ID, org.admin.ID, org.member.ID} {
		if _, err := f.svc.GetSyncTaskStatus(ctx, userID, f.projectID, task.ID); err != nil {
			t.Fatalf("read %d: %v", userID, err)
		}
		if _, err := f.svc.AnalyzeSyncImpact(ctx, userID, f.projectID, f.entryID, GlossarySyncImpactInput{OldTarget: "旧"}); err != nil {
			t.Fatalf("impact %d: %v", userID, err)
		}
	}
	for _, userID := range []int{org.member.ID, org.other.ID} {
		if _, err := f.svc.SubmitSyncTask(ctx, userID, f.projectID, f.entryID, GlossarySyncExecuteInput{OldTarget: "旧", NewTarget: "新"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("submit %d: %v", userID, err)
		}
		if _, err := f.svc.CancelSyncTask(ctx, userID, f.projectID, task.ID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("cancel %d: %v", userID, err)
		}
	}
	info, err := f.svc.SubmitSyncTask(ctx, org.admin.ID, f.projectID, f.entryID, GlossarySyncExecuteInput{OldTarget: "旧", NewTarget: "新"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CancelSyncTask(ctx, org.admin.ID, f.projectID, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := org.svc.RemoveMember(ctx, org.owner.ID, org.org.ID, org.admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetSyncTaskStatus(ctx, org.admin.ID, f.projectID, info.TaskID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked creator read=%v", err)
	}
}

func TestGlossarySyncLifecycleResumeSkipsMissingAndChangedSegments(t *testing.T) {
	ctx := context.Background()
	f := seedSyncLifecycle(t, testClient(t), 205)
	task := f.submit(t)
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.client.SyncTask.UpdateOneID(task.ID).SetStatus(SyncTaskStatusRunning).SetStartedAt(started).ExecX(ctx)
	done, err := f.svc.executeSyncBatch(ctx, task.ID)
	if err != nil || done {
		t.Fatalf("first batch done=%v err=%v", done, err)
	}
	first := f.client.SyncTask.GetX(ctx, task.ID)
	if first.NextSegmentIndex != 100 || first.ProcessedSegments != 100 {
		t.Fatalf("checkpoint=%+v", first)
	}
	f.client.Segment.DeleteOneID(f.segmentIDs[100]).ExecX(ctx)
	f.client.Segment.UpdateOneID(f.segmentIDs[101]).ClearTargetText().ExecX(ctx)
	f.client.Segment.UpdateOneID(f.segmentIDs[102]).SetStatus(segment.StatusPending).ExecX(ctx)
	f.client.Segment.UpdateOneID(f.segmentIDs[103]).SetSourceText("unrelated").ExecX(ctx)
	for i := 0; i < 2; i++ {
		if err := f.svc.PrepareRecovery(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.ExecuteSyncTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	finished := f.client.SyncTask.GetX(ctx, task.ID)
	if finished.Status != SyncTaskStatusCompleted || finished.StartedAt == nil || !finished.StartedAt.Equal(started) || finished.NextSegmentIndex != 205 {
		t.Fatalf("finished=%+v", finished)
	}
	result := syncTaskResult(t, finished)
	if result.TotalUpdated != 201 || result.TotalSkipped != 4 {
		t.Fatalf("result=%+v", result)
	}
	for _, id := range append(append([]int{}, f.segmentIDs[:100]...), f.segmentIDs[104:]...) {
		row := f.client.Segment.GetX(ctx, id)
		if row.TargetText == nil || *row.TargetText != "旧新" {
			t.Fatalf("segment %d repeated replacement: %v", id, row.TargetText)
		}
	}
	if err := f.svc.ExecuteSyncTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if count := f.client.ActivityLog.Query().Where(activitylog.ActionEQ("glossary.sync_execute")).CountX(ctx); count != 1 {
		t.Fatalf("duplicate completion audit=%d", count)
	}
}

func TestGlossarySyncLifecycleBatchRollback(t *testing.T) {
	ctx := context.Background()
	f := seedSyncLifecycle(t, testClient(t), 3)
	task := f.submit(t)
	f.client.SyncTask.UpdateOneID(task.ID).SetStatus(SyncTaskStatusRunning).ExecX(ctx)
	fail := true
	attempted := 0
	f.client.Segment.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			attempted++
			if fail && attempted == 3 {
				return nil, errors.New("injected segment write failure")
			}
			return next.Mutate(ctx, mutation)
		})
	})
	if _, err := f.svc.executeSyncBatch(ctx, task.ID); err == nil {
		t.Fatal("expected rollback")
	}
	row := f.client.SyncTask.GetX(ctx, task.ID)
	if row.ProcessedSegments != 0 || row.NextSegmentIndex != 0 || row.Result != task.Result {
		t.Fatalf("checkpoint committed independently: %+v", row)
	}
	for _, id := range f.segmentIDs {
		if *f.client.Segment.GetX(ctx, id).TargetText != "旧" {
			t.Fatal("segment survived rollback")
		}
	}
	fail = false
	if done, err := f.svc.executeSyncBatch(ctx, task.ID); err != nil || !done {
		t.Fatalf("retry done=%v err=%v", done, err)
	}
}

func TestGlossarySyncLifecycleCancellationAndShutdown(t *testing.T) {
	ctx := context.Background()
	for _, initial := range []string{SyncTaskStatusPending, SyncTaskStatusRunning} {
		t.Run(initial, func(t *testing.T) {
			f := seedSyncLifecycle(t, testClient(t), 101)
			task := f.submit(t)
			if initial == SyncTaskStatusRunning {
				f.client.SyncTask.UpdateOneID(task.ID).SetStatus(initial).ExecX(ctx)
				if _, err := f.svc.executeSyncBatch(ctx, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			cancelled, err := f.svc.CancelSyncTask(ctx, f.owner, f.projectID, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			again, err := f.svc.CancelSyncTask(ctx, f.owner, f.projectID, task.ID)
			if err != nil || !again.UpdatedAt.Equal(cancelled.UpdatedAt) || !again.CancelledAt.Equal(*cancelled.CancelledAt) {
				t.Fatalf("repeat cancel changed task: %v", err)
			}
			if err := f.svc.ExecuteSyncTask(ctx, task.ID); err != nil {
				t.Fatal(err)
			}
			if done, err := f.svc.executeSyncBatch(ctx, task.ID); err != nil || !done {
				t.Fatalf("late batch: done=%v err=%v", done, err)
			}
			_ = f.svc.FailSyncTask(ctx, task.ID, errors.New("late failure"))
			final := f.client.SyncTask.GetX(ctx, task.ID)
			if final.Status != SyncTaskStatusCancelled || final.Result != cancelled.Result || final.ProcessedSegments != cancelled.ProcessedSegments {
				t.Fatalf("terminal overwritten: %+v", final)
			}
			if *f.client.Segment.GetX(ctx, f.segmentIDs[100]).TargetText != "旧" {
				t.Fatal("new batch committed after cancellation")
			}
		})
	}
	f := seedSyncLifecycle(t, testClient(t), 1)
	task := f.submit(t)
	f.client.SyncTask.UpdateOneID(task.ID).SetStatus(SyncTaskStatusRunning).ExecX(ctx)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_ = f.svc.FailSyncTask(cancelled, task.ID, context.Canceled)
	if row := f.client.SyncTask.GetX(ctx, task.ID); row.Status != SyncTaskStatusRunning {
		t.Fatalf("shutdown changed status=%s", row.Status)
	}
	for _, terminal := range []string{SyncTaskStatusCompleted, SyncTaskStatusFailed} {
		f.client.SyncTask.UpdateOneID(task.ID).SetStatus(terminal).ExecX(ctx)
		if _, err := f.svc.CancelSyncTask(ctx, f.owner, f.projectID, task.ID); !errors.Is(err, ErrSyncTaskStateConflict) {
			t.Fatalf("cancel %s=%v", terminal, err)
		}
	}
}

func TestGlossarySyncLifecycleLegacyRecoveryAndMigration(t *testing.T) {
	ctx := context.Background()
	f := seedSyncLifecycle(t, testClient(t), 1)
	var tasks []*ent.SyncTask
	for _, scenario := range []string{"pending", "running", "progress", "terminal", "invalid_version", "invalid_position"} {
		task := f.submit(t)
		update := f.client.SyncTask.UpdateOneID(task.ID).SetCheckpointVersion(0).SetResult("")
		switch scenario {
		case "running":
			update.SetStatus(SyncTaskStatusRunning)
		case "progress":
			update.SetProcessedSegments(1)
		case "terminal":
			update.SetStatus(SyncTaskStatusCompleted).SetResult(`{"total_updated":1,"total_skipped":0}`)
		case "invalid_version":
			update.SetCheckpointVersion(9)
		case "invalid_position":
			update.SetCheckpointVersion(1).SetNextSegmentIndex(10)
		}
		tasks = append(tasks, update.SaveX(ctx))
	}
	for i := 0; i < 2; i++ {
		if err := f.client.Schema.Create(ctx); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.PrepareRecovery(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for i, old := range tasks {
		row := f.client.SyncTask.GetX(ctx, old.ID)
		switch i {
		case 0:
			if row.Status != SyncTaskStatusPending || row.CheckpointVersion != 1 || row.StartedAt != nil {
				t.Fatalf("legacy pending=%+v", row)
			}
		case 3:
			if row.Status != old.Status || row.Result != old.Result || !row.UpdatedAt.Equal(old.UpdatedAt) || row.StartedAt != nil {
				t.Fatalf("history modified=%+v", row)
			}
		default:
			if row.Status != SyncTaskStatusFailed || row.Error != syncRecoveryFailure {
				t.Fatalf("untrusted row=%+v", row)
			}
		}
	}
	ids, err := f.svc.PendingTaskIDs(ctx, 0, 1)
	if err != nil || len(ids) != 1 || ids[0] != tasks[0].ID {
		t.Fatalf("pending=%v err=%v", ids, err)
	}
	ids, err = f.svc.PendingTaskIDs(ctx, ids[0], 1)
	if err != nil || len(ids) != 0 {
		t.Fatalf("pending second page=%v err=%v", ids, err)
	}
	old := tasks[0]
	longPending := f.client.SyncTask.Create().SetProjectID(f.projectID).SetActorUserID(f.owner).SetEntryID(f.entryID).
		SetOldTarget(old.OldTarget).SetNewTarget(old.NewTarget).SetTotalSegments(old.TotalSegments).
		SetSegmentIds(old.SegmentIds).SetResourceIds(old.ResourceIds).SetCreatedAt(time.Now().Add(-72 * time.Hour)).SaveX(ctx)
	if err := f.svc.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CleanupExpiredTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if row := f.client.SyncTask.GetX(ctx, longPending.ID); row.Status != SyncTaskStatusPending {
		t.Fatalf("old creation age changed status=%s", row.Status)
	}
	if count := f.client.SyncTask.Query().Where(synctask.StatusEQ(SyncTaskStatusPending)).CountX(ctx); count != 2 {
		t.Fatalf("cleanup changed pending=%d", count)
	}
}
