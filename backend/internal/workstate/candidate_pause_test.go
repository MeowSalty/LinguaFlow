package workstate

import (
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
)

func TestPausedCandidateRequiresAdmittedAlignmentInCurrentEpoch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     string
		stage     string
		epoch     int64
		candidate string
		other     bool
		allowed   bool
	}{
		{name: "sent", state: "sent", stage: "alignment", epoch: 1, candidate: "candidate-1", allowed: true},
		{name: "received", state: "received", stage: "ruby_alignment", epoch: 1, candidate: "candidate-1", allowed: true},
		{name: "completed", state: "completed", stage: "ruby_alignment", epoch: 1, candidate: "candidate-1", allowed: true},
		{name: "failed", state: "failed", stage: "alignment", epoch: 1, candidate: "candidate-1", allowed: true},
		{name: "reserved", state: "reserved", stage: "alignment", epoch: 1, candidate: "candidate-1"},
		{name: "unknown", state: "unknown", stage: "alignment", epoch: 1, candidate: "candidate-1"},
		{name: "refunded", state: "cancelled_before_dispatch", stage: "alignment", epoch: 1, candidate: "candidate-1"},
		{name: "old_epoch", state: "received", stage: "alignment", epoch: 0, candidate: "candidate-1"},
		{name: "other_candidate", state: "received", stage: "alignment", epoch: 1, candidate: "candidate-2"},
		{name: "other_member", state: "received", stage: "alignment", epoch: 1, candidate: "candidate-1", other: true},
		{name: "unrelated_main", state: "received", stage: "main", epoch: 1, candidate: "candidate-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, client, store, scope, seg := fixture(t)
			candidate := ready(scope, seg)
			candidate.State = "pending_alignment"
			if err := store.SaveCandidate(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			client.Job.UpdateOneID(scope.JobID).SetRetryEpoch(1).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
			client.WorkItem.Update().SetRetryEpoch(1).ExecX(ctx)
			candidate.Scope.RetryEpoch = 1
			memberID := seg.ID
			if tc.other {
				memberID++
			}
			client.WorkRequest.Create().SetIdentity("proof").SetJobID(scope.JobID).
				SetResourceID(scope.ResourceID).SetJobRoundID(scope.RoundID).SetRetryEpoch(tc.epoch).
				SetCandidateID(tc.candidate).SetSegmentIds([]int{memberID}).SetStage(tc.stage).
				SetBackendID(1).SetBudgetModel("stage_separated").SetInputDigest("alignment").SetState(tc.state).ExecX(ctx)
			candidate.Version++
			candidate.State = "ready_to_commit"
			candidate.Payload = []byte(`{"version":2}`)
			err := store.SaveCandidate(ctx, candidate)
			if tc.allowed {
				if err != nil {
					t.Fatalf("admitted alignment save: %v", err)
				}
				if err := store.SaveCandidate(ctx, candidate); err != nil {
					t.Fatalf("handoff replay: %v", err)
				}
			} else if !errors.Is(err, ErrStopped) {
				t.Fatalf("unattributed alignment save: %v", err)
			}
			saved := client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(candidate.ID)).OnlyX(ctx)
			wantVersion := int64(1)
			if tc.allowed {
				wantVersion = 2
			}
			if saved.Version != wantVersion {
				t.Fatalf("candidate version=%d, want %d", saved.Version, wantVersion)
			}
			if client.JobRoundSegment.Query().CountX(ctx) != 0 || client.Job.GetX(ctx, scope.JobID).ProgressCompleted != 0 {
				t.Fatal("candidate handoff changed completion facts")
			}
		})
	}
}

func TestPausedNewCandidateCannotUseAlignmentAsParent(t *testing.T) {
	ctx, client, store, scope, seg := fixture(t)
	request := Request{ID: "alignment", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "alignment", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "alignment", AlignmentAttempt: true}
	if err := store.ReserveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRequest(ctx, request.ID, RequestResult{State: "received"}); err != nil {
		t.Fatal(err)
	}
	client.Job.UpdateOneID(scope.JobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
	candidate := ready(scope, seg)
	candidate.ParentRequestID = request.ID
	if err := store.SaveCandidate(ctx, candidate); !errors.Is(err, ErrStopped) {
		t.Fatalf("new candidate with alignment parent: %v", err)
	}
	if client.WorkCandidate.Query().CountX(ctx) != 0 {
		t.Fatal("alignment request created an unattributed main candidate")
	}
}
