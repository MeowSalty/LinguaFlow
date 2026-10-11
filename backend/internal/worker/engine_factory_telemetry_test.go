package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/backend/openai"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/telemetry"
)

type engineTelemetryFixture struct {
	factory     *EngineFactory
	engine      *engine.Engine
	snapshot    *service.JobExecutionSnapshot
	pool        *backend.LimiterPool
	collector   *telemetry.Collector
	calls       *atomic.Int64
	credentials *credential.Memory
	policy      *fixtureBackendPolicy
	requests    chan capturedEngineRequest
}

type capturedEngineRequest struct {
	Model         string  `json:"model"`
	Temperature   float64 `json:"temperature"`
	Authorization string  `json:"-"`
}

type fixtureBackendPolicy struct {
	registry *credential.Memory
	deleted  atomic.Bool
}

func (p *fixtureBackendPolicy) Check(ctx context.Context, b credential.Binding, id int, provider, endpoint string) error {
	if p.deleted.Load() {
		return credential.ErrBackendDeleted
	}
	return p.registry.Check(ctx, b, id, provider, endpoint)
}

func newEngineTelemetryFixture(t *testing.T, currentRPM, snapshotRPM int) engineTelemetryFixture {
	t.Helper()
	calls := &atomic.Int64{}
	requests := make(chan capturedEngineRequest, 20)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var captured capturedEngineRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		captured.Authorization = r.Header.Get("Authorization")
		requests <- captured
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
	registry := credential.NewMemory()
	binding, err := registry.Register("openai", upstream.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	opts, err := execution.ResolveBackendOptions("openai", map[string]any{"base_url": upstream.URL, "model": "test", "response_format": "none"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Rounds[0].Backend = service.BackendSnapshot{
		ID: 1, Name: "telemetry-test", Type: "openai", RateLimitPerMinute: snapshotRPM,
		Options: opts, Credential: binding,
	}
	policy := &fixtureBackendPolicy{registry: registry}
	factory := NewEngineFactoryWithCredentials(nil, pool, registry, policy, clients)
	eng, err := factory.BuildEngine(context.Background(), snapshot, engine.RuntimeResources{}, nil)
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return engineTelemetryFixture{factory: factory, engine: eng, snapshot: snapshot, pool: pool, collector: collector, calls: calls, credentials: registry, policy: policy, requests: requests}
}

func TestEngineFactoryRestoresFrozenTemplatesOptionsAndRuby(t *testing.T) {
	f := newEngineTelemetryFixture(t, 0, 0)
	s := f.snapshot
	s.Rounds[0].Backend.Options["model"] = "archived-model"
	s.Rounds[0].Backend.Options["temperature"] = 0.25
	s.Rounds[0].Translate.Prompt.Content = "archived translate"
	s.Strategy.Ruby.Enabled = true
	s.Strategy.Ruby.PreserveKinds = []string{}
	s.RubyTemplates = execution.RubyTemplates{JSON: "archived ruby JSON", Text: "archived ruby text", BatchJSON: "archived batch JSON", BatchText: "archived batch text"}
	s.RetryReminderTemplate = "archived reminder {{.Reason}}"
	b := s.Rounds[0].Backend
	s.Rounds = append(s.Rounds,
		service.JobRoundSnapshot{Mode: "adjudicate", Backend: b, Adjudicate: &service.JobAdjudicateRoundSnapshot{TemplateContent: "archived adjudicate", BatchSize: 3, Concurrency: 1}},
		service.JobRoundSnapshot{Mode: "semantic_qa", Backend: b, SemanticQA: &service.JobSemanticQARoundSnapshot{TemplateContent: "archived semantic QA", BatchSize: 3, Concurrency: 1}},
		service.JobRoundSnapshot{Mode: "revise", Backend: b, Revise: &service.JobReviseRoundSnapshot{TemplateContent: "archived revise", BatchSize: 3, Concurrency: 1}},
	)
	s.RubyRetry = &service.ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: b, MaxAttempts: 2, BatchSize: new(4), MaxWordsPerBatch: new(100), BatchWaitMS: new(0)}
	resolved, err := execution.Resolve(*s)
	if err != nil {
		t.Fatal(err)
	}
	s = resolved
	stored, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "api_key") {
		t.Fatal("snapshot contains a secret field")
	}
	// Simulate later changes to all source assets; restoration only gets stored bytes.
	s.Rounds[0].Backend.Options["model"] = "replacement-model"
	s.Rounds[0].Backend.Options["temperature"] = 0.9
	s.Rounds[0].Translate.Prompt.Content = "replacement translate"
	s.Rounds[1].Adjudicate.TemplateContent = "replacement adjudicate"
	s.Rounds[2].SemanticQA.TemplateContent = "replacement semantic QA"
	s.Rounds[3].Revise.TemplateContent = "replacement revise"
	s.RubyTemplates = execution.RubyTemplates{JSON: "replacement", Text: "replacement"}
	var restored service.JobExecutionSnapshot
	if err := json.Unmarshal(stored, &restored); err != nil {
		t.Fatal(err)
	}
	eng, err := f.factory.BuildEngine(context.Background(), &restored, engine.RuntimeResources{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	wantTemplates := []string{"archived translate", "archived adjudicate", "archived semantic QA", "archived revise"}
	for i, round := range eng.Rounds() {
		var system string
		switch h := round.Handler.(type) {
		case *pipeline.TranslateHandler:
			if h.RetryReminderTemplate != "archived reminder {{.Reason}}" {
				t.Fatal("retry reminder snapshot lost")
			}
			system, _, err = h.Renderer.Render(prompt.Data{SourceLang: "en", TargetLang: "zh"})
			if h.RubyTemplates.JSON != "archived ruby JSON" || h.RubyTemplates.Text != "archived ruby text" || h.RubyTemplates.BatchJSON != "archived batch JSON" || h.RubyTemplates.BatchText != "archived batch text" || h.RubyRetryAttempts != 2 || h.RubyBatch != (pipeline.AlignmentBatchConfig{BatchSize: 4, MaxWordsPerBatch: 100, ProtocolVersion: 1}) {
				t.Fatal("frozen Ruby configuration lost")
			}
			if h.RubyPreserveKinds == nil || len(h.RubyPreserveKinds) != 0 {
				t.Fatal("explicit empty Ruby kinds lost")
			}
			if h.Retry.MaxAttempts != 0 || h.Context.Before != 0 || h.Context.After != 0 {
				t.Fatal("explicit zero values changed")
			}
			if _, err := h.RubyRetryBackends[0].Translate(context.Background(), backend.Request{User: "ruby"}); err != nil {
				t.Fatal(err)
			}
		case *pipeline.AdjudicateHandler:
			system, _, err = h.Renderer.Render(prompt.AdjudicationData{})
		case *pipeline.SemanticQAHandler:
			system, _, err = h.Renderer.Render(prompt.SemanticQAData{})
		case *pipeline.ReviseHandler:
			system, _, err = h.Renderer.Render(prompt.ReviseData{})
			if h.RubyTemplates.JSON != "archived ruby JSON" || h.RubyTemplates.BatchJSON != "archived batch JSON" || h.RubyTemplates.BatchText != "archived batch text" || h.RubyBatch != (pipeline.AlignmentBatchConfig{BatchSize: 4, MaxWordsPerBatch: 100, ProtocolVersion: 1}) {
				t.Fatal("revise Ruby configuration lost")
			}
		}
		if err != nil || system != wantTemplates[i] {
			t.Fatalf("round %d template=%q err=%v", i, system, err)
		}
		if _, err := extractBackend(round.Handler).Translate(context.Background(), backend.Request{System: system, User: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	for range 5 {
		request := <-f.requests
		if request.Model != "archived-model" || request.Temperature != 0.25 || request.Authorization != "Bearer test" {
			t.Fatalf("frozen provider request mismatch: %+v", request)
		}
	}
	if _, ok := restored.Rounds[0].Backend.Options["api_key"]; ok {
		t.Fatal("runtime injection mutated persisted options")
	}
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
	f.policy.deleted.Store(true)
	f.pool.Remove(1)
	eng, err := f.factory.BuildEngine(context.Background(), f.snapshot, engine.RuntimeResources{}, nil)
	if eng != nil {
		_ = eng.Close()
		t.Fatal("deleted backend was rebuilt from the stale snapshot")
	}
	if !errors.Is(err, credential.ErrBackendDeleted) {
		t.Fatalf("build error = %v, want deleted backend policy", err)
	}
	old := extractBackend(f.engine.Rounds()[0].Handler)
	if _, err := old.Translate(context.Background(), backend.Request{User: "hello"}); !errors.Is(err, credential.ErrBackendDeleted) {
		t.Fatalf("old handle error = %v, want deleted backend policy", err)
	}
	if f.calls.Load() != 0 {
		t.Fatalf("removed backend issued %d requests", f.calls.Load())
	}
}

func TestEngineFactoryRevocationBlocksExistingHandleAndRestore(t *testing.T) {
	f := newEngineTelemetryFixture(t, 0, 0)
	if err := f.credentials.Revoke(f.snapshot.Rounds[0].Backend.Credential); err != nil {
		t.Fatal(err)
	}
	b := extractBackend(f.engine.Rounds()[0].Handler)
	if _, err := b.Translate(context.Background(), backend.Request{User: "hello"}); !errors.Is(err, credential.ErrRevoked) {
		t.Fatalf("existing handle: %v", err)
	}
	if restored, err := f.factory.BuildEngine(context.Background(), f.snapshot, engine.RuntimeResources{}, nil); restored != nil || !errors.Is(err, credential.ErrRevoked) {
		t.Fatalf("restore: engine=%v err=%v", restored, err)
	}
	if f.calls.Load() != 0 {
		t.Fatal("revoked credential reached upstream")
	}
}

func TestEngineFactoryRechecksPolicyAfterLimiterWait(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		name := "revoked"
		if deleted {
			name = "deleted"
		}
		t.Run(name, func(t *testing.T) {
			f := newEngineTelemetryFixture(t, 1, 0)
			b := extractBackend(f.engine.Rounds()[0].Handler)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := b.Translate(ctx, backend.Request{User: "first"}); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { _, err := b.Translate(ctx, backend.Request{User: "queued"}); result <- err }()
			for f.pool.Snapshot().Waiters == 0 {
				select {
				case err := <-result:
					t.Fatalf("request did not wait: %v", err)
				case <-ctx.Done():
					t.Fatal("limiter did not queue request")
				case <-time.After(time.Millisecond):
				}
			}
			want := credential.ErrRevoked
			if deleted {
				want = credential.ErrBackendDeleted
				f.policy.deleted.Store(true)
				f.pool.Remove(1)
			} else {
				if err := f.credentials.Revoke(f.snapshot.Rounds[0].Backend.Credential); err != nil {
					t.Fatal(err)
				}
				f.pool.Refresh(1, 0)
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatalf("queued request: %v, want %v", err, want)
				}
			case <-ctx.Done():
				t.Fatal("queued request did not finish")
			}
			if f.calls.Load() != 1 {
				t.Fatalf("queued request reached upstream: %d", f.calls.Load())
			}
		})
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
