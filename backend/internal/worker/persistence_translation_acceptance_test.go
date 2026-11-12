package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	pa "github.com/MeowSalty/LinguaFlow/backend/internal/persistenceacceptance"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

const persistenceTranslationTarget = "压力测试译文"

type persistenceTranslationModel struct {
	persistenceFastModel
	mu    sync.Mutex
	sizes map[int]int
}

func (m *persistenceTranslationModel) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	response, err := m.persistenceFastModel.Translate(ctx, req)
	if err != nil {
		return nil, err
	}
	properties, ok := req.JSONSchema["properties"].(map[string]any)
	if !ok {
		return nil, errors.New("translation request has no structured response schema")
	}
	translations, ok := properties["translations"].(map[string]any)
	if !ok {
		return nil, errors.New("translation request has no translations schema")
	}
	ids, ok := translations["properties"].(map[string]any)
	if !ok || len(ids) == 0 {
		return nil, errors.New("translation request has no requested identities")
	}
	m.mu.Lock()
	if m.sizes == nil {
		m.sizes = map[int]int{}
	}
	m.sizes[len(ids)]++
	m.mu.Unlock()
	text := make(map[string]string, len(ids))
	for id := range ids {
		text[id] = persistenceTranslationTarget
	}
	encoded, err := json.Marshal(map[string]any{"translations": text})
	if err != nil {
		return nil, err
	}
	response.Text = string(encoded)
	return response, nil
}

func (m *persistenceTranslationModel) sizeDistribution() map[int]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[int]int, len(m.sizes))
	for size, count := range m.sizes {
		out[size] = count
	}
	return out
}

type persistenceTranslationObserver struct {
	timings                            *persistenceTimings
	mu                                 sync.Mutex
	commits, committedMembers, missing int
	requests                           map[string]bool
	resources                          map[int]int
	examples                           []persistence.Metadata
}

func (o *persistenceTranslationObserver) Observe(v persistence.Observation) {
	o.timings.Observe(v)
	if v.Metadata.Operation != "commit" || v.Phase != "save_execution" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.commits++
	if v.Err == nil {
		o.committedMembers++
	}
	if v.Metadata.JobID == 0 || v.Metadata.ResourceID == 0 || v.Metadata.RequestID == "" || v.Metadata.CandidateID == "" {
		o.missing++
	}
	o.requests[v.Metadata.RequestID] = true
	o.resources[v.Metadata.ResourceID]++
	if len(o.examples) < 7 {
		o.examples = append(o.examples, v.Metadata)
	}
}

func (o *persistenceTranslationObserver) summary() map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	return map[string]any{"events": o.commits, "missing": o.missing, "request_count": len(o.requests), "resource_events": o.resources, "examples": o.examples}
}

// Progress reads existing counters only; it never adds SQL to the workload.
func persistenceTranslationProgress(t *testing.T, delay int, probe *pa.Probe, model *persistenceTranslationModel, runtime *pipeline.ExecutionRuntime, observer *persistenceTranslationObserver) func() {
	t.Helper()
	stop, joined := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(joined)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			observer.timings.mu.Lock()
			transactions := len(observer.timings.samples["transaction"])
			observer.timings.mu.Unlock()
			observer.mu.Lock()
			members := observer.committedMembers
			observer.mu.Unlock()
			saves := runtime.Saver.Snapshot()
			t.Logf("translation progress driver=%s delay_ms=%d SQL=%d transactions=%d calls=%d committed_members=%d saves=%d normal_queue=%d reliable_queue=%d normal_peak=%d reliable_peak=%d", pa.Driver(), delay, probe.SQLCount(), transactions, model.calls.Load(), members, saves.Saves, saves.NormalQueued, saves.ReliableQueued, saves.PeakNormalQueued, saves.PeakReliableQueued)
		}
	}()
	return func() {
		once.Do(func() { close(stop) })
		<-joined
	}
}

// This follows the production translation candidate path. The source batches
// stay at 300 and runtime capacities/budgets keep their product defaults.
func TestPersistenceTranslationLoadMatrix(t *testing.T) {
	if os.Getenv("LINGUAFLOW_PERSISTENCE_DRIVER") == "" {
		t.Skip("long translation matrix: run task backend:test:persistence, task backend:test:persistence:postgres, or task backend:bench:persistence with its explicit driver")
	}
	for _, ms := range []int{0, 5} {
		t.Run(fmt.Sprintf("delay%d", ms), func(t *testing.T) {
			persistenceTranslationLoad(t, 7, 2, ms, "translation7x2x300")
		})
	}
}

// Ordinary CI crosses a shared candidate window with two resources. Explicit
// acceptance keeps the production 300/256 size and its report sample.
func TestPersistenceTranslationContract(t *testing.T) {
	if pa.FullWorkload() {
		persistenceTranslationLoad(t, 2, 1, 0, "translation2x1x300")
		return
	}
	persistenceTranslationLoad(t, 2, 1, 0, "translation2x1x20", 20)
}

func persistenceTranslationLoad(t *testing.T, resources, batches, ms int, sampleName string, sizes ...int) {
	t.Helper()
	size := 300
	limits := pipeline.DefaultCandidateLimits()
	if len(sizes) > 0 {
		size = sizes[0]
		limits.Segments = 16
	}
	fixtures := persistenceResourceFixtures(t, resources, batches, size)
	wantResourceSegments := batches * size
	wantSegments := resources * wantResourceSegments
	// This bounds the whole workload harness, not an individual save.
	// Product SQL execution and logical handoff budgets remain 30/120s.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Minute)
	defer cancel()
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 10}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := pipeline.NewExecutionRuntime(admission, limits, nil)
	runtime.JobID = fixtures[0].Scope.JobID
	runtime.Lifetime = ctx
	defer runtime.Close()
	timings := &persistenceTimings{}
	observer := &persistenceTranslationObserver{timings: timings, requests: map[string]bool{}, resources: map[int]int{}}
	ctx = persistence.WithObserver(ctx, observer)
	runtime.Lifetime = ctx
	model := &persistenceTranslationModel{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	type resourceRun struct {
		store    *roundStore
		reporter *progress.DBReporter
		handler  *pipeline.TranslateHandler
		doc      *pipeline.Document
	}
	var runs []resourceRun
	for _, fixture := range fixtures {
		fixture.Client.JobRound.UpdateOneID(fixture.Scope.RoundID).SetMode("translate").ExecX(ctx)
		indices := make(map[int]int, len(fixture.IDs))
		for i, id := range fixture.IDs {
			indices[i] = id
		}
		snapshot := &service.JobExecutionSnapshot{SourceLang: "en", TargetLang: "zh", Rounds: []execution.JobRoundSnapshot{{Mode: "translate", Backend: execution.BackendSnapshot{ID: 1, Name: "isolated-fake"}, Translate: &execution.JobTranslateRoundSnapshot{Prompt: execution.PromptSnapshot{Content: "Translate; return JSON translations."}, Concurrency: 10, BatchSize: size, Retry: execution.RetryConfig{MaxAttempts: 1}}}}}
		snapshot = completeWorkerSnapshot(t, snapshot)
		fixture.SetExecutionSnapshot(t, snapshot)
		store := newRoundStore(fixture.Client, fixture.Scope, indices, snapshot, 0, nil, false, pa.Driver(), nil, nil)
		opts := progress.DBReporterOptions{Client: fixture.Client, JobID: fixture.Scope.JobID, JobResourceID: fixture.Scope.JobResourceID, ResourceID: fixture.Scope.ResourceID, Lifetime: ctx, Logger: logger, Ticker: time.Hour}
		persistenceWireReporter(runtime, &opts, observer)
		reporter := progress.NewDBReporter(opts)
		reporter.SwitchRound(fixture.Scope.RoundID, func(i int) (int, bool) {
			id, ok := indices[i]
			return id, ok
		})
		renderer, err := prompt.NewRenderer(`Translate {{.SourceLang}} into {{.TargetLang}}. Return JSON {"translations":{"<id>":"<translation>"}}.`)
		if err != nil {
			t.Fatal(err)
		}
		handler := &pipeline.TranslateHandler{Backend: pipeline.BindRequestBackend(model, 1, 0, runtime), Renderer: renderer, BatchSize: size, Logger: logger, Reporter: reporter}
		doc := &pipeline.Document{SourceLang: "en", TargetLang: "zh"}
		rows := fixture.Client.Segment.Query().Where(segment.ResourceIDEQ(fixture.Scope.ResourceID)).Order(segment.BySegmentIndex()).AllX(ctx)
		for _, row := range rows {
			doc.Segments = append(doc.Segments, pipeline.Segment{ID: fmt.Sprint(row.ID), DBID: row.ID, ContentVersion: row.ContentVersion, Source: row.SourceText, Status: string(row.Status), TargetIsNull: row.TargetText == nil, Translate: true})
		}
		runs = append(runs, resourceRun{store, reporter, handler, doc})
	}
	runtime.Observer = observer
	timings.sampleRuntime(runtime)
	fixtures[0].Probe.Reset(time.Duration(ms) * time.Millisecond)
	stopProgress := persistenceTranslationProgress(t, ms, fixtures[0].Probe, model, runtime, observer)
	defer stopProgress()
	done := make(chan error, len(runs))
	var yields atomic.Int64
	for _, run := range runs {
		go func() {
			var runErr error
			for {
				changed := runtime.Window.Changed()
				_, runErr = pipeline.RunRound(ctx, pipeline.Round{Runtime: runtime, Store: run.store, Handler: run.handler, Concurrency: 10, Retry: backend.RetryPolicy{MaxAttempts: 1}}, run.doc, nil, logger, run.reporter)
				if !errors.Is(runErr, pipeline.ErrResourceYield) {
					break
				}
				yields.Add(1)
				select {
				case <-ctx.Done():
					runErr = ctx.Err()
				case <-changed:
					continue
				}
				break
			}
			if runErr != nil {
				// Preserve the first resource's storage error before its peers wake
				// from shared-capacity waits, then join every resource below.
				t.Logf("translation resource failed: %v", runErr)
				cancel()
			}
			closeErr := run.reporter.Close()
			if closeErr != nil {
				t.Logf("translation reporter failed: %v", closeErr)
				cancel()
			}
			done <- errors.Join(runErr, closeErr)
		}()
	}
	failures := persistenceJoin(done, len(fixtures))
	stopProgress()
	timings.stopSampling()
	if len(failures) > 0 {
		t.Fatalf("translation load failed: %v", failures)
	}
	remainingBudget := runtime.Admission.PipelineSnapshot()
	if remainingBudget.Segments != 0 || remainingBudget.CandidateBytes != 0 || remainingBudget.GrowthBytes != 0 || remainingBudget.HandoffBytes != 0 || remainingBudget.PreparedBytes != 0 || remainingBudget.AuxInputBytes != 0 || remainingBudget.AuxResponseBytes != 0 || remainingBudget.AuxOwners != 0 {
		t.Fatalf("completed translation retained in-flight budget: %+v", remainingBudget)
	}
	for _, owners := range remainingBudget.MainOwners {
		if owners != 0 {
			t.Fatalf("completed translation retained main owners: %+v", remainingBudget)
		}
	}
	runtime.Close()
	assertCtx, assertCancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer assertCancel()
	client := fixtures[0].Client
	job := client.Job.GetX(assertCtx, fixtures[0].Scope.JobID)
	usageRows := client.UsageRecord.Query().AllX(assertCtx)
	usage := len(usageRows)
	usageCalls := map[int]int{}
	for _, row := range usageRows {
		usageCalls[row.ID] = row.APICalls
	}
	facts := client.JobRoundSegment.Query().CountX(assertCtx)
	candidates := client.WorkCandidate.Query().CountX(assertCtx)
	requestRows := client.WorkRequest.Query().AllX(assertCtx)
	requests, abortedRequests, invalidAbortedRequests := 0, 0, 0
	requestSizes := map[int]int{}
	memberCounts := map[int]int{}
	requestIdentities := map[string]bool{}
	usageIdentities := map[int]bool{}
	invalidRequests := 0
	requestScopes := make(map[int]*pa.Fixture, len(fixtures))
	for _, fixture := range fixtures {
		requestScopes[fixture.Scope.RoundID] = fixture
	}
	for _, request := range requestRows {
		fixture := requestScopes[request.JobRoundID]
		if fixture == nil || request.JobID != fixture.Scope.JobID || request.ResourceID != fixture.Scope.ResourceID || request.RetryEpoch != fixture.Scope.RetryEpoch || request.Stage != string(backend.RequestStageMain) {
			invalidRequests++
		}
		if request.Identity == "" || requestIdentities[request.Identity] {
			invalidRequests++
		}
		requestIdentities[request.Identity] = true
		if request.State == "cancelled_before_dispatch" {
			abortedRequests++
			if request.UsageKnown || request.UsageRecordID != nil || request.InputTokens != 0 || request.OutputTokens != 0 || request.HandoffAt != nil || request.ResponseBytes != 0 || len(request.ResponsePayload) != 0 || request.FreezeBytes != 0 || len(request.FreezePayload) != 0 || request.BusinessBound != 0 || len(request.PromotionMembers) != 0 {
				invalidAbortedRequests++
			}
			continue
		}
		requests++
		if len(request.SegmentIds) < 1 || len(request.SegmentIds) > limits.Segments {
			invalidRequests++
		}
		if request.State != "completed" || !request.UsageKnown || request.InputTokens != 3 || request.OutputTokens != 5 || request.UsageRecordID == nil {
			invalidRequests++
		}
		if request.UsageRecordID != nil {
			if usageIdentities[*request.UsageRecordID] || usageCalls[*request.UsageRecordID] != 1 {
				invalidRequests++
			}
			usageIdentities[*request.UsageRecordID] = true
		}
		requestSizes[len(request.SegmentIds)]++
		for _, id := range request.SegmentIds {
			memberCounts[id]++
		}
	}
	duplicateOrMissingMembers := 0
	for _, fixture := range fixtures {
		for _, id := range fixture.IDs {
			if memberCounts[id] != 1 {
				duplicateOrMissingMembers++
			}
		}
	}
	resourceCompleted := make([]int, len(fixtures))
	bodyUpdated := 0
	for i, fixture := range fixtures {
		resourceCompleted[i] = client.JobRound.GetX(assertCtx, fixture.Scope.RoundID).SegmentCompleted
		rows := client.Segment.Query().Where(segment.ResourceIDEQ(fixture.Scope.ResourceID)).AllX(assertCtx)
		for _, row := range rows {
			if row.TargetText != nil && *row.TargetText == persistenceTranslationTarget && string(row.Status) == "translated" && row.ContentVersion == 2 {
				bodyUpdated++
			}
		}
	}
	correlations := observer.summary()
	extra := map[string]any{"delay_ms": ms, "resources": resources, "source_batches": resources * batches, "source_batch_size": size, "actual_llm_batches": requests, "request_size_distribution": requestSizes, "model_size_distribution": model.sizeDistribution(), "default_candidate_segments": limits.Segments, "split_reason": "source batches exceed or compete for the shared candidate window", "segments": wantSegments, "provider_calls": model.calls.Load(), "provider_peak": model.peak.Load(), "resource_yields": yields.Load(), "completed": job.ProgressCompleted, "total": job.ProgressTotal, "body_updated": bodyUpdated, "facts": facts, "candidate_records": candidates, "request_records": len(requestRows), "completed_request_records": requests, "aborted_request_records": abortedRequests, "invalid_aborted_requests": invalidAbortedRequests, "usage_records": usage, "invalid_requests": invalidRequests, "duplicate_or_missing_members": duplicateOrMissingMembers, "resource_completed": resourceCompleted, "failures": failures, "timings": timings.summary(), "commit_correlations": correlations}
	persistenceReport(t, fixtures[0].Probe, sampleName, extra)
	if int64(requests) != model.calls.Load() || usage != requests || job.ProgressCompleted != int64(wantSegments) || job.ProgressTotal != int64(wantSegments) || bodyUpdated != wantSegments || facts != wantSegments || candidates != wantSegments || invalidRequests != 0 || invalidAbortedRequests != 0 || duplicateOrMissingMembers != 0 || len(memberCounts) != wantSegments || !reflect.DeepEqual(requestSizes, model.sizeDistribution()) {
		t.Fatalf("translation requests/body/facts do not satisfy the source workload: %v", extra)
	}
	for _, fixture := range fixtures {
		for _, member := range client.WorkItem.Query().Where(workitem.JobRoundIDEQ(fixture.Scope.RoundID)).AllX(assertCtx) {
			if member.MainAttempts != 1 || member.MainNetworkAttempts != 1 || member.NetworkAttempts != 1 {
				t.Fatalf("pre-dispatch abort did not refund member attempts: %+v", member)
			}
		}
	}
	if model.peak.Load() > 10 {
		t.Fatal("shared provider concurrency exceeded 10")
	}
	for _, count := range resourceCompleted {
		if count != wantResourceSegments {
			t.Fatalf("incomplete resource: %v", resourceCompleted)
		}
	}
	if correlations["events"] != wantSegments || correlations["missing"] != 0 || correlations["request_count"] != requests {
		t.Fatalf("commit identity attribution incomplete: %v", correlations)
	}
	for _, operation := range []string{"reserve", "sent", "response_handoff", "member_promotion", "commit", "stage_initialize"} {
		for _, phase := range []string{"save_queue", "transaction", "query", "exec"} {
			if _, ok := extra["timings"].(map[string]any)[operation+"."+phase]; !ok {
				t.Fatalf("missing %s.%s timing", operation, phase)
			}
		}
	}
	for key, cap := range map[string]int{"pending_owner_peak": 16, "coordinator_intent_peak": 16, "normal_queue_peak": 16, "reliable_queue_peak": 32, "completion_peak": 16} {
		peak, ok := extra["timings"].(map[string]any)[key].(int)
		if !ok || peak > cap {
			t.Fatalf("%s=%v exceeds %d or was not measured", key, extra["timings"].(map[string]any)[key], cap)
		}
	}
}
