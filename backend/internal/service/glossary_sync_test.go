package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
)

// seedSyncTaskFixture 构建可直接执行 ExecuteSyncTask 的最小闭环：
// 项目 + 术语条目 + 指定格式的资源 + 一条含旧术语的已译段落 + pending 同步任务。
// 返回任务 ID 与段落，供断言替换结果。
func seedSyncTaskFixture(t *testing.T, format string) (taskID int, seg *ent.Segment, client *ent.Client) {
	t.Helper()
	client = testClient(t)
	projectID := seedProject(t, client, seedSyncUser(t, client))

	entry, err := client.GlossaryEntry.Create().
		SetProjectID(projectID).
		SetSourceKey(glossarySourceKey("雷神")).
		SetSource("雷神").
		SetTarget("雷神").
		SetCaseSensitive(false).
		SetForbidden(false).
		SetMandatory(true).
		SetNotes("").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create glossary entry: %v", err)
	}

	res, err := client.Resource.Create().
		SetProjectID(projectID).
		SetPath("book." + format).
		SetFormat(format).
		SetStoragePath("storage/book." + format).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create resource: %v", err)
	}

	// 段译文含 ruby 标签；旧术语「雷神<rt>らいじん」横跨标签边界，替换会把
	// </rt> 劈成多余闭标签——这正是结构守卫要拦截的形态。
	seg, err = client.Segment.Create().
		SetResourceID(res.ID).
		SetSegmentIndex(0).
		SetSourceText("レベル６：雷神").
		SetTargetText("<ruby>雷神<rt>らいじん</rt></ruby>！").
		SetStatus(segment.StatusTranslated).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create segment: %v", err)
	}

	segmentIDs, err := json.Marshal([]int{seg.ID})
	if err != nil {
		t.Fatalf("marshal segment ids: %v", err)
	}
	task, err := client.SyncTask.Create().
		SetProjectID(projectID).
		SetEntryID(entry.ID).
		SetActorUserID(1).
		SetOldTarget("雷神<rt>らいじん").
		SetNewTarget("雷霆").
		SetTotalSegments(1).
		SetStatus(SyncTaskStatusPending).
		SetSegmentIds(string(segmentIDs)).
		SetResourceIds("[]").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create sync task: %v", err)
	}
	return task.ID, seg, client
}

func seedSyncUser(t *testing.T, client *ent.Client) int {
	t.Helper()
	u, err := client.User.Create().
		SetUsername("sync-user").
		SetPasswordHash("$2a$10$dummyhashvaluehere").
		SetEmail("sync@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u.ID
}

// syncTaskResult 解析任务 Result 字段中的结果摘要。
func syncTaskResult(t *testing.T, task *ent.SyncTask) GlossarySyncResult {
	t.Helper()
	var parsed struct {
		TotalUpdated int `json:"total_updated"`
		TotalSkipped int `json:"total_skipped"`
	}
	if err := json.Unmarshal([]byte(task.Result), &parsed); err != nil {
		t.Fatalf("unmarshal sync result %q: %v", task.Result, err)
	}
	return GlossarySyncResult{TotalUpdated: parsed.TotalUpdated, TotalSkipped: parsed.TotalSkipped}
}

// epub 资源上替换劈开标签：该段必须被跳过并计数，任务整体照常完成，
// 且数据库译文保持原样——坏结果不得落库。
func TestGlossarySyncExecute_EpubMarkupBreakingReplacementSkipped(t *testing.T) {
	taskID, seg, client := seedSyncTaskFixture(t, "epub")
	svc := NewGlossarySyncService(client, nil, nil, nil, discardLogger())

	if err := svc.ExecuteSyncTask(context.Background(), taskID); err != nil {
		t.Fatalf("ExecuteSyncTask: %v", err)
	}

	task, err := client.SyncTask.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != SyncTaskStatusCompleted {
		t.Fatalf("task status = %q, want completed（跳过不应使任务失败）", task.Status)
	}
	result := syncTaskResult(t, task)
	if result.TotalSkipped != 1 || result.TotalUpdated != 0 {
		t.Fatalf("result = %+v, want skipped=1 updated=0", result)
	}

	row, err := client.Segment.Get(context.Background(), seg.ID)
	if err != nil {
		t.Fatalf("get segment: %v", err)
	}
	if row.TargetText == nil || *row.TargetText != "<ruby>雷神<rt>らいじん</rt></ruby>！" {
		t.Fatalf("target = %v, want unchanged", row.TargetText)
	}
	if row.Status != segment.StatusTranslated {
		t.Errorf("status = %q, want translated（跳过段不得改状态）", row.Status)
	}
}

// 非 epub 格式不做结构校验：同样的替换照常生效（格式门禁不误伤直通格式）。
func TestGlossarySyncExecute_NonEpubReplacementApplies(t *testing.T) {
	taskID, seg, client := seedSyncTaskFixture(t, "txt")
	svc := NewGlossarySyncService(client, nil, nil, nil, discardLogger())

	if err := svc.ExecuteSyncTask(context.Background(), taskID); err != nil {
		t.Fatalf("ExecuteSyncTask: %v", err)
	}

	task, err := client.SyncTask.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	result := syncTaskResult(t, task)
	if result.TotalUpdated != 1 || result.TotalSkipped != 0 {
		t.Fatalf("result = %+v, want updated=1 skipped=0", result)
	}

	row, err := client.Segment.Get(context.Background(), seg.ID)
	if err != nil {
		t.Fatalf("get segment: %v", err)
	}
	if row.TargetText == nil || *row.TargetText != "<ruby>雷霆</rt></ruby>！" {
		t.Fatalf("target = %v, want replaced text", row.TargetText)
	}
	if row.Status != segment.StatusEdited {
		t.Errorf("status = %q, want edited", row.Status)
	}
}

// epub 上结构合法的替换照常生效：守卫只拦坏结果，不拦术语同步本身。
func TestGlossarySyncExecute_EpubWellFormedReplacementApplies(t *testing.T) {
	taskID, seg, client := seedSyncTaskFixture(t, "epub")
	task, err := client.SyncTask.UpdateOneID(taskID).
		SetOldTarget("雷神").
		SetNewTarget("雷霆").
		Save(context.Background())
	if err != nil {
		t.Fatalf("update task: %v", err)
	}
	svc := NewGlossarySyncService(client, nil, nil, nil, discardLogger())

	if err := svc.ExecuteSyncTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ExecuteSyncTask: %v", err)
	}

	row, err := client.Segment.Get(context.Background(), seg.ID)
	if err != nil {
		t.Fatalf("get segment: %v", err)
	}
	if row.TargetText == nil || *row.TargetText != "<ruby>雷霆<rt>らいじん</rt></ruby>！" {
		t.Fatalf("target = %v, want replaced text", row.TargetText)
	}
}
