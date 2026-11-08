package worker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	pa "github.com/MeowSalty/LinguaFlow/backend/internal/persistenceacceptance"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

type persistenceFastModel struct {
	calls, inflight, peak atomic.Int64
	barrier               <-chan struct{}
	entered               chan<- struct{}
}

func (*persistenceFastModel) Name() string { return "isolated-fast-model" }
func (*persistenceFastModel) Close() error { return nil }
func (m *persistenceFastModel) Translate(ctx context.Context, _ backend.Request) (*backend.Response, error) {
	m.calls.Add(1)
	n := m.inflight.Add(1)
	defer m.inflight.Add(-1)
	for old := m.peak.Load(); n > old; old = m.peak.Load() {
		if m.peak.CompareAndSwap(old, n) {
			break
		}
	}
	if m.entered != nil {
		m.entered <- struct{}{}
	}
	if m.barrier != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-m.barrier:
		}
	} else {
		timer := time.NewTimer(2 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return &backend.Response{Text: `{"glossary":[]}`, Usage: backend.Usage{PromptTokens: 3, CompletionTokens: 5}}, nil
}

type persistenceTimings struct {
	mu      sync.Mutex
	samples map[string][]time.Duration
	peaks   map[string]int
	stop    chan struct{}
	joined  chan struct{}
	runtime *pipeline.ExecutionRuntime
}

func (o *persistenceTimings) Observe(v persistence.Observation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.samples == nil {
		o.samples = map[string][]time.Duration{}
	}
	o.samples[v.Phase] = append(o.samples[v.Phase], v.Duration)
	if v.Metadata.Operation != "" {
		key := v.Metadata.Operation + "." + v.Phase
		o.samples[key] = append(o.samples[key], v.Duration)
	}
}
func (o *persistenceTimings) summary() map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := map[string]any{}
	for key, peak := range o.peaks {
		out[key] = peak
	}
	for phase, samples := range o.samples {
		samples = append([]time.Duration(nil), samples...)
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		if len(samples) > 0 {
			out[phase] = map[string]any{"count": len(samples), "p50_ms": float64(samples[(len(samples)-1)/2]) / float64(time.Millisecond), "p95_ms": float64(samples[int(float64(len(samples)-1)*.95)]) / float64(time.Millisecond)}
		}
	}
	return out
}

func (o *persistenceTimings) sampleRuntime(r *pipeline.ExecutionRuntime) {
	o.runtime = r
	o.stop = make(chan struct{})
	o.joined = make(chan struct{})
	o.peaks = map[string]int{}
	go func() {
		defer close(o.joined)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-o.stop:
				return
			case <-ticker.C:
			}
			values := map[string]int{}
			s := r.Admission.Snapshot()
			values["completion_peak"] = s.PendingResults
			values["acquire_queue_peak"] = int(s.QueuedRequests)
			values["pending_owner_peak"], values["coordinator_intent_peak"] = r.Preparation.Snapshot()
			v := r.Saver.Snapshot()
			values["normal_queue_peak"] = v.NormalQueued
			values["reliable_queue_peak"] = v.ReliableQueued
			o.mu.Lock()
			for key, value := range values {
				o.peaks[key] = max(o.peaks[key], value)
			}
			o.mu.Unlock()
		}
	}()
}
func (o *persistenceTimings) stopSampling() {
	close(o.stop)
	<-o.joined
	o.peaks["pending_owner_peak"], o.peaks["coordinator_intent_peak"] = o.runtime.Preparation.Peaks()
	s := o.runtime.Saver.Snapshot()
	o.peaks["normal_queue_peak"] = s.PeakNormalQueued
	o.peaks["reliable_queue_peak"] = s.PeakReliableQueued
}

func persistenceWireReporter(r *pipeline.ExecutionRuntime, opts *progress.DBReporterOptions, o persistence.Observer) {
	// Producers share the runtime; avoid rewriting its observer after initialization.
	if r.Observer != o {
		r.Observer = o
	}
	opts.Saver = r.PersistenceSaver()
}
func persistenceResources(t *testing.T, batches int) []*pa.Fixture {
	t.Helper()
	first := pa.Seed(t, batches*300, true)
	fixtures := []*pa.Fixture{first}
	project := *first.Client.Resource.GetX(t.Context(), first.Scope.ResourceID).ProjectID
	for i := 1; i < 7; i++ {
		resource := first.Client.Resource.Create().SetProjectID(project).SetPath(fmt.Sprintf("resource%d.txt", i)).SetStoragePath(fmt.Sprintf("resource%d.txt", i)).SetFormat("txt").SaveX(t.Context())
		fixtures = append(fixtures, pa.SeedResource(t, first.Client, first.DB, first.Probe, project, first.Scope.JobID, resource.ID, batches*300, true))
	}
	return fixtures
}
func persistenceRunResources(t *testing.T, fixtures []*pa.Fixture, model *persistenceFastModel) (*pipeline.ExecutionRuntime, *persistenceTimings, <-chan error) {
	t.Helper()
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 10}, MaxCompletions: 16})
	if err != nil {
		t.Fatal(err)
	}
	r := pipeline.NewExecutionRuntime(admission, pipeline.DefaultCandidateLimits(), nil)
	r.JobID = fixtures[0].Scope.JobID
	observer := &persistenceTimings{}
	persistenceWireReporter(r, &progress.DBReporterOptions{}, observer)
	observer.sampleRuntime(r)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	done := make(chan error, 7)
	for _, f := range fixtures {
		store := persistenceRoundStore(f)
		opts := progress.DBReporterOptions{Client: f.Client, JobID: f.Scope.JobID, JobResourceID: f.Scope.JobResourceID, ResourceID: f.Scope.ResourceID, Logger: logger, Ticker: time.Hour}
		persistenceWireReporter(r, &opts, observer)
		reporter := progress.NewDBReporter(opts)
		reporter.SwitchRound(f.Scope.RoundID, func(i int) (int, bool) {
			if i < 0 || i >= len(f.IDs) {
				return 0, false
			}
			return f.IDs[i], true
		})
		renderer, err := prompt.NewBootstrapRenderer("Extract terms; return JSON glossary.")
		if err != nil {
			t.Fatal(err)
		}
		handler := &pipeline.ExtractHandler{Backends: []backend.Backend{pipeline.BindRequestBackend(model, 1, 0, r)}, Renderer: renderer, Glossary: glossary.NewMemory(), BatchSize: 300, MinSourceLen: 1, Logger: logger, Reporter: reporter}
		doc := &pipeline.Document{SourceLang: "en", TargetLang: "zh"}
		for i, id := range f.IDs {
			doc.Segments = append(doc.Segments, pipeline.Segment{ID: fmt.Sprint(id), DBID: id, Source: fmt.Sprintf("synthetic source %d", i), Translate: true})
		}
		go func() {
			_, err := pipeline.RunRound(t.Context(), pipeline.Round{Runtime: r, Store: store, Handler: handler, Concurrency: 10}, doc, nil, logger, reporter)
			closeErr := reporter.Close()
			if err == nil {
				err = closeErr
			}
			done <- err
		}()
	}
	return r, observer, done
}
func persistenceJoin(done <-chan error) []string {
	var failures []string
	for range 7 {
		if err := <-done; err != nil {
			failures = append(failures, err.Error())
		}
	}
	return failures
}

func TestPersistenceLoadMatrix(t *testing.T) {
	for _, ms := range []int{0, 1, 2, 5} {
		t.Run(fmt.Sprintf("delay%d", ms), func(t *testing.T) {
			fixtures := persistenceResources(t, 10)
			probe := fixtures[0].Probe
			probe.Reset(time.Duration(ms) * time.Millisecond)
			model := &persistenceFastModel{}
			r, observer, done := persistenceRunResources(t, fixtures, model)
			failures := persistenceJoin(done)
			observer.stopSampling()
			r.Close()
			job := fixtures[0].Client.Job.GetX(t.Context(), fixtures[0].Scope.JobID)
			usage := fixtures[0].Client.UsageRecord.Query().CountX(t.Context())
			resourceCompletion := make([]int, len(fixtures))
			for i, f := range fixtures {
				resourceCompletion[i] = f.Client.JobRound.GetX(t.Context(), f.Scope.RoundID).SegmentCompleted
			}
			extra := map[string]any{"delay_ms": ms, "resources": 7, "batches": 70, "segments": 21000, "provider_calls": model.calls.Load(), "provider_peak": model.peak.Load(), "completed": job.ProgressCompleted, "total": job.ProgressTotal, "usage_records": usage, "resource_completed": resourceCompletion, "failures": failures, "timings": observer.summary()}
			persistenceReport(t, probe, "load7x10x300", extra)
			if model.peak.Load() > 10 {
				t.Fatal("shared provider concurrency exceeded 10")
			}
			if _, measured := extra["timings"].(map[string]any)["save_queue"]; !measured {
				t.Fatal("runtime observer did not produce required Save queue timings")
			}
			for _, operation := range []string{"reserve", "sent", "received", "completed", "checkpoint", "stage_initialize"} {
				for _, phase := range []string{"save_queue", "query", "exec"} {
					if _, measured := extra["timings"].(map[string]any)[operation+"."+phase]; !measured {
						t.Fatalf("required %s %s observations missing", operation, phase)
					}
				}
			}
			if len(failures) > 0 {
				t.Fatalf("load failed: %v", failures)
			}
			if model.calls.Load() != 70 || usage != 70 || job.ProgressCompleted != 21000 || job.ProgressTotal != 21000 {
				t.Fatalf("model/final facts: %v", extra)
			}
			for _, count := range resourceCompletion {
				if count != 3000 {
					t.Fatalf("resource failed to progress: %v", resourceCompletion)
				}
			}
			for key, cap := range map[string]int{"pending_owner_peak": 16, "normal_queue_peak": 16, "reliable_queue_peak": 32, "completion_peak": 16} {
				peak, ok := extra["timings"].(map[string]any)[key].(int)
				if !ok {
					t.Fatalf("required %s high-water sample missing", key)
				}
				if peak > cap {
					t.Fatalf("%s=%d exceeds %d", key, peak, cap)
				}
			}
		})
	}
}

func TestPersistenceCommitObservations(t *testing.T) {
	f := pa.Seed(t, 1, true)
	store := persistenceRoundStore(f)
	row := f.Client.Segment.GetX(t.Context(), f.IDs[0])
	candidate := &pipeline.Candidate{DTOVersion: pipeline.CandidateDTOVersion, ID: pipeline.NewWorkID(), WorkID: store.WorkIdentity(0), Version: 1, Index: 0, Mode: pipeline.RoundModeTranslate, Format: "text", Ready: true, BaselineStatus: string(row.Status), Segment: pipeline.Segment{DBID: row.ID, Source: row.SourceText, ContentVersion: row.ContentVersion, Status: string(row.Status), TargetIsNull: row.TargetText == nil, Translate: true}}
	if err := store.Save(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, MaxCompletions: 1})
	if err != nil {
		t.Fatal(err)
	}
	r := pipeline.NewExecutionRuntime(admission, pipeline.DefaultCandidateLimits(), nil)
	defer r.Close()
	observer := &persistenceTimings{}
	opts := progress.DBReporterOptions{}
	persistenceWireReporter(r, &opts, observer)
	if opts.Saver == nil {
		t.Fatal("commit observations require the production Save scheduler")
	}
	f.Probe.Reset(0)
	err = opts.Saver.Save(persistence.WithObserver(t.Context(), observer), persistence.Metadata{Operation: "commit", Class: persistence.Reliable, JobID: f.Scope.JobID, ResourceID: f.Scope.ResourceID}, func(ctx context.Context) error {
		outcome, err := store.Commit(ctx, candidate, pipeline.TranslatedSegment{SourceText: row.SourceText, TargetText: "observed committed target"})
		if err == nil && outcome != pipeline.CommitAccepted {
			return fmt.Errorf("commit outcome=%s", outcome)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	timings := observer.summary()
	for _, phase := range []string{"save_queue", "query", "exec", "transaction"} {
		if _, measured := timings["commit."+phase]; !measured {
			t.Fatalf("required commit %s observations missing", phase)
		}
	}
	persistenceReport(t, f.Probe, "commit-observations", map[string]any{"timings": timings})
}

func TestPersistenceSharedConcurrencyReachesTen(t *testing.T) {
	fixtures := persistenceResources(t, 2)
	release := make(chan struct{})
	entered := make(chan struct{}, 14)
	model := &persistenceFastModel{barrier: release, entered: entered}
	r, observer, done := persistenceRunResources(t, fixtures, model)
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	reached := true
	for range 10 {
		select {
		case <-entered:
		case <-timer.C:
			reached = false
		}
		if !reached {
			break
		}
	}
	close(release)
	failures := persistenceJoin(done)
	observer.stopSampling()
	r.Close()
	if !reached || model.peak.Load() != 10 || len(failures) > 0 {
		t.Fatalf("full call path failed to reach shared capacity 10: peak=%d failures=%v", model.peak.Load(), failures)
	}
}
