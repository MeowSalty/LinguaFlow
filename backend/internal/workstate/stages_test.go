package workstate

import (
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

func TestReadStageCountsUsesCurrentEpochAndDurableWork(t *testing.T) {
	ctx, client, _, scope, seg := fixture(t)
	client.Job.UpdateOneID(scope.JobID).SetRetryEpoch(2).SetStatus("pausing").SetPauseRequested(true).SetProgressCompleted(7).ExecX(ctx)
	client.JobRoundSegment.Create().SetJobRoundID(scope.RoundID).SetSegmentID(seg.ID).ExecX(ctx)
	for i, request := range []struct {
		state, stage string
		epoch        int64
	}{
		{"sent", "main", 2},
		{"sent", "main", 2},
		{"sent", "alignment", 2},
		{"received", "main", 2},
		{"received", "alignment", 2},
		{"unknown", "ruby_alignment", 2},
		{"reserved", "main", 2},
		{"completed", "main", 2},
		{"sent", "ruby_alignment", 1},
		{"unknown", "main", 1},
		{"received", "main", 1},
	} {
		client.WorkRequest.Create().SetIdentity(string(rune('a' + i))).SetJobID(scope.JobID).
			SetResourceID(scope.ResourceID).SetJobRoundID(scope.RoundID).SetRetryEpoch(request.epoch).
			SetSegmentIds([]int{seg.ID}).SetStage(request.stage).SetBackendID(1).
			SetBudgetModel("stage_separated").SetInputDigest("frozen").SetState(request.state).SaveX(ctx)
	}
	item := client.WorkItem.Query().Where(workitem.SegmentIDEQ(seg.ID)).OnlyX(ctx)
	for i, state := range []string{"pending_alignment", "pending_alignment", "ready_to_commit", "completed", "stale"} {
		client.WorkCandidate.Create().SetIdentity(string(rune('k' + i))).SetWorkItemID(item.ID).
			SetDtoVersion(1).SetSnapshotDigest("frozen").SetMode("translate").SetSourceGeneration(0).
			SetBaselineVersion(seg.ContentVersion).SetBaselineStatus(string(seg.Status)).SetState(state).SaveX(ctx)
	}
	before := time.Now().Add(-time.Second)
	counts, err := ReadStageCounts(ctx, client, scope.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if counts.MainRequests != 2 || counts.AlignmentRequests != 1 || counts.SavingRequests != 2 || counts.UnknownRequests != 1 || counts.DrainingRequests != 5 || counts.PendingAlignment != 2 || counts.ReadyToCommit != 1 || counts.ConfirmedWork != 1 {
		t.Fatalf("incorrect stage observation: %+v", counts)
	}
	if counts.AsOf.Before(before) || counts.AsOf.After(time.Now()) {
		t.Fatalf("invalid observation timestamp: %v", counts.AsOf)
	}
	client.Job.UpdateOneID(scope.JobID).SetStatus("running").SetPauseRequested(false).ExecX(ctx)
	counts, err = ReadStageCounts(ctx, client, scope.JobID)
	if err != nil || counts.DrainingRequests != 0 || counts.MainRequests != 2 {
		t.Fatalf("running request counted as draining: %+v %v", counts, err)
	}
}

func TestReadStageCountsConfirmedFactsExcludeClosedUnresolvedWork(t *testing.T) {
	ctx, client, _, scope, seg := fixture(t)
	closed := client.JobRound.Create().SetJobID(scope.JobID).SetJobResourceID(scope.JobResourceID).
		SetRoundIndex(1).SetMode("translate").SetStatus("completed").SetSegmentTotal(2).SetSegmentCompleted(1).SaveX(ctx)
	client.JobRoundSegment.Create().SetJobRoundID(closed.ID).SetSegmentID(seg.ID).ExecX(ctx)
	// The legacy effective-progress contract closes a completed round to total,
	// including its unresolved member. Confirmed work must only report checkpoints.
	client.Job.UpdateOneID(scope.JobID).SetProgressTotal(3).SetProgressCompleted(2).ExecX(ctx)
	counts, err := ReadStageCounts(ctx, client, scope.JobID)
	if err != nil || counts.ConfirmedWork != 1 {
		t.Fatalf("closed unresolved work was counted as confirmed: %+v %v", counts, err)
	}
	client.JobRoundSegment.Create().SetJobRoundID(scope.RoundID).SetSegmentID(seg.ID).ExecX(ctx)
	counts, err = ReadStageCounts(ctx, client, scope.JobID)
	if err != nil || counts.ConfirmedWork != 2 {
		t.Fatalf("same segment in two rounds must have two completion facts: %+v %v", counts, err)
	}
	if client.Job.GetX(ctx, scope.JobID).ProgressCompleted != 2 {
		t.Fatal("diagnostic read changed the effective-progress contract")
	}
}
