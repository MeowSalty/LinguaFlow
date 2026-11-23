package service

import (
	"context"
	stdsql "database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctaskitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctaskresult"
)

type syncPlanQueryRecorder struct{ *jobQueryRecorder }
type syncPlanQueryTx struct {
	dialect.Tx
	owner *syncPlanQueryRecorder
}

func (r *syncPlanQueryRecorder) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := r.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &syncPlanQueryTx{Tx: tx, owner: r}, nil
}

func (tx *syncPlanQueryTx) Query(ctx context.Context, query string, args, v any) error {
	tx.owner.queries = append(tx.owner.queries, query)
	if values, ok := args.([]any); ok {
		tx.owner.args = append(tx.owner.args, append([]any(nil), values...))
	} else {
		tx.owner.args = append(tx.owner.args, nil)
	}
	return tx.Tx.Query(ctx, query, args, v)
}

func syncPlanRecordingClient(t *testing.T) (*ent.Client, *syncPlanQueryRecorder) {
	t.Helper()
	db, err := stdsql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	recorder := &syncPlanQueryRecorder{jobQueryRecorder: &jobQueryRecorder{Driver: database.NewDriver(entsql.OpenDB(dialect.SQLite, db))}}
	client := ent.NewClient(ent.Driver(recorder))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	return client, recorder
}

func TestGlossarySyncPlanPagesAndIncrementalResult(t *testing.T) {
	ctx := context.Background()
	c, recorder := syncPlanRecordingClient(t)
	f := seedSyncLifecycle(t, c, 205)
	recorder.queries, recorder.args = nil, nil
	task := f.submit(t)
	identityPages := 0
	for _, query := range recorder.queries {
		if !strings.Contains(query, "FROM `segments`") || !strings.Contains(query, "ORDER BY") || strings.Contains(query, "DESC") {
			continue
		}
		identityPages++
		projection := strings.Split(query, " FROM ")[0]
		if strings.Contains(projection, "source_text") || strings.Contains(projection, "target_text") || !strings.Contains(query, "LIMIT 100") {
			t.Fatalf("submission loads a body or unbounded page: %s", query)
		}
	}
	if identityPages != 4 { // Three populated pages and one empty bounded page.
		t.Fatalf("identity pages=%d want=4", identityPages)
	}
	c.SyncTask.UpdateOneID(task.ID).SetStatus(SyncTaskStatusRunning).ExecX(ctx)
	recorder.queries, recorder.args = nil, nil
	if done, err := f.svc.executeSyncBatch(ctx, task.ID); err != nil || done {
		t.Fatalf("batch: done=%v err=%v", done, err)
	}
	planReads := 0
	for _, query := range recorder.queries {
		if strings.Contains(query, "FROM `sync_task_items`") {
			planReads++
			if !strings.Contains(query, "LIMIT 100") || strings.Contains(query, "COUNT(") || !strings.Contains(query, "ordinal` >=") {
				t.Fatalf("batch reloads full plan: %s", query)
			}
		}
		if strings.Contains(query, "FROM `sync_tasks`") && strings.Contains(query, "checkpoint_version") && strings.Contains(query, "`result`") {
			t.Fatalf("batch header loads accumulated JSON: %s", query)
		}
		if strings.Contains(query, "FROM `segments`") && strings.Contains(strings.Split(query, " FROM ")[0], "`meta`") {
			t.Fatalf("batch loads unrelated metadata: %s", query)
		}
	}
	if planReads != 1 {
		t.Fatalf("plan reads=%d want=1", planReads)
	}
	header := c.SyncTask.GetX(ctx, task.ID)
	if header.Result != "" || header.TotalUpdated != 100 || header.TotalSkipped != 0 {
		t.Fatalf("batch rewrote result JSON or lost scalar counts: %+v", header)
	}
	resourceResult := c.SyncTaskResult.Query().Where(synctaskresult.TaskIDEQ(task.ID)).OnlyX(ctx)
	if resourceResult.UpdatedCount != 100 || resourceResult.ResourcePath != "sync.txt" {
		t.Fatalf("resource result=%+v", resourceResult)
	}
	apiSnapshot, err := f.svc.GetSyncTaskStatus(ctx, f.owner, f.projectID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var result glossarySyncResultJSON
	if err := json.Unmarshal([]byte(apiSnapshot.Result), &result); err != nil || result.TotalUpdated != 100 || len(result.Resources) != 1 {
		t.Fatalf("API snapshot=%s err=%v", apiSnapshot.Result, err)
	}
	if c.SyncTask.GetX(ctx, task.ID).Result != "" {
		t.Fatal("read-only API snapshot wrote JSON")
	}
}

func TestGlossarySyncPublishedActiveMigrationUsesLatestResult(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	f := seedSyncLifecycle(t, c, 205)
	for _, column := range []string{"segment_ids", "resource_ids"} {
		if _, err := c.ExecContext(ctx, "ALTER TABLE sync_tasks ADD COLUMN "+column+" TEXT NOT NULL DEFAULT '[]'"); err != nil {
			t.Fatal(err)
		}
	}
	// Reproduce a published cp=1 task after its first committed batch. The
	// old JSON is valid and nonempty, and must be copied into final result rows.
	if n, err := c.Segment.Update().Where(segment.IDIn(f.segmentIDs[:100]...)).SetTargetText("旧新").SetStatus(segment.StatusEdited).Save(ctx); err != nil || n != 100 {
		t.Fatalf("seed committed content: count=%d err=%v", n, err)
	}
	c.Resource.UpdateOneID(f.resourceID).SetTranslationGeneration(1).ExecX(ctx)
	legacyResult := glossarySyncResultJSON{TotalUpdated: 100, Resources: []*GlossarySyncExecuteResourceResult{{ResourceID: f.resourceID, ResourcePath: "legacy/sync.txt", UpdatedCount: 100}}}
	resultJSON, err := json.Marshal(legacyResult)
	if err != nil {
		t.Fatal(err)
	}
	task := c.SyncTask.Create().SetProjectID(f.projectID).SetEntryID(f.entryID).SetActorUserID(f.owner).
		SetOldTarget("旧").SetNewTarget("旧新").SetTotalSegments(205).SetProcessedSegments(100).SetNextSegmentIndex(100).
		SetCheckpointVersion(1).SetStatus(SyncTaskStatusRunning).SetStartedAt(time.Now().UTC()).SetResult(string(resultJSON)).SaveX(ctx)
	idsJSON, err := json.Marshal(f.segmentIDs)
	if err != nil {
		t.Fatal(err)
	}
	resourcesJSON, err := json.Marshal([]int{f.resourceID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecContext(ctx, "UPDATE sync_tasks SET segment_ids=$1,resource_ids=$2 WHERE id=$3", string(idsJSON), string(resourcesJSON), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.MigratePublishedData(ctx, c, dialect.SQLite); err != nil {
		t.Fatal(err)
	}
	if done, err := f.svc.executeSyncBatch(ctx, task.ID); err != nil || done {
		t.Fatalf("post-migration batch: done=%v err=%v", done, err)
	}
	header := c.SyncTask.GetX(ctx, task.ID)
	if header.CheckpointVersion != syncCheckpointVersion || header.Status != SyncTaskStatusRunning || header.NextSegmentIndex != 200 || header.TotalUpdated != 200 || header.Result != "" {
		t.Fatalf("migrated active checkpoint/result: %+v", header)
	}
	assertLatest := func(label string, snapshot *ent.SyncTask) {
		t.Helper()
		var result glossarySyncResultJSON
		if err := json.Unmarshal([]byte(snapshot.Result), &result); err != nil {
			t.Fatalf("%s result: %v", label, err)
		}
		if result.TotalUpdated != 200 || result.TotalSkipped != 0 || len(result.Resources) != 1 || result.Resources[0].UpdatedCount != 200 || result.Resources[0].SkippedCount != 0 || result.Resources[0].ResourcePath != "legacy/sync.txt" {
			t.Fatalf("%s returned stale or changed result: %+v", label, result)
		}
	}
	snapshot, err := f.svc.GetSyncTaskStatus(ctx, f.owner, f.projectID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertLatest("active API", snapshot)
	if c.SyncTask.GetX(ctx, task.ID).Result != "" {
		t.Fatal("active API rewrote the migrated JSON")
	}
	cancelled, err := f.svc.CancelSyncTask(ctx, f.owner, f.projectID, task.ID)
	if err != nil || cancelled.Status != SyncTaskStatusCancelled {
		t.Fatalf("cancel: task=%+v err=%v", cancelled, err)
	}
	assertLatest("cancel response", cancelled)
	assertLatest("persisted terminal", c.SyncTask.GetX(ctx, task.ID))
	if *c.Segment.GetX(ctx, f.segmentIDs[200]).TargetText != "旧" {
		t.Fatal("cancellation consumed an unprocessed plan position")
	}
}

func TestGlossarySyncPlanIdentityAndDeletedPosition(t *testing.T) {
	ctx := context.Background()
	f := seedSyncLifecycle(t, testClient(t), 101)
	task := f.submit(t)
	for _, row := range []struct{ ordinal, segment int }{{0, f.segmentIDs[100]}, {101, f.segmentIDs[0]}} {
		_, err := f.client.SyncTaskItem.Create().SetTaskID(task.ID).SetOrdinal(row.ordinal).SetSegmentID(row.segment).SetResourceID(f.resourceID).Save(ctx)
		if !ent.IsConstraintError(err) {
			t.Fatalf("duplicate plan identity was accepted: %v", err)
		}
	}
	f.client.Segment.DeleteOneID(f.segmentIDs[100]).ExecX(ctx)
	if n := f.client.SyncTaskItem.Query().Where(synctaskitem.TaskIDEQ(task.ID)).CountX(ctx); n != 101 {
		t.Fatalf("deletion changed plan length=%d", n)
	}
	if err := f.svc.ExecuteSyncTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	final := f.client.SyncTask.GetX(ctx, task.ID)
	if final.NextSegmentIndex != 101 || final.TotalUpdated != 100 || final.TotalSkipped != 1 {
		t.Fatalf("deleted position was not consumed: %+v", final)
	}
	f.client.SyncTask.DeleteOneID(task.ID).ExecX(ctx)
	if f.client.SyncTaskItem.Query().Where(synctaskitem.TaskIDEQ(task.ID)).CountX(ctx) != 0 || f.client.SyncTaskResult.Query().Where(synctaskresult.TaskIDEQ(task.ID)).CountX(ctx) != 0 {
		t.Fatal("task deletion retained plan/results")
	}
}

func TestGlossarySyncPlanGapAndBuildCapacityRollback(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"gap", "capacity"} {
		t.Run(scenario, func(t *testing.T) {
			f := seedSyncLifecycle(t, testClient(t), 3)
			task := f.submit(t)
			f.client.SyncTask.UpdateOneID(task.ID).SetStatus(SyncTaskStatusRunning).ExecX(ctx)
			batchCtx := ctx
			if scenario == "gap" {
				f.client.SyncTaskItem.Delete().Where(synctaskitem.TaskIDEQ(task.ID), synctaskitem.OrdinalEQ(1)).ExecX(ctx)
			} else {
				batchCtx = backend.WithBuildAllocation(ctx, 1, nil, nil)
			}
			if _, err := f.svc.executeSyncBatch(batchCtx, task.ID); err == nil || scenario == "capacity" && !errors.Is(err, backend.ErrBuildCapacity) {
				t.Fatalf("invalid batch accepted: %v", err)
			}
			header := f.client.SyncTask.GetX(ctx, task.ID)
			if header.ProcessedSegments != 0 || header.TotalUpdated != 0 || header.TotalSkipped != 0 {
				t.Fatalf("bad batch advanced checkpoint: %+v", header)
			}
			for _, id := range f.segmentIDs {
				if *f.client.Segment.GetX(ctx, id).TargetText != "旧" {
					t.Fatal("bad batch retained segment mutation")
				}
			}
		})
	}
}

func TestGlossarySyncPlanFoldedReplacementKeepsUnicodeOffsets(t *testing.T) {
	b, _ := backend.NewBuildBounds(1024, 1024)
	defer b.CloseRetired()
	got, changed, err := syncCaseInsensitiveReplace(b, "İTERM İterm", "iterm", "新")
	if err != nil || !changed || got != "新 新" {
		t.Fatalf("replacement=%q changed=%v err=%v", got, changed, err)
	}
}
