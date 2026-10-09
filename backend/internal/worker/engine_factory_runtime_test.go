package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestEngineFactoryOwnsOnlyLocalRuntime(t *testing.T) {
	fixture := newEngineTelemetryFixture(t, 0, 0)
	local := fixture.engine.Rounds()[0].Runtime
	if err := fixture.engine.Close(); err != nil {
		t.Fatal(err)
	}
	intent := backend.RequestAdmissionIntent{RoundIndex: 0, BackendID: 1, Stage: backend.RequestStageMain}
	if p, _, err := local.Admission.TryAdmit(context.Background(), intent, nil); !errors.Is(err, backend.ErrAdmissionClosed) {
		p.Release()
		t.Fatalf("local runtime remained open: %v", err)
	}
	shared, err := NewExecutionRuntime(fixture.snapshot, fixture.pool, nil, pipeline.DefaultCandidateLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shared.Close)
	ctx := pipeline.WithExecutionRuntime(context.Background(), shared)
	e, err := fixture.factory.BuildEngine(ctx, fixture.snapshot, engine.RuntimeResources{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.Rounds()[0].Runtime != shared {
		t.Fatal("resource engine created a second runtime")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	// A sibling must still be able to use the shared runtime after this close.
	p, _, err := shared.Admission.TryAdmit(ctx, intent, nil)
	if err != nil || p == nil {
		t.Fatalf("resource close shut down shared runtime: %v", err)
	}
	p.Release()
	fixture.factory.build = func(backend.Config) (backend.Backend, error) { return nil, errors.New("construction failed") }
	if e, err := fixture.factory.BuildEngine(ctx, fixture.snapshot, engine.RuntimeResources{}, nil); e != nil || err == nil {
		t.Fatalf("expected failed construction: %v", err)
	}
	p, _, err = shared.Admission.TryAdmit(ctx, intent, nil)
	if err != nil || p == nil {
		t.Fatalf("failed resource construction shut down shared runtime: %v", err)
	}
	p.Release()
}

func TestEngineFactoryPassesDeploymentLimitsToAllBackends(t *testing.T) {
	fixture := newEngineTelemetryFixture(t, 0, 0)
	snapshot := *fixture.snapshot
	snapshot.Strategy.Ruby.Enabled = true
	snapshot.RubyRetry = &service.ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: snapshot.Rounds[0].Backend, MaxAttempts: 2}
	resolved, err := execution.Resolve(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	limits := PipelineConfig{Candidates: pipeline.CandidateLimits{Segments: 2, Bytes: 8, ItemBytes: 4}, MaxResponseBytes: 3 << 20}
	fixture.factory.SetPipelineLimits(limits)
	build := fixture.factory.build
	var calls int
	fixture.factory.build = func(cfg backend.Config) (backend.Backend, error) {
		calls++
		if cfg.MaxResponseBytes != limits.MaxResponseBytes {
			t.Errorf("backend %q response limit=%d, want %d", cfg.Name, cfg.MaxResponseBytes, limits.MaxResponseBytes)
		}
		return build(cfg)
	}
	e, err := fixture.factory.BuildEngine(context.Background(), resolved, engine.RuntimeResources{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if calls != 2 {
		t.Fatalf("main/alignment factories built=%d, want 2", calls)
	}
	window := e.Rounds()[0].Runtime.Window
	if window.TryReserve("oversized", 3) || !window.TryReserve("full", 2) || window.TryReserve("extra", 1) {
		t.Fatal("local runtime ignored deployed candidate window")
	}
	window.Release("full")
}
