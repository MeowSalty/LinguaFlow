package workstate

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestRubyHandoffProgressSurvivesRestartAndPause(t *testing.T) {
	ctx := context.Background()
	_, client, store, scope, seg := fixture(t)
	currentExecution(t, ctx, client, scope)
	r, h, c := rawRequest(scope, seg)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatal(err)
	}
	limits := backend.PipelineBudgetLimits{Segments: 1, Candidates: 128, Item: 1024, Responses: 1024, Handoff: 4096}
	first, err := store.BeginRuntimeProgress(ctx, scope.JobID, c.ID, r.ID, scope.ResourceID, 0, limits)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProgressID != c.ID || first.ResponseID != r.ID {
		t.Fatalf("first %+v", first)
	}
	client.Job.UpdateOneID(scope.JobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
	again, err := NewStore(client).BeginRuntimeProgress(ctx, scope.JobID, c.ID, r.ID, scope.ResourceID, 0, backend.PipelineBudgetLimits{Segments: 1, Candidates: 1, Item: 1024, Responses: 1024, Handoff: 2048})
	if err != nil || again != first {
		t.Fatalf("caps rolled %+v/%v", again, err)
	}
	if _, err := store.BeginRuntimeProgress(ctx, scope.JobID, "another", r.ID, scope.ResourceID, 0, limits); err == nil {
		t.Fatal("second progress owner admitted")
	}
	state, err := store.LoadRuntimeState(ctx, scope.JobID)
	if err != nil || state.ProgressID != c.ID {
		t.Fatalf("state %+v/%v", state, err)
	}
}
