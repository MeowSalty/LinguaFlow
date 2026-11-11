package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	pa "github.com/MeowSalty/LinguaFlow/backend/internal/persistenceacceptance"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestRubyHandoffAcceptanceRecoveryOriginalMemberBoundary(t *testing.T) {
	ctx := t.Context()
	barrier := &handoffHTTPBarrier{entered: make(chan struct{}, 4), release: make(chan struct{})}
	barrier.open()
	server := httptest.NewServer(http.HandlerFunc(barrier.serve))
	t.Cleanup(server.Close)
	_, db, _ := pa.Open(t)
	db.SetMaxOpenConns(1)
	// The runner's services use database/sql transaction methods directly;
	// this boundary needs the same production driver, without probe wrapping.
	client := ent.NewClient(ent.Driver(database.NewDriver(sql.OpenDB(pa.Driver(), db))))
	t.Cleanup(func() { _ = client.Close() })
	f := newRubyPipelineFixtureWithDriver(t, client, pa.Driver(), server.URL, 2)
	f.snapshot.Rounds[0].Translate.BatchSize = 2
	f.snapshot.Rounds[0].Translate.Concurrency = 2
	f.snapshot.Rounds[0].Translate.Retry.MaxAttempts = 0
	f.snapshot.Strategy.Ruby.Enabled = false
	f.snapshot.RubyRetry = nil
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	gate := pipeline.NewPauseGate()
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, gate, pipeline.DefaultCandidateLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreHandoffAcceptanceInventory(ctx, f, runtime); err != nil {
		runtime.Close()
		t.Fatal(err)
	}
	eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
	if err != nil {
		runtime.Close()
		t.Fatal(err)
	}
	doc, base := f.round(t, 0)
	for i := range doc.Segments {
		doc.Segments[i].Translate = true
	}
	eng.PrepareDocument(doc, nil)
	store := &handoffPauseBoundaryStore{roundStore: base, phase: "promotion_after", pause: func(ctx context.Context) error {
		_, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID)
		if err == nil {
			gate.Pause()
		}
		return err
	}}
	seedCtx, seedCancel := context.WithTimeout(ctx, 30*time.Second)
	_, err = pipeline.RunStagedRound(seedCtx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
	seedCancel()
	eng.Close()
	runtime.Close()
	if err != nil || !gate.Paused() {
		t.Fatalf("partial response seed: %v", err)
	}
	request := f.client.WorkRequest.Query().OnlyX(ctx)
	if request.PromotionCount != 2 || request.PromotionCursor != 1 {
		t.Fatalf("original count/cursor seed: count=%d cursor=%d", request.PromotionCount, request.PromotionCursor)
	}
	before, err := f.runner.recoveryProgress(ctx, f.jobID)
	if err != nil || before != (recoveryProgress{promoted: 1}) {
		t.Fatalf("manifest initialization or uncommitted raw counted as progress: %+v err=%v", before, err)
	}
	if err := f.jobs.MarkJobPaused(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	f.runner.pipeCfg.Candidates = pipeline.CandidateLimits{Segments: 2, Bytes: 16 << 20, ItemBytes: 1 << 20}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = f.runner.processJob(runCtx, f.jobID)
	cancel()
	if err != nil || f.client.Job.GetX(ctx, f.jobID).Status != service.JobStatusCompleted {
		t.Fatalf("original n equals S must recover partial raw: %v", err)
	}
	request = f.client.WorkRequest.GetX(ctx, request.ID)
	if request.PromotionCount != 2 || request.PromotionCursor != 2 || f.client.JobRoundSegment.Query().CountX(ctx) != 2 {
		t.Fatalf("recovery changed original membership or lost commits: %+v", request)
	}
	after, err := f.runner.recoveryProgress(ctx, f.jobID)
	if err != nil || after != (recoveryProgress{confirmed: 2, promoted: 2, finishedRounds: 1}) {
		t.Fatalf("commits/promotion/round did not advance persistent progress: %+v err=%v", after, err)
	}
	// Re-enter the runner with a completed response audit and a lower S. Pure
	// history has no remaining promotion work and must not block dispatch setup.
	f.client.JobResource.UpdateOneID(f.jobResources[0]).SetStatus(service.JobResourceStatusPending).ExecX(ctx)
	f.client.Job.UpdateOneID(f.jobID).SetStatus(service.JobStatusPending).ExecX(ctx)
	f.runner.pipeCfg.Candidates = pipeline.CandidateLimits{Segments: 1, Bytes: 16 << 20, ItemBytes: 1 << 20}
	runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
	err = f.runner.processJob(runCtx, f.jobID)
	cancel()
	if err != nil || f.client.Job.GetX(ctx, f.jobID).Status != service.JobStatusCompleted {
		t.Fatalf("consumed n2 audit blocked S1: %v", err)
	}
	barrier.mu.Lock()
	calls := barrier.calls
	barrier.mu.Unlock()
	if calls != 1 {
		t.Fatalf("recovery or consumed history replayed provider: calls=%d", calls)
	}
	f.assertUsage(t, 1)
}

func TestRubyHandoffAcceptanceRunnerFuturePinsAggregateCapacity(t *testing.T) {
	runHandoffRunnerFuturePins(t, false, handoffRunnerFuturePinsOptions{segments: 2})
}

func TestRubyHandoffAcceptanceRunnerEmptyEarlyRoundsDrainFuturePins(t *testing.T) {
	runHandoffRunnerFuturePins(t, false, handoffRunnerFuturePinsOptions{segments: 2, emptyEarlier: true})
}

func TestRubyHandoffAcceptanceRunnerFixedFuturePrefixMakesProgress(t *testing.T) {
	runHandoffRunnerFuturePins(t, false, handoffRunnerFuturePinsOptions{segments: 5, canProgress: true})
}
