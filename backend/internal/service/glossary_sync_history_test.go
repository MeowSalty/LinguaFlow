package service

import (
	"context"
	"errors"
	"testing"
)

func TestGlossarySyncHistoryTerminalTimes(t *testing.T) {
	for _, kind := range []string{"completed", "failed", "cancelled", "invalid_checkpoint"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f := seedSyncLifecycle(t, testClient(t), 1)
			row := f.submit(t)
			if row.FinishedAt != nil || row.RetentionAnchorAt != nil {
				t.Fatal("new task has terminal time")
			}
			switch kind {
			case "completed":
				if err := f.svc.ExecuteSyncTask(ctx, row.ID); err != nil {
					t.Fatal(err)
				}
			case "failed":
				cause := errors.New("sync failure")
				if err := f.svc.FailSyncTask(ctx, row.ID, cause); !errors.Is(err, cause) {
					t.Fatal(err)
				}
			case "cancelled":
				if _, err := f.svc.CancelSyncTask(ctx, f.owner, f.projectID, row.ID); err != nil {
					t.Fatal(err)
				}
			case "invalid_checkpoint":
				f.client.SyncTask.UpdateOneID(row.ID).SetCheckpointVersion(9).ExecX(ctx)
				if err := f.svc.PrepareRecovery(ctx); err != nil {
					t.Fatal(err)
				}
			}
			finished := f.client.SyncTask.GetX(ctx, row.ID)
			if finished.FinishedAt == nil || finished.RetentionAnchorAt == nil || !finished.FinishedAt.Equal(*finished.RetentionAnchorAt) {
				t.Fatalf("terminal timing=%+v", finished)
			}
			if kind == "cancelled" && (finished.CancelledAt == nil || !finished.CancelledAt.Equal(*finished.FinishedAt)) {
				t.Fatal("cancellation timestamps disagree")
			}
			_ = f.svc.FailSyncTask(ctx, row.ID, errors.New("late failure"))
			if err := f.svc.PrepareRecovery(ctx); err != nil {
				t.Fatal(err)
			}
			again := f.client.SyncTask.GetX(ctx, row.ID)
			if again.Status != finished.Status || !again.FinishedAt.Equal(*finished.FinishedAt) || !again.RetentionAnchorAt.Equal(*finished.RetentionAnchorAt) {
				t.Fatal("late callback changed terminal timing")
			}
		})
	}
}
