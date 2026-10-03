package service

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
)

func seedOperationStorage(t *testing.T, c *ent.Client, projectID, actor int, status string, at time.Time) *ent.StorageTask {
	t.Helper()
	key := fmt.Sprintf("storage-%d-%d-%s", projectID, actor, status)
	return c.StorageTask.Create().SetOperationID(key).SetIdempotencyKey(key).SetRequestHash("hash").SetProjectID(projectID).SetActorID(actor).
		SetKind("repair").SetStatus(storagetask.Status(status)).SetInput(map[string]any{"private": "private-secret"}).SetPhase("verify").SetErrorCode("storage_auth_required").SetUpdatedAt(at).SaveX(context.Background())
}

func TestStorageOperationProjectionAndPermissions(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	actor := createTestUser(t, c, "storage-ops")
	other := createTestUser(t, c, "storage-other")
	p := createTestProject(t, c, "mixed-storage", actor.ID)
	hidden := createTestProject(t, c, "hidden", other.ID)
	at := time.Now().UTC().Add(-time.Minute)
	for _, status := range []string{"pending", "running", "waiting_retry", "needs_action", "completed", "failed", "cancelled"} {
		seedOperationStorage(t, c, p.ID, actor.ID, status, at)
		seedOperationStorage(t, c, hidden.ID, other.ID, status, at)
	}
	seedQueryJob(t, c, p.ID, "pending", "manual", at)
	seedOperationSync(t, c, p.ID, actor.ID, "pending", at)
	svc := NewOperationQueryService(c)
	all, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{State: "all"}})
	if err != nil || len(all.Items) != 9 {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	var got []string
	cursor := ""
	for {
		page, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{State: "all", Limit: 1, Cursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, operationKeys(page.Items)...)
		for _, row := range page.Items {
			if row.ProjectID() != p.ID || row.ProjectName != p.Name {
				t.Fatalf("wrong project: %+v", row)
			}
			if row.StorageTask != nil && row.StorageTask.Input != nil {
				t.Fatal("task input leaked into projection")
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if !slices.Equal(got, operationKeys(all.Items)) {
		t.Fatalf("mixed pagination=%v", got)
	}
	active, err := svc.List(ctx, actor.ID, OperationListOptions{TaskType: OperationStorage})
	if err != nil || len(active.Items) != 4 {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	for _, status := range []string{"waiting_retry", "needs_action"} {
		page, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{Status: status}})
		if err != nil || len(page.Items) != 1 || page.Items[0].TaskType != OperationStorage {
			t.Fatalf("status=%s page=%+v err=%v", status, page, err)
		}
	}
	summary, err := svc.Summary(ctx, actor.ID, OperationSummaryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ByType.Storage != (OperationCounts{Pending: 1, Running: 1, WaitingRetry: 1, NeedsAction: 1, RecentFailed: 1}) || summary.Total.Pending != 3 {
		t.Fatalf("summary=%+v", summary)
	}
	// 每页都会检查当前项目鉴权。
	c.Project.UpdateOneID(p.ID).SetOwnerUserID(other.ID).ExecX(ctx)
	// 存储事实在项目删除后仍保留，但不再能通过项目访问被发现。
	deleted := createTestProject(t, c, "deleted-storage-project", actor.ID)
	seedOperationStorage(t, c, deleted.ID, actor.ID, "pending", at)
	c.Project.DeleteOneID(deleted.ID).ExecX(ctx)
	page, err := svc.List(ctx, actor.ID, OperationListOptions{TaskType: OperationStorage, AccessibleJobListOptions: AccessibleJobListOptions{State: "all"}})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("deleted project tasks=%+v err=%v", page, err)
	}
}
