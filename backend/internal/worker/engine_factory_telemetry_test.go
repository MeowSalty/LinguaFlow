package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/backend/openai"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/telemetry"
)

type engineTelemetryFixture struct {
	factory   *EngineFactory
	engine    *engine.Engine
	snapshot  *service.JobExecutionSnapshot
	pool      *backend.LimiterPool
	collector *telemetry.Collector
	calls     *atomic.Int64
}

func newEngineTelemetryFixture(t *testing.T, currentRPM, snapshotRPM int) engineTelemetryFixture {
	t.Helper()
	calls := &atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":11,"total_tokens":18}}`)
	}))
	t.Cleanup(upstream.Close)
	collector := telemetry.NewCollector()
	clients := telemetry.NewHTTPClients(collector)
	t.Cleanup(func() {
		clients.Shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := clients.Wait(ctx); err != nil {
			t.Errorf("HTTP clients did not stop: %v", err)
		}
	})
	pool := backend.NewLimiterPool()
	pool.Initialize(map[int]int{1: currentRPM})
	t.Cleanup(pool.Shutdown)
	snapshot := translateSnapshot(t)
	snapshot.Rounds[0].Backend = service.BackendSnapshot{
		ID: 1, Name: "telemetry-test", Type: "openai", RateLimitPerMinute: snapshotRPM,
		Options: map[string]any{"api_key": "test", "base_url": upstream.URL, "model": "test", "response_format": "none"},
	}
	factory := NewEngineFactory(nil, pool, clients)
	eng, err := factory.BuildEngine(context.Background(), snapshot, engine.RuntimeResources{}, nil)
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return engineTelemetryFixture{factory: factory, engine: eng, snapshot: snapshot, pool: pool, collector: collector, calls: calls}
}

func TestEngineFactoryUsesCurrentLimiterPolicyInsteadOfSnapshot(t *testing.T) {
	f := newEngineTelemetryFixture(t, 0, 1)
	b := extractBackend(f.engine.Rounds()[0].Handler)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// A restored 1 RPM snapshot would exhaust its token after the first call.
	// The current unlimited registry must permit both without a limiter wait.
	for range 2 {
		if _, err := b.Translate(ctx, backend.Request{User: "hello"}); err != nil {
			t.Fatalf("current policy was not used: %v", err)
		}
	}
	if f.calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", f.calls.Load())
	}
	s := f.pool.Snapshot()
	if s.ActiveLimiters != 0 || s.Waiters != 0 || s.WaitDurationSecondsCount != 0 {
		t.Fatalf("stale snapshot introduced rate limiting: %+v", s)
	}
}

func TestEngineFactoryCannotReviveRemovedBackendFromSnapshot(t *testing.T) {
	f := newEngineTelemetryFixture(t, 0, 60)
	f.pool.Remove(1)
	eng, err := f.factory.BuildEngine(context.Background(), f.snapshot, engine.RuntimeResources{}, nil)
	if eng != nil {
		_ = eng.Close()
		t.Fatal("deleted backend was rebuilt from the stale snapshot")
	}
	if !errors.Is(err, backend.ErrLimiterMissing) {
		t.Fatalf("build error = %v, want missing current policy", err)
	}
	old := extractBackend(f.engine.Rounds()[0].Handler)
	if _, err := old.Translate(context.Background(), backend.Request{User: "hello"}); !errors.Is(err, backend.ErrLimiterClosed) {
		t.Fatalf("old handle error = %v, want closed", err)
	}
	if f.calls.Load() != 0 {
		t.Fatalf("removed backend issued %d requests", f.calls.Load())
	}
}

func TestEngineFactoryInjectsHTTPMetricsWithoutDuplicatingUsage(t *testing.T) {
	f := newEngineTelemetryFixture(t, 0, 0)
	b := extractBackend(f.engine.Rounds()[0].Handler)
	if _, err := b.Translate(context.Background(), backend.Request{User: "hello"}); err != nil {
		t.Fatal(err)
	}
	metrics := CollectMeterMetrics(f.engine)
	if len(metrics) != 1 || metrics[0].APICalls != 1 || metrics[0].InputTokens != 7 || metrics[0].OutputTokens != 11 {
		t.Fatalf("usage must record one backend call and its tokens: %+v", metrics)
	}
	var attempts int64
	for _, request := range f.collector.Snapshot().ExternalRequests {
		attempts += request.Total
		if request.Provider != "openai" || request.Operation != "generate" {
			if request.Total != 0 {
				t.Fatalf("unexpected HTTP dimension: %+v", request)
			}
			continue
		}
		if request.Total != 1 || request.Inflight != 0 {
			t.Fatalf("HTTP attempt lifecycle: %+v", request)
		}
		for _, outcome := range request.Outcomes {
			want := int64(0)
			if outcome.Outcome == "success" {
				want = 1
			}
			if outcome.Finished != want || outcome.DurationCount != want {
				t.Fatalf("HTTP completion recorded incorrectly: %+v", outcome)
			}
		}
	}
	if attempts != 1 || f.calls.Load() != attempts {
		t.Fatalf("upstream calls=%d attempts=%d, want one each", f.calls.Load(), attempts)
	}
}
