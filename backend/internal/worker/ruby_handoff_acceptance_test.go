package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goRuntime "runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// This fixture observes the real HTTP adapter. It shares no provider interface
// fake or private accounting implementation with the scheduler under test.
type handoffPermanentStorageError struct{}

func verifyHandoffOwnedTerminal(s backend.OwnedPayloadSnapshot, required ...string) error {
	var total int64
	for category, current := range s.Current {
		if current < 0 || current > s.Peak[category] || current != 0 {
			return fmt.Errorf("named actual payload remained after join: %s=%d peak=%d", category, current, s.Peak[category])
		}
		total += current
	}
	if total != s.Total || s.Total != 0 || s.TotalPeak < 1 {
		return fmt.Errorf("named actual payload total did not retire: %+v", s)
	}
	for _, category := range required {
		if s.Peak[category] < 1 {
			return fmt.Errorf("real allocation path had no named observation: %s snapshot=%+v", category, s)
		}
	}
	return nil
}

func TestRubyHandoffAcceptanceHTTPOriginalMemberMetadataExceedsFixedO(t *testing.T) {
	const members = 128
	f := newRubyPipelineFixture(t, members)
	f.snapshot.Rounds[0].Translate.BatchSize = members
	f.snapshot.RubyRetry.Concurrency = 2
	f.persistSnapshot(t)
	limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 16, MaxMainResponseOwners: 4, MaxAuxResponseOwners: 16, MaxBuildWorkers: 16, MaxQueuedDescriptors: 4096, MaxLogicalStorageBytes: 1 << 30}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
	if err != nil {
		t.Fatal(err)
	}
	source := workstate.InventorySource{DB: f.client, Driver: "sqlite", ItemBytes: f.runner.pipeCfg.Candidates.ItemBytes}
	f.runner.SetRootBudget(root, nil)
	f.runner.SetBudgetSource(source, nil)
	workstate.InstallStorageHooks(f.client)
	var observed atomic.Int64
	f.respond = func(ctx context.Context, request rubyPipelineRequest) (any, error) {
		if len(request.Segments) > 0 {
			if len(request.Segments) != members {
				return nil, fmt.Errorf("main was cropped: %d != %d", len(request.Segments), members)
			}
			n, err := source.Metadata(ctx, f.jobID)
			if err != nil {
				return nil, err
			}
			observed.Store(n)
		}
		return rubyPipelineResponse(request), nil
	}
	// Confirming 128 durable members needs bounded headroom when race and
	// coverage instrumentation share CPU with other package tests.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := f.runner.processJob(ctx, f.jobID); err != nil {
		main, align, sources, alignment := f.calls()
		t.Logf("metadata failure: main_HTTP=%d aux_HTTP=%d source_members=%d aux_batches=%d observed_metadata=%d root=%+v", main, align, len(sources), len(alignment), observed.Load(), root.Snapshot(0))
		factCtx, factCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer factCancel()
		facts, factErr := f.client.JobRoundSegment.Query().Where(jobroundsegment.HasJobRoundWith(jobround.JobIDEQ(f.jobID))).Count(factCtx)
		t.Logf("metadata failure: confirmed=%d read_error=%v", facts, factErr)
		requests, readErr := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).Select(workrequest.FieldStage, workrequest.FieldState, workrequest.FieldPromotionState, workrequest.FieldPromotionCursor, workrequest.FieldPromotionCount, workrequest.FieldLastError).Order(ent.Desc(workrequest.FieldID)).Limit(16).All(factCtx)
		if readErr != nil {
			t.Logf("metadata request diagnostics: %v", readErr)
		}
		for _, request := range requests {
			t.Logf("metadata request: stage=%s state=%s promotion=%s cursor=%d/%d error=%v", request.Stage, request.State, request.PromotionState, request.PromotionCursor, request.PromotionCount, request.LastError)
		}
		t.Fatal(err)
	}
	row := f.client.Job.GetX(ctx, f.jobID)
	if row.Status != service.JobStatusCompleted {
		cause := ""
		if row.ErrorMessage != nil {
			cause = *row.ErrorMessage
		}
		t.Fatalf("complete n128 metadata failed: %s %s", row.Status, cause)
	}
	f.assertAccepted(t, 0, members)
	if observed.Load() <= backend.RequestMetadataBytes {
		t.Fatalf("fixture did not exceed fixed O: actual metadata=%d", observed.Load())
	}
	v, storage, err := source.Job(ctx, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	if v.PayloadBytes != 0 || v.UnconfirmedMembers != 0 {
		t.Fatalf("joined terminal metadata still owned P/S: %+v", v)
	}
	v.LogicalStorageBytes = storage
	if root.Snapshot(0).Instance != v {
		t.Fatalf("root vs reliable SQL audit: root=%+v source=%+v", root.Snapshot(0).Instance, v)
	}
	main, _, _, _ := f.calls()
	if main != 1 {
		t.Fatalf("complete original n128 main calls=%d", main)
	}
	t.Logf("complete main n%d HTTP1, pre-response actual member metadata=%d > O=%d; joined P/S=0 audit LS=%d", members, observed.Load(), backend.RequestMetadataBytes, storage)
}

func (handoffPermanentStorageError) Error() string   { return "acceptance durable record unavailable" }
func (handoffPermanentStorageError) Permanent() bool { return true }

type handoffStoredRecordStore struct {
	pipeline.RoundStore
	entered chan struct{}
	release <-chan struct{}
	failure bool
}

func (s *handoffStoredRecordStore) Record(ctx context.Context, id string, record pipeline.RequestRecord) error {
	if record.State == "completed" {
		s.entered <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if s.failure {
			return handoffPermanentStorageError{}
		}
	}
	return s.RoundStore.Record(ctx, id, record)
}
func TestRubyHandoffAcceptanceHTTPStoredRoundsOwnResponseUntilDurable(t *testing.T) {
	for _, mode := range []string{"extract", "semantic_qa", "adjudicate"} {
		for _, failure := range []bool{false, true} {
			name := "slow"
			if failure {
				name = "storage_failure"
			}
			t.Run(mode+"/"+name, func(t *testing.T) {
				content := `{"glossary":[]}`
				if mode == "semantic_qa" {
					content = `{"issues":[]}`
				} else if mode == "adjudicate" {
					content = `{"verdicts":[]}`
				}
				var mu sync.Mutex
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						http.Error(w, "request", 400)
						return
					}
					mu.Lock()
					calls++
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 7, "completion_tokens": 11, "total_tokens": 18}})
				}))
				defer server.Close()
				f := newRubyPipelineFixtureWithClient(t, newRegistryTestClient(t), server.URL, 2)
				b := f.snapshot.Rounds[0].Backend
				f.snapshot.Rounds = []service.JobRoundSnapshot{{Mode: "extract", Backend: b, Extract: &execution.JobExtractRoundSnapshot{TemplateContent: templates.EmbeddedBootstrapTemplate(), Concurrency: 1, BatchSize: 2, MaxTermsPer1000Chars: 10, MinSourceLen: 1}}}
				if mode == "semantic_qa" {
					f.snapshot.Rounds = []service.JobRoundSnapshot{{Mode: mode, Backend: b, SemanticQA: &execution.JobSemanticQARoundSnapshot{TemplateContent: templates.EmbeddedSemanticQATemplate(), Concurrency: 1, BatchSize: 2, SegmentScope: "all"}}}
				} else if mode == "adjudicate" {
					f.snapshot.Rounds = []service.JobRoundSnapshot{{Mode: mode, Backend: b, Adjudicate: &execution.JobAdjudicateRoundSnapshot{TemplateContent: templates.EmbeddedAdjudicationTemplate(), Concurrency: 1, BatchSize: 2, AdjudicateCodes: []string{"source_residual"}}}}
				}
				if mode != "extract" {
					for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(context.Background()) {
						f.client.Segment.UpdateOneID(row.ID).SetTargetText("original target").SetStatus(segment.StatusTranslated).SetQualityIssues([]qa.QualityIssue{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "verify issue"}}).ExecX(context.Background())
					}
				}
				f.snapshot.Strategy.Ruby.Enabled = false
				f.snapshot.RubyRetry = nil
				f.snapshot.GlossaryEnabled = true
				f.snapshot = completeWorkerSnapshot(t, f.snapshot)
				f.persistSnapshot(t)
				f.client.JobRound.UpdateOneID(f.roundIDs[0]).SetMode(mode).ExecX(context.Background())
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
				if err != nil {
					t.Fatal(err)
				}
				defer runtime.Close()
				if err := runtime.SetResponseLimits(1<<20, 64<<20); err != nil {
					t.Fatal(err)
				}
				eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{Glossary: glossary.NewMemory()}, progress.Nop{})
				if err != nil {
					t.Fatal(err)
				}
				defer eng.Close()
				doc, base := f.round(t, 0)
				eng.PrepareDocument(doc, nil)
				release := make(chan struct{})
				entered := make(chan struct{}, 1)
				var once sync.Once
				open := func() { once.Do(func() { close(release) }) }
				store := &handoffStoredRecordStore{RoundStore: base, entered: entered, release: release, failure: failure}
				round := eng.Rounds()[0]
				round.Store = store
				round.Runtime = runtime
				type outcome struct {
					result pipeline.RunRoundResult
					err    error
				}
				done := make(chan outcome, 1)
				joined := false
				go func() {
					result, err := pipeline.RunRound(ctx, round, doc, nil, f.logger, progress.Nop{})
					done <- outcome{result, err}
				}()
				cleanup := func() {
					open()
					cancel()
					if !joined {
						<-done
						joined = true
					}
				}
				defer cleanup()
				fail := func(format string, args ...any) { cleanup(); t.Fatalf(format, args...) }
				select {
				case <-entered:
				case got := <-done:
					joined = true
					fail("stored stopped before durable record: %+v %v", got.result, got.err)
				case <-ctx.Done():
					fail("stored never reached Record: %v", ctx.Err())
				}
				atRecord := runtime.Admission.PipelineSnapshot()
				legacy := runtime.Admission.Snapshot()
				if atRecord.MainOwners[0] != 1 || atRecord.MainOwnerPeak != 1 || atRecord.HandoffBytes <= atRecord.ProgressLane || legacy.PendingResults != 1 || legacy.MainInflight[0] != 0 {
					fail("stored direct-save has no held J/R owner at durable record: %+v %+v", atRecord, legacy)
				}
				open()
				got := <-done
				joined = true
				if failure {
					if got.err == nil {
						fail("stored durable failure swallowed")
					}
				} else if got.err != nil || len(got.result.Resolved) != 2 {
					fail("stored completion: %+v %v", got.result, got.err)
				}
				mu.Lock()
				n := calls
				mu.Unlock()
				if n != 1 {
					fail("stored durability retried model%d", n)
				}
				end := runtime.Admission.PipelineSnapshot()
				if end.MainOwners[0] != 0 || end.HandoffBytes != end.ProgressLane || runtime.Admission.Snapshot().PendingResults != 0 {
					fail("stored terminal owner leaked: %+v", end)
				}
				if err := verifyHandoffOwnedTerminal(end.OwnedPayloads, "wire"); err != nil {
					fail("stored actual backing observation: %v", err)
				}
				t.Logf("actual_HTTP=%d durable_failure=%v barrier=%+v network=%+v terminal=%+v", n, failure, atRecord, legacy, end)
			})
		}
	}
}

type handoffLateCancelStore struct {
	*roundStore
	entered chan pipeline.MainResponse
	release <-chan struct{}
}

func (s *handoffLateCancelStore) HandoffResponse(ctx context.Context, response pipeline.MainResponse) error {
	s.entered <- response
	// Fault sits outside the transaction; an in-flight late saver must join
	// before owner retirement and the production cancellation cleanup.
	<-s.release
	return s.roundStore.HandoffResponse(ctx, response)
}
func TestRubyHandoffAcceptanceHTTPCancelLateSaverJoinsBeforeCleanup(t *testing.T) {
	f, httpBarrier := newHandoffHTTPFixture(t, "translate", 1, 3, 3)
	httpBarrier.open()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.SetResponseLimits(1<<20, 64<<20); err != nil {
		t.Fatal(err)
	}
	if err := restoreHandoffAcceptanceInventory(ctx, f, runtime); err != nil {
		t.Fatal(err)
	}
	eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	doc, base := f.round(t, 0)
	eng.PrepareDocument(doc, nil)
	baselineVersions := map[int]int64{}
	for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(context.Background()) {
		baselineVersions[row.ID] = row.ContentVersion
	}
	entered := make(chan pipeline.MainResponse, 1)
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	store := &handoffLateCancelStore{roundStore: base, entered: entered, release: release}
	done := make(chan error, 1)
	joined := false
	go func() {
		_, err := pipeline.RunStagedRound(ctx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
		done <- err
	}()
	cleanup := func() {
		open()
		cancel()
		if !joined {
			<-done
			joined = true
		}
	}
	defer cleanup()
	fail := func(format string, args ...any) { cleanup(); t.Fatalf(format, args...) }
	var late pipeline.MainResponse
	select {
	case late = <-entered:
	case err := <-done:
		joined = true
		fail("Cancel saver barrier failed: %v", err)
	case <-ctx.Done():
		fail("Cancel saver did not reach raw transaction: %v", ctx.Err())
	}
	if _, err := f.jobs.CancelJob(context.Background(), f.ownerID, f.jobID); err != nil {
		fail("service Cancel: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		fail("Cancel returned before late raw saver joined: %v", err)
	default:
	}
	atCancel := runtime.Admission.PipelineSnapshot()
	if atCancel.MainOwners[0] != 1 || atCancel.Segments != 3 {
		fail("Cancel released owner/member before saver join: %+v", atCancel)
	}
	open()
	joinErr := <-done
	joined = true
	if joinErr == nil {
		fail("cancelled late response became accepted")
	}
	readCtx := context.Background()
	if err := workstate.NewStore(f.client).CleanupCancelledPayloads(readCtx, f.jobID); err != nil {
		fail("post-join payload cleanup: %v", err)
	}
	if err := runtime.ApplyCancelledPayloads(readCtx); err != nil {
		fail("post-join credit cleanup: %v", err)
	}
	state, err := workstate.NewStore(f.client).LoadRuntimeState(readCtx, f.jobID)
	if err != nil {
		fail("cancelled runtime state: %v", err)
	}
	if state.ProgressID != "" || state.ResponseID != "" || state.BusinessOwner != "" {
		fail("Cancel did not fence late generation and clear bindings: %+v", state)
	}
	if err := base.HandoffResponse(readCtx, late); err == nil {
		fail("post-cleanup late raw save bypassed Cancel generation")
	}
	for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(readCtx) {
		if row.TargetText != nil || row.Status != segment.StatusPending || row.ContentVersion != baselineVersions[row.ID] {
			fail("Cancel late save changed member%d target_nil=%v status%s version%d baseline%d", row.ID, row.TargetText == nil, row.Status, row.ContentVersion, baselineVersions[row.ID])
		}
	}
	if f.client.WorkCandidate.Query().CountX(readCtx) != 0 || f.client.JobRoundSegment.Query().CountX(readCtx) != 0 {
		fail("Cancel late save created candidate/checkpoint")
	}
	for _, row := range f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).AllX(readCtx) {
		if len(row.FreezePayload) != 0 || len(row.ResponsePayload) != 0 || len(row.CorrectedTargets) != 0 {
			fail("Cancel retained private request payload%s", row.Identity)
		}
	}
	end := runtime.Admission.PipelineSnapshot()
	if end.Segments != 0 || end.MainOwners[0] != 0 || end.CandidateBytes != 0 || end.GrowthBytes != 0 || end.HandoffBytes != end.ProgressLane {
		fail("Cancel retained credit after actual saver join: %+v", end)
	}
	httpBarrier.mu.Lock()
	calls := httpBarrier.calls
	httpBarrier.mu.Unlock()
	if calls != 1 {
		fail("Cancel spawned extra HTTP%d", calls)
	}
	t.Logf("real_HTTP=%d late_raw_before_transaction=true join_err=%v pre_join=%+v lifecycle=%+v post_join=%+v", calls, joinErr, atCancel, state, end)
}

type handoffGrowthCommitStore struct {
	*roundStore
	entered chan int64
	release <-chan struct{}
	once    sync.Once
}

func (s *handoffGrowthCommitStore) Commit(ctx context.Context, c *pipeline.Candidate, result pipeline.TranslatedSegment) (pipeline.CommitOutcome, error) {
	s.once.Do(func() { s.entered <- c.StoredBytes })
	select {
	case <-s.release:
	case <-ctx.Done():
		return pipeline.CommitRejected, ctx.Err()
	}
	return s.roundStore.Commit(ctx, c, result)
}

func TestRubyHandoffAcceptanceHTTPRecoveryGrowthThirtyTwo(t *testing.T) {
	for _, fullJ := range []bool{false, true} {
		name := "W_recovery"
		if fullJ {
			name = "future_raw_fills_J"
		}
		t.Run(name, func(t *testing.T) { runHandoffRecoveryGrowthThirtyTwo(t, fullJ) })
	}
}
func runHandoffRecoveryGrowthThirtyTwo(t *testing.T, fullJ bool) {
	const count = 32
	const initialBytes = 512 << 10
	counts := []int{count}
	if fullJ {
		counts = append(counts, 1)
	}
	f := newRubyPipelineFixture(t, counts...)
	f.snapshot.Rounds[0].Translate.Concurrency = 32
	f.snapshot.RubyRetry.Concurrency = 8
	batch, words, wait := 1, 0, 0
	f.snapshot.RubyRetry.BatchSize = &batch
	f.snapshot.RubyRetry.MaxWordsPerBatch = &words
	f.snapshot.RubyRetry.BatchWaitMS = &wait
	if fullJ {
		late := f.snapshot.Rounds[0]
		copyTranslate := *late.Translate
		late.Translate = &copyTranslate
		late.Translate.Concurrency = 1
		late.Translate.BatchSize = 1
		f.snapshot.Rounds = append(f.snapshot.Rounds, late)
		f.client.JobRound.UpdateOneID(f.roundIDs[1]).SetRoundIndex(1).ExecX(context.Background())
	}
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var rawJ0 int64
	defer func() { t.Logf("fixture reliable parent inventory bytes=%d", rawJ0) }()
	if fullJ {
		f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
			result := rubyPipelineResponse(request).(map[string]any)
			result["padding"] = ""
			encoded, _ := json.Marshal(result)
			// Keep the real provider HTTP envelope within R as well as content.
			// Its bounded transport includes escaped-content/envelope overhead.
			result["padding"] = strings.Repeat("j", (1<<20)-1024-len(encoded))
			check, _ := json.Marshal(result)
			if len(check) != (1<<20)-1024 {
				return nil, fmt.Errorf("raw fixture bytes%d", len(check))
			}
			return result, nil
		}
		gate := pipeline.NewPauseGate()
		seed, err := NewExecutionRuntime(f.snapshot, f.pool, gate, pipeline.DefaultCandidateLimits())
		if err != nil {
			t.Fatal(err)
		}
		if err := seed.SetResponseLimits(1<<20, 64<<20); err != nil {
			t.Fatal(err)
		}
		e, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, seed), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
		if err != nil {
			t.Fatal(err)
		}
		lateDoc, lateBase := f.round(t, 1)
		lateBase.round = 1
		e.PrepareDocument(lateDoc, nil)
		lateStore := &handoffPauseBoundaryStore{roundStore: lateBase, phase: "raw_after", pause: func(ctx context.Context) error {
			_, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID)
			if err == nil {
				gate.Pause()
			}
			return err
		}}
		_, err = pipeline.RunStagedRound(ctx, e.Rounds()[1], lateDoc, lateStore, seed, f.logger, progress.Nop{})
		e.Close()
		seed.Close()
		if err != nil || !gate.Paused() {
			for _, row := range f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).AllX(context.Background()) {
				t.Logf("full_R_seed state%s phase%s raw_bytes%d freeze%d gate_paused%v boundary_error%v", row.State, row.PromotionState, row.ResponseBytes, row.FreezeBytes, gate.Paused(), lateStore.err)
			}
			t.Fatalf("real future raw seed: %v", err)
		}
		headers, _, err := workstate.NewStore(f.client).LoadResponseHeaders(ctx, f.jobID, 0, 16)
		if err != nil || len(headers) != 1 || headers[0].ResponseBytes != (1<<20)-1024 {
			t.Fatalf("full actual R seed: %+v %v", headers, err)
		}
		rawJ0 = headers[0].FreezeBytes + headers[0].ResponseBytes + backend.RequestMetadataBytes
		if err := f.jobs.MarkJobPaused(ctx, f.jobID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
			t.Fatal(err)
		}
	}
	// Seed true DTO2 inventory; bytes are actual serialized bodies, not ledger
	// numbers. Each member is pending alignment and has an immutable baseline.
	doc, base := f.round(t, 0)
	ids := make([]int, count)
	for i := range ids {
		ids[i] = i
	}
	if _, err := base.Seal(ctx, ids); err != nil {
		t.Fatal(err)
	}
	items := []ruby.Item{{ID: "1", SourceBase: "source-a", SourceText: "original-a", Kind: "creative"}, {ID: "2", SourceBase: "source-b", SourceText: "original-b", Kind: "creative"}}
	for i, seg := range doc.Segments {
		makeCandidate := func(padding int) (*pipeline.Candidate, []byte) {
			target := "alpha beta " + strings.Repeat("x", padding)
			state, err := pipeline.PrepareAlignment(target, doc.Format, items, nil, ruby.ProtocolV2, nil)
			if err != nil {
				t.Fatal(err)
			}
			owned := seg
			owned.Target = target
			owned.Meta = map[string]any{"padding": ""}
			c := &pipeline.Candidate{DTOVersion: 2, ID: fmt.Sprintf("growth-%02d", i), WorkID: workstate.WorkIdentity(base.scope, seg.DBID), Version: 1, Index: i, Mode: pipeline.RoundModeTranslate, Format: doc.Format, Segment: owned, BaselineTarget: seg.Target, BaselineStatus: seg.Status, Alignment: state}
			data, err := c.Encode()
			if err != nil {
				t.Fatal(err)
			}
			return c, data
		}
		_, small := makeCandidate(0)
		_, probe := makeCandidate(64)
		factor := (len(probe) - len(small)) / 64
		if factor < 1 {
			t.Fatal("candidate has no finite target multiplicity")
		}
		c, data := makeCandidate((initialBytes - len(small) - 1024) / factor)
		if delta := initialBytes - len(data); delta > 0 {
			c.Segment.Meta = map[string]any{"padding": strings.Repeat("p", delta)}
			data, _ = c.Encode()
		}
		// Metadata syntax changes the fixed overhead; tune only the fixture's
		// harmless ASCII padding, never the configured W or target assertions.
		for tries := 0; len(data) != initialBytes && tries < 8; tries++ {
			delta := initialBytes - len(data)
			if c.Segment.Meta == nil {
				c.Segment.Meta = map[string]any{"padding": ""}
				data, _ = c.Encode()
				continue
			}
			p := c.Segment.Meta["padding"].(string)
			if len(p)+delta < 0 {
				t.Fatal("fixture padding cannot fit fixed DTO shape")
			}
			c.Segment.Meta["padding"] = strings.Repeat("p", len(p)+delta)
			data, _ = c.Encode()
		}
		if len(data) != initialBytes {
			t.Fatalf("actual candidate bytes%d want%d", len(data), initialBytes)
		}
		if err := base.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		if c.StoredBytes != initialBytes {
			t.Fatalf("Save changed measured candidate bytes%d", c.StoredBytes)
		}
	}
	var mu sync.Mutex
	httpCalls, active, peak := 0, 0, 0
	f.respond = func(ctx context.Context, request rubyPipelineRequest) (any, error) {
		if request.Segments != nil {
			return nil, fmt.Errorf("recovery retranslated main members")
		}
		mu.Lock()
		httpCalls++
		active++
		peak = max(peak, active)
		mu.Unlock()
		entries := make([]ruby.OutputEntry, 0, len(request.Missing))
		for _, m := range request.Missing {
			base := "alpha"
			if m.SourceBase == "source-b" {
				base = "beta"
			}
			entries = append(entries, ruby.OutputEntry{ID: m.ID, Base: base, Text: strings.Repeat("r", 128<<10), Kind: "creative", Occurrence: 1})
		}
		mu.Lock()
		active--
		mu.Unlock()
		return map[string]any{"ruby_output": entries}, nil
	}
	limits := pipeline.DefaultCandidateLimits()
	limits.Bytes = (16 << 20) + (1 << 19) // one existing half-MiB draft can grow to Q.
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	handoff := int64(64 << 20)
	if fullJ {
		handoff = 25 << 17 //3.125MiB fits one ordinary u plus the progress L.
	}
	if err := runtime.SetResponseLimits(1<<20, handoff); err != nil {
		t.Fatal(err)
	}
	if err := restoreHandoffAcceptanceInventory(ctx, f, runtime); err != nil {
		t.Fatal(err)
	}
	if fullJ {
		granted, err := runtime.Admission.PrepareMain("fresh_J_probe", 0, f.resourceIDs[0], 64, 64, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if !granted {
			t.Fatal("small prepared identity should fit residual known K")
		}
		permit, _, err := runtime.Admission.TryAdmit(ctx, backend.RequestAdmissionIntent{RequestID: "fresh_J_probe", Stage: backend.RequestStageMain, RoundIndex: 0, BackendID: f.snapshot.Rounds[0].Backend.ID}, nil)
		runtime.Admission.ReleasePrepared("fresh_J_probe")
		if permit != nil {
			permit.Release()
			t.Fatal("future full R unexpectedly granted another main response owner")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	eng.PrepareDocument(doc, nil)
	release := make(chan struct{})
	entered := make(chan int64, 1)
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	store := &handoffGrowthCommitStore{roundStore: base, entered: entered, release: release}
	type outcome struct {
		result pipeline.RunRoundResult
		err    error
	}
	done := make(chan outcome, 1)
	joined := false
	go func() {
		result, err := pipeline.RunStagedRound(ctx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
		done <- outcome{result, err}
	}()
	cleanup := func() {
		open()
		cancel()
		if !joined {
			<-done
			joined = true
		}
	}
	defer cleanup()
	fail := func(format string, args ...any) {
		cleanup()
		logHandoffAcceptanceFailure(t, f, runtime)
		t.Fatalf(format, args...)
	}
	var grown int64
	select {
	case grown = <-entered:
	case got := <-done:
		joined = true
		fail("growth stopped before durable candidate: %+v %v", got.result, got.err)
	case <-ctx.Done():
		fail("growth failed to reach durable candidate: %v", ctx.Err())
	}
	state, err := workstate.NewStore(f.client).LoadRuntimeState(context.Background(), f.jobID)
	if err != nil {
		fail("load growth episode: %v", err)
	}
	snap := runtime.Admission.PipelineSnapshot()
	expectedN := int64(32)
	if fullJ {
		expectedN++
	}
	if state.ProgressID == "" || snap.Segments != int(expectedN) {
		fail("fixed W recovery lost lifecycle identity or counted members twice: %+v %+v", state, snap)
	}
	if grown < 700<<10 || grown > 800<<10 {
		fail("real candidate did not grow about0.5->0.75MiB: %d", grown)
	}
	if snap.CandidateBytes+snap.GrowthBytes > max(limits.Bytes, limits.ItemBytes) || snap.ProgressID != state.ProgressID {
		fail("growth admitted all32 expansions/second borrower: %+v %+v", snap, state)
	}
	mu.Lock()
	callsAtCommit := httpCalls
	mu.Unlock()
	if callsAtCommit != 1 {
		fail("borrower held at first Commit allowed%d aux instead1", callsAtCommit)
	}
	open()
	select {
	case got := <-done:
		joined = true
		if got.err != nil || len(got.result.Resolved) != count || len(got.result.Unresolved) != 0 {
			fail("growth terminal members: %+v %v", got.result, got.err)
		}
	case <-ctx.Done():
		fail("growth did not terminally release: %v", ctx.Err())
	}
	mu.Lock()
	calls, actualPeak := httpCalls, peak
	mu.Unlock()
	if calls != 32 || actualPeak > 8 {
		fail("growth actual aux calls%d peak%d", calls, actualPeak)
	}
	if n := f.client.JobRoundSegment.Query().CountX(context.Background()); n != 32 {
		fail("growth checkpoint%d", n)
	}
	end := runtime.Admission.PipelineSnapshot()
	expectedRemaining := 0
	if fullJ {
		expectedRemaining = 1
	}
	if end.CandidateBytes != 0 || end.GrowthBytes != 0 || end.Segments != expectedRemaining || end.AuxOwners != 0 {
		fail("growth terminal credits: %+v", end)
	}
	usageCalls := 32
	if fullJ {
		usageCalls++
	}
	f.assertUsage(t, usageCalls)
	t.Logf("real_DTO_bytes=32*%d grown_first=%d W=16MiB Q=1MiB A=8 J=%d future_raw_fills_J=%v real_aux_calls=%d peak=%d fixed_episode=%+v first_commit=%+v terminal=%+v", initialBytes, grown, handoff, fullJ, calls, actualPeak, state, snap, end)
}

type handoffHTTPBarrier struct {
	mu                  sync.Mutex
	active, peak, calls int
	wireBytes           int64
	maxInputBytes       int64
	members             []int
	entered             chan struct{}
	release             chan struct{}
	once                sync.Once
	transform           func(any) any
}

func TestRubyHandoffAcceptanceRunnerDefaultQABounded(t *testing.T) {
	for _, mode := range []string{"translate", "revise", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			fixtureMode := mode
			if mode == "capacity" {
				fixtureMode = "translate"
			}
			f, barrier := newHandoffHTTPFixture(t, fixtureMode, 1, 1, 1)
			ctx := context.Background()
			f.snapshot.SourceLang, f.snapshot.TargetLang = "en", "zh"
			f.snapshot.Strategy.QA.Enabled = true
			f.snapshot.Strategy.QA.Checks = qa.AllCheckerNames()
			f.snapshot.GlossaryEnabled = true
			f.snapshot = completeWorkerSnapshot(t, f.snapshot)
			f.persistSnapshot(t)
			original := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(ctx)
			f.client.Segment.UpdateOneID(original.ID).SetSourceText("Cloud API 1").ExecX(ctx)
			project := f.client.Job.QueryProject(f.client.Job.GetX(ctx, f.jobID)).OnlyX(ctx)
			f.client.GlossaryEntry.Create().SetProjectID(project.ID).SetSourceKey("cloud").SetSource("Cloud").SetTarget("雲").SetMandatory(true).ExecX(ctx)
			f.client.GlossaryEntry.Create().SetProjectID(project.ID).SetSourceKey("api\x00bad").SetSource("API").SetTarget("bad").SetForbidden(true).ExecX(ctx)
			target := "bad 2 )"
			if mode == "capacity" {
				var parts []string
				for i := 0; i < 200; i++ {
					parts = append(parts, fmt.Sprintf("__LF_QA%03d", i))
				}
				target = strings.Join(parts, " ")
				f.runner.pipeCfg.Candidates = pipeline.CandidateLimits{Segments: 4, Bytes: 16 << 20, ItemBytes: 8 << 10}
			}
			barrier.transform = func(value any) any {
				result := value.(map[string]any)
				if fixtureMode == "revise" {
					revisions := result["revisions"].([]prompt.ReviseRevision)
					for i := range revisions {
						revisions[i].Target = target
					}
					result["revisions"] = revisions
				} else {
					translations := result["translations"].(map[string]string)
					for id := range translations {
						translations[id] = target
					}
				}
				return result
			}
			barrier.open()
			runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := f.runner.processJob(runCtx, f.jobID)
			cancel()
			row := f.client.Segment.GetX(ctx, original.ID)
			job := f.client.Job.GetX(ctx, f.jobID)
			barrier.mu.Lock()
			calls := barrier.calls
			barrier.mu.Unlock()
			if calls != 1 {
				t.Fatalf("default QA replayed HTTP: calls%d error%v", calls, err)
			}
			if mode == "capacity" {
				if row.TargetText != nil || f.client.JobRoundSegment.Query().CountX(ctx) != 0 || job.Status != service.JobStatusFailed {
					t.Fatalf("default QA capacity accepted candidate or altered baseline: target%v checkpoint%d job%s err%v", row.TargetText, f.client.JobRoundSegment.Query().CountX(ctx), job.Status, err)
				}
				items := f.client.WorkItem.Query().Where(workitem.JobIDEQ(f.jobID)).AllX(ctx)
				capacity := job.ErrorMessage != nil && strings.Contains(*job.ErrorMessage, "capacity")
				for _, item := range items {
					capacity = capacity || strings.Contains(item.LastError, "capacity")
				}
				if !capacity && !errors.Is(err, backend.ErrBuildCapacity) {
					t.Fatalf("capacity cause not retained: job%s jobError%v err%v items%+v", job.Status, job.ErrorMessage, err, items)
				}
				if job.ErrorMessage != nil {
					t.Logf("durable QA capacity cause=%s", *job.ErrorMessage)
				}
			} else {
				if err != nil || job.Status != service.JobStatusCompleted || row.TargetText == nil || *row.TargetText != target || row.Status != segment.StatusTranslated || f.client.JobRoundSegment.Query().CountX(ctx) != 1 {
					cause := ""
					if job.ErrorMessage != nil {
						cause = *job.ErrorMessage
					}
					t.Fatalf("default QA legitimate candidate failed: job%s target%v status%s err%v cause=%s", job.Status, row.TargetText, row.Status, err, cause)
				}
				codes := map[string]bool{}
				for _, issue := range row.QualityIssues {
					codes[issue.Code] = true
				}
				for _, code := range []string{qa.CheckForbiddenTerm, qa.CheckTermInconsistency, qa.CheckNumberMismatch, qa.CheckPunctuationPairing} {
					if !codes[code] {
						t.Fatalf("default bounded checker silently omitted%s: %+v", code, row.QualityIssues)
					}
				}
				if fixtureMode == "revise" && codes[qa.IssueCodeCalque] {
					t.Fatal("Revise bounded reconciliation kept repaired target code")
				}
			}
			f.assertUsage(t, 1)
			t.Logf("real_default_QA=true mode=%s HTTP=%d target_bytes=%d checkpoint=%d job=%s issues=%d error=%v", mode, calls, len(target), f.client.JobRoundSegment.Query().CountX(ctx), job.Status, len(row.QualityIssues), err)
		})
	}
}

func (b *handoffHTTPBarrier) open() { b.once.Do(func() { close(b.release) }) }

func (b *handoffHTTPBarrier) serve(w http.ResponseWriter, r *http.Request) {
	var wire struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var request rubyEntryRequest
	for _, message := range wire.Messages {
		if message.Role == "user" {
			if err := json.Unmarshal([]byte(message.Content), &request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	}
	var content any
	memberCount := 0
	if request.Task == "revise_translation" {
		var inputs []prompt.ReviseSegment
		if err := json.Unmarshal(request.Segments, &inputs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		revisions := make([]prompt.ReviseRevision, len(inputs))
		memberCount = len(inputs)
		for i, in := range inputs {
			revisions[i] = prompt.ReviseRevision{ID: in.ID, Target: "accepted-" + in.Source}
		}
		content = map[string]any{"revisions": revisions}
	} else {
		var inputs map[string]prompt.SegmentDetail
		if err := json.Unmarshal(request.Segments, &inputs); err != nil || len(inputs) == 0 {
			http.Error(w, "missing translation members", http.StatusBadRequest)
			return
		}
		translations := make(map[string]string, len(inputs))
		for id, in := range inputs {
			if in.Translate {
				memberCount++
				translations[id] = "accepted-" + in.Source
			}
		}
		content = map[string]any{"translations": translations}
	}
	b.mu.Lock()
	b.calls++
	b.active++
	b.peak = max(b.peak, b.active)
	b.wireBytes += r.ContentLength
	inputBytes := r.ContentLength
	for _, message := range wire.Messages {
		inputBytes += int64(len(message.Content))
	}
	b.maxInputBytes = max(b.maxInputBytes, inputBytes)
	b.members = append(b.members, memberCount)
	b.mu.Unlock()
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
	case <-r.Context().Done():
		b.mu.Lock()
		b.active--
		b.mu.Unlock()
		return
	}
	if b.transform != nil {
		content = b.transform(content)
	}
	encoded, err := json.Marshal(content)
	b.mu.Lock()
	// Provider work has completed before the first response byte. Counting a
	// deferred handler epilogue would overcount an already completed HTTP call.
	b.active--
	b.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(encoded)}, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 11, "total_tokens": 18},
	})
}

func newHandoffHTTPFixture(t *testing.T, mode string, concurrency, count, batch int) (*rubyPipelineFixture, *handoffHTTPBarrier) {
	t.Helper()
	barrier := &handoffHTTPBarrier{entered: make(chan struct{}, count), release: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(barrier.serve))
	t.Cleanup(server.Close)
	t.Cleanup(barrier.open)
	f := newRubyPipelineFixtureWithClient(t, newRegistryTestClient(t), server.URL, count)
	f.snapshot.Strategy.Ruby.Enabled = false
	f.snapshot.RubyRetry = nil
	f.snapshot.Rounds[0].Translate.Concurrency = concurrency
	f.snapshot.Rounds[0].Translate.BatchSize = batch
	f.snapshot.Rounds[0].Translate.Retry.MaxAttempts = 0
	ctx := context.Background()
	for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(ctx) {
		u := f.client.Segment.UpdateOneID(row.ID).SetSourceText(fmt.Sprintf("short-segment-%03d", row.SegmentIndex))
		if mode == "revise" {
			u.SetTargetText("original target").SetStatus(segment.StatusTranslated).SetQualityIssues([]qa.QualityIssue{{Code: "calque", Severity: qa.SeverityWarning, Message: "revise"}})
		}
		u.ExecX(ctx)
	}
	if mode == "revise" {
		provider := f.snapshot.Rounds[0].Backend
		f.snapshot.Rounds = []service.JobRoundSnapshot{{Mode: "revise", Backend: provider, Revise: &service.JobReviseRoundSnapshot{
			TemplateContent: templates.EmbeddedReviseTemplate(), BatchSize: batch, Concurrency: concurrency, SegmentScope: "with_issues", IssueCodes: []string{"calque"},
		}}}
		f.client.JobRound.UpdateOneID(f.roundIDs[0]).SetMode("revise").ExecX(ctx)
	}
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	return f, barrier
}

func TestRubyHandoffAcceptanceHTTPMainConcurrency(t *testing.T) {
	for _, mode := range []string{"translate", "revise"} {
		for _, concurrency := range []int{1, 4, 16, 32} {
			t.Run(fmt.Sprintf("%s/C%d", mode, concurrency), func(t *testing.T) {
				runHandoffHTTPCase(t, mode, concurrency, 2*concurrency+1, 1, 1, concurrency, 64<<20, false)
			})
		}
	}
}

func TestRubyHandoffAcceptanceHTTPBoundedProtectionContextAndMetadata(t *testing.T) {
	for _, kind := range []string{"protected_context_escapes", "metadata_depth65", "metadata_nodes65537", "response_over_R"} {
		t.Run(kind, func(t *testing.T) {
			count := 1
			if kind == "protected_context_escapes" {
				count = 5
			}
			f, barrier := newHandoffHTTPFixture(t, "translate", 4, count, 1)
			barrier.open()
			f.snapshot.Strategy.Protect.Enabled = true
			f.snapshot.Strategy.Protect.Rules = []string{"code", "link", "placeholder", "xml"}
			f.snapshot.Strategy.Context.Enabled = true
			f.snapshot.Strategy.Context.Before = 1
			f.snapshot.Strategy.Context.After = 1
			f.snapshot.Strategy.Context.MaxChars = 65536
			f.snapshot = completeWorkerSnapshot(t, f.snapshot)
			f.persistSnapshot(t)
			for _, row := range f.client.Segment.Query().AllX(context.Background()) {
				source := fmt.Sprintf("segment%d `keep_code` [same link](https://example.invalid/a?q=1&b=2) %%s \"quoted\" \\slash\n%s", row.SegmentIndex, strings.Repeat("大量原文 ", 900))
				update := f.client.Segment.UpdateOneID(row.ID).SetSourceText(source)
				if kind == "metadata_depth65" {
					update.SetMeta(`{"nested":` + strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65) + "}")
				}
				if kind == "metadata_nodes65537" {
					update.SetMeta(`{"nodes":[` + strings.Repeat("0,", 65536) + "0]}")
				}
				update.ExecX(context.Background())
			}
			if kind == "response_over_R" {
				barrier.transform = func(content any) any {
					r := content.(map[string]any)
					r["large_unknown"] = strings.Repeat("x", 2<<20)
					return r
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			if err = runtime.SetResponseLimits(1<<20, 64<<20); err != nil {
				t.Fatal(err)
			}
			eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			doc, store := f.round(t, 0)
			eng.PrepareDocument(doc, nil)
			result, runErr := pipeline.RunStagedRound(ctx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
			cancel()
			barrier.mu.Lock()
			calls := barrier.calls
			wire := barrier.wireBytes
			barrier.mu.Unlock()
			if kind == "protected_context_escapes" {
				if runErr != nil || len(result.Resolved) != count || calls != count {
					t.Fatalf("protected/context actual pipeline: %+v %v HTTP%d", result, runErr, calls)
				}
				for _, row := range f.client.Segment.Query().AllX(context.Background()) {
					if row.TargetText == nil || *row.TargetText != "accepted-"+row.SourceText {
						t.Fatalf("protected mapping/context changed member%d", row.ID)
					}
				}
				f.assertUsage(t, count)
			} else {
				wantHTTP := 0
				if kind == "response_over_R" {
					wantHTTP = 1
				}
				if calls != wantHTTP || len(result.Resolved) != 0 {
					t.Fatalf("bounded case %s sent/confirmed extra work: HTTP%d result%+v err%v", kind, calls, result, runErr)
				}
				if kind != "response_over_R" && !errors.Is(runErr, backend.ErrBuildCapacity) {
					t.Fatalf("metadata was not rejected by bounded build: %v", runErr)
				}
				if kind == "response_over_R" {
					if !errors.Is(runErr, backend.ErrResponseTooLarge) {
						t.Fatalf("R rejection lost returned capacity cause: %v", runErr)
					}
					record := f.client.WorkRequest.Query().OnlyX(context.Background())
					if record.State != "failed" || !strings.Contains(record.LastError, "exceeds byte limit") {
						t.Fatalf("R failure lost exact cause: %s %s", record.State, record.LastError)
					}
				}
				for _, row := range f.client.Segment.Query().AllX(context.Background()) {
					if row.TargetText != nil || row.Status != segment.StatusPending {
						t.Fatalf("bounded failure changed baseline member%d", row.ID)
					}
				}
				if n := f.client.JobRoundSegment.Query().CountX(context.Background()); n != 0 {
					t.Fatalf("bounded failure checkpoint%d", n)
				}
				for _, record := range f.client.WorkRequest.Query().AllX(context.Background()) {
					if len(record.ResponsePayload) != 0 || record.ResponseBytes != 0 {
						t.Fatalf("out-of-budget raw persisted")
					}
				}
			}
			t.Logf("kind=%s actual_HTTP=%d full_request_wire_total=%d resolved=%d err=%v accounting=%+v", kind, calls, wire, len(result.Resolved), runErr, runtime.Admission.PipelineSnapshot())
		})
	}
}

func TestRubyHandoffAcceptanceHTTPHandoffCapacity(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		count, batch, actualBatch, peak int
		j                               int64
		s, c                            int
	}{
		{"default_batch10_complete10_J64", 256, 10, 10, 25, 64 << 20, 0, 0},
		{"default_batch10_complete10_J16", 256, 10, 10, 3, 16 << 20, 0, 0},
		{"explicit_batch1_J16", 65, 1, 1, 12, 16 << 20, 0, 0},
		{"complete10_S321_C32", 321, 10, 10, 32, 64 << 20, 321, 32},
		{"complete10_S256_C4", 41, 10, 10, 4, 64 << 20, 256, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := pipeline.DefaultCandidateLimits()
			if tc.s > 0 {
				limits.Segments = tc.s
			}
			c := 32
			if tc.c > 0 {
				c = tc.c
			}
			runHandoffHTTPCase(t, "translate", c, tc.count, tc.batch, tc.actualBatch, tc.peak, tc.j, true, limits)
		})
	}
}

func runHandoffHTTPCase(t *testing.T, mode string, concurrency, count, batch, actualBatch, wantPeak int, handoff int64, inline bool, configured ...pipeline.CandidateLimits) {
	f, httpBarrier := newHandoffHTTPFixture(t, mode, concurrency, count, batch)
	resources := engine.RuntimeResources{}
	if inline {
		cfg := execution.DefaultInlineTermExtraction()
		cfg.Enabled = true
		f.snapshot.GlossaryEnabled = true
		f.snapshot.Rounds[0].Translate.InlineTermExtraction = &cfg
		// A frozen short system keeps actual full K within the design's
		// 8KiB short-input fixture; the user encoder remains production.
		f.snapshot.Rounds[0].Translate.Prompt.Content = "Translate {{.SourceLang}} to {{.TargetLang}}. Return translations and extracted terms."
		f.snapshot = completeWorkerSnapshot(t, f.snapshot)
		f.persistSnapshot(t)
		resources.Glossary = glossary.NewMemory()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	limits := pipeline.DefaultCandidateLimits()
	if len(configured) > 0 {
		limits = configured[0]
	}
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.SetResponseLimits(1<<20, handoff); err != nil {
		t.Fatal(err)
	}
	eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, resources, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	doc, durable := f.round(t, 0)
	eng.PrepareDocument(doc, nil)
	type outcome struct {
		result pipeline.RunRoundResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := pipeline.RunStagedRound(ctx, eng.Rounds()[0], doc, durable, runtime, f.logger, progress.Nop{})
		done <- outcome{result, err}
	}()
	joined := false
	cleanup := func() {
		httpBarrier.open()
		cancel()
		if !joined {
			select {
			case <-done:
				joined = true
			case <-time.After(10 * time.Second):
				t.Log("pipeline still draining after releasing HTTP and cancelling")
				<-done
				joined = true
			}
		}
	}
	defer cleanup()
	fail := func(format string, args ...any) {
		cleanup()
		logHandoffAcceptanceFailure(t, f, runtime)
		// All launched work is released and joined before Fatal.
		t.Fatalf(format, args...)
	}
	barrierDeadline := time.NewTimer(10 * time.Second)
	defer barrierDeadline.Stop()
	for i := 0; i < wantPeak; i++ {
		select {
		case <-httpBarrier.entered:
		case got := <-done:
			joined = true
			fail("round stopped before HTTP barrier %d/%d: %+v %v", i, wantPeak, got.result, got.err)
		case <-barrierDeadline.C:
			fail("actual HTTP barrier reached %d/%d; ledger=%+v", i, wantPeak, runtime.Admission.PipelineSnapshot())
		}
	}
	atBarrier := runtime.Admission.PipelineSnapshot()
	if atBarrier.MainOwners[0] != wantPeak || atBarrier.HandoffBytes > handoff || atBarrier.CandidateBytes+atBarrier.GrowthBytes > max(limits.Bytes, limits.ItemBytes) || atBarrier.Segments > limits.Segments {
		fail("HTTP barrier and credits disagree: %+v", atBarrier)
	}
	if atBarrier.OwnedPayloads.Current["freeze"] < 1 || atBarrier.OwnedPayloads.Current["wire"] < 1 {
		fail("actual frozen/wire backing absent during held HTTP: %+v", atBarrier.OwnedPayloads)
	}
	if inline && concurrency == 32 {
		reason := "configured_concurrency"
		if wantPeak < concurrency {
			reason = "response_handoff"
			if int64(wantPeak*actualBatch)+int64(actualBatch) > int64(limits.Segments-1) {
				reason = "member_capacity"
			}
		}
		if atBarrier.RequestedMain[0] != concurrency || atBarrier.EffectiveMain[0] != wantPeak || atBarrier.MainCapacityReason[0] != reason || atBarrier.MainOwnerPeak != wantPeak {
			fail("requested/effective/reason/response-owner peak disagree with real HTTP barrier: %+v", atBarrier)
		}
	}
	maxFreeze := int64(0)
	for _, request := range f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID), workrequest.StateNEQ("cancelled_before_dispatch")).Select(workrequest.FieldFreezeBytes, workrequest.FieldBusinessBound).AllX(context.Background()) {
		maxFreeze = max(maxFreeze, request.FreezeBytes)
		if inline && request.BusinessBound != int64(actualBatch)<<20 {
			fail("Bmax=%d want nQ=%d", request.BusinessBound, int64(actualBatch)<<20)
		}
	}
	if inline && atBarrier.ProgressLane != (2<<20)+(32<<10)+(int64(actualBatch)<<20) {
		fail("inline business lane=%d; n=%d", atBarrier.ProgressLane, actualBatch)
	}
	httpBarrier.open()
	var got outcome
	select {
	case got = <-done:
		joined = true
	case <-ctx.Done():
		fail("round failed to finish: %v", ctx.Err())
	}
	if got.err != nil || len(got.result.Resolved) != count || len(got.result.Unresolved) != 0 {
		fail("incomplete round: %+v %v", got.result, got.err)
	}
	httpBarrier.mu.Lock()
	peak, calls, wireBytes, inputBytes := httpBarrier.peak, httpBarrier.calls, httpBarrier.wireBytes, httpBarrier.maxInputBytes
	memberCounts := append([]int(nil), httpBarrier.members...)
	httpBarrier.mu.Unlock()
	wantCalls := (count + actualBatch - 1) / actualBatch
	if peak != wantPeak || calls != wantCalls {
		fail("real HTTP peak=%d calls=%d; want=%d/%d", peak, calls, wantPeak, wantCalls)
	}
	for i, n := range memberCounts {
		if n < 1 || n > actualBatch || i < wantPeak && n != actualBatch {
			fail("actual HTTP request %d members=%d; fair n=%d", i, n, actualBatch)
		}
	}
	if inline && maxFreeze+inputBytes > 8<<10 {
		fail("full actual K=%d exceeds design fixture 8KiB (freeze=%d input=%d)", maxFreeze+inputBytes, maxFreeze, inputBytes)
	}
	readCtx := context.Background()
	for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(readCtx) {
		if row.TargetText == nil || *row.TargetText != "accepted-"+row.SourceText || row.Status != segment.StatusTranslated {
			fail("SQLite member did not commit the unique accepted target: id=%d source=%q target=%v status=%s", row.ID, row.SourceText, row.TargetText, row.Status)
		}
	}
	if n := f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).CountX(readCtx); n != count {
		fail("durable checkpoints=%d want=%d", n, count)
	}
	if n := f.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(f.roundIDs[0]), workitem.StateNEQ("resolved")).CountX(readCtx); n != 0 {
		fail("unfinished durable members=%d", n)
	}
	for _, request := range f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).AllX(readCtx) {
		if request.State == "cancelled_before_dispatch" {
			if len(request.ResponsePayload) != 0 || len(request.FreezePayload) != 0 || request.ResponseBytes != 0 || request.FreezeBytes != 0 {
				fail("cancelled descriptor retained backing: %s", request.Identity)
			}
			continue
		}
		if request.State != "completed" || request.PromotionState != "promoted" || len(request.ResponsePayload) != 0 || len(request.FreezePayload) != 0 {
			fail("terminal raw carrier retained payload or incomplete promotion: %s/%s", request.State, request.PromotionState)
		}
	}
	f.assertUsage(t, wantCalls)
	end := runtime.Admission.PipelineSnapshot()
	if end.MainOwners[0] != 0 || end.AuxOwners != 0 || end.Segments != 0 || end.CandidateBytes != 0 || end.GrowthBytes != 0 || end.PreparedBytes != 0 || end.AuxInputBytes != 0 || end.AuxResponseBytes != 0 || end.HandoffBytes != end.ProgressLane {
		fail("terminal work retained an owner/payload credit: %+v", end)
	}
	if err := verifyHandoffOwnedTerminal(end.OwnedPayloads, "freeze", "raw", "wire", "candidate"); err != nil {
		fail("main actual backing observation: %v", err)
	}
	t.Logf("mode=%s C=%d batch=%d fair_n=%d inline_rewrite=%v J=%d actual_K_bound=%d freeze_max=%d input_max=%d real_HTTP_peak=%d calls=%d members=%d wire_bytes=%d barrier=%+v terminal=%+v", mode, concurrency, batch, actualBatch, inline, handoff, maxFreeze+inputBytes, maxFreeze, inputBytes, peak, calls, count, wireBytes, atBarrier, end)
}

func TestRubyHandoffAcceptanceHTTPWorkAndWindowLimits(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		concurrency, count, batch, n, peak int
		limits                             pipeline.CandidateLimits
	}{
		{"S_below_C", 16, 9, 1, 1, 4, pipeline.CandidateLimits{Segments: 5, Bytes: 16 << 20, ItemBytes: 1 << 20}},
		{"fewer_members_than_C", 32, 3, 1, 1, 3, pipeline.DefaultCandidateLimits()},
		{"tail_batch", 4, 14, 3, 3, 4, pipeline.DefaultCandidateLimits()},
		{"S1_W_below_Q", 32, 3, 1, 1, 1, pipeline.CandidateLimits{Segments: 1, Bytes: 1, ItemBytes: 1 << 20}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runHandoffHTTPCase(t, "translate", tc.concurrency, tc.count, tc.batch, tc.n, tc.peak, 64<<20, false, tc.limits)
		})
	}
}

func TestRubyHandoffAcceptanceHTTPAuxEight(t *testing.T) {
	for _, counts := range [][]int{{16}, {8, 8}} {
		t.Run(fmt.Sprintf("resources%d", len(counts)), func(t *testing.T) { runHandoffHTTPAuxEight(t, counts...) })
	}
}
func runHandoffHTTPAuxEight(t *testing.T, counts ...int) {
	runHandoffHTTPAuxScopes(t, false, counts...)
}
func TestRubyHandoffAcceptanceHTTPAuxEightAcrossJobs(t *testing.T) {
	runHandoffHTTPAuxScopes(t, true, 16, 16)
}

func TestRubyHandoffAcceptanceRunnerPauseAfterAuxGrantKeepsQ(t *testing.T) {
	runHandoffAuxGrantPause(t, false)
}
func TestRubyHandoffAcceptanceRunnerRetryAfterAuxGrantKeepsQ(t *testing.T) {
	runHandoffAuxGrantPause(t, true)
}
func runHandoffAuxGrantPause(t *testing.T, retry bool) {
	f := newRubyPipelineFixture(t, 1)
	f.snapshot.Rounds[0].Translate.BatchSize = 1
	f.snapshot.RubyRetry.Concurrency = 1
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	f.runner.pipeCfg.Candidates = pipeline.CandidateLimits{Segments: 4, Bytes: 16 << 20, ItemBytes: 1 << 20}
	f.runner.pipeCfg.MaxResponseBytes = 1 << 20
	f.runner.pipeCfg.ResponseHandoffBytes = 64 << 20
	var once sync.Once
	var pauseErr error
	f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
		if request.Segments == nil {
			// Leave one real missing alignment member unresolved at this grant.
			// A fully completed aux may legitimately reach terminal while draining.
			if len(request.Missing) == 2 {
				request.Missing = request.Missing[:1]
			}
			once.Do(func() {
				_, pauseErr = f.jobs.PauseJob(context.Background(), f.ownerID, f.jobID)
				if pauseErr == nil && !f.runner.Pause(f.jobID) {
					pauseErr = fmt.Errorf("runner aux pause gate missing")
				}
			})
		}
		return rubyPipelineResponse(request), pauseErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := f.runner.processJob(ctx, f.jobID)
	cancel()
	if err != nil || pauseErr != nil {
		t.Fatalf("aux grant Pause: %v %v", err, pauseErr)
	}
	read := context.Background()
	if j := f.client.Job.GetX(read, f.jobID); j.Status != service.JobStatusPaused || j.RetryEpoch != 0 {
		t.Fatalf("aux did not drain to paused: %s/%d", j.Status, j.RetryEpoch)
	}
	if n := f.client.JobRoundSegment.Query().CountX(read); n != 0 {
		t.Fatalf("aux Pause confirmed%d before Commit", n)
	}
	ws := workstate.NewStore(f.client)
	headers, _, err := ws.LoadCandidateHeaders(read, f.jobID, 0, 16)
	if err != nil || len(headers) != 1 || !headers[0].GrowthReserved || headers[0].PayloadBytes <= 0 {
		t.Fatalf("durable aux Q lost at Pause: %+v %v", headers, err)
	}
	state, err := ws.LoadRuntimeState(read, f.jobID)
	if err != nil || state.Version != 1 {
		t.Fatalf("ordinary aux lost its lifecycle state: %+v %v", state, err)
	}
	if retry {
		// Another scope may fail the Job after this admitted aux has joined and
		// saved a partial candidate. Reproduce that terminal Job state, then use
		// the real service Retry transaction rather than editing epoch or credit.
		f.client.Job.UpdateOneID(f.jobID).SetStatus(service.JobStatusFailed).ExecX(read)
		f.client.JobResource.UpdateOneID(f.jobResources[0]).SetStatus(service.JobResourceStatusFailed).ExecX(read)
		f.client.JobRound.UpdateOneID(f.roundIDs[0]).SetStatus(service.JobRoundStatusFailed).ExecX(read)
		retried, err := f.jobs.RetryJob(read, f.ownerID, f.jobID)
		if err != nil || retried.RetryEpoch != 1 {
			t.Fatalf("service Retry epoch: %+v %v", retried, err)
		}
		fresh, _, err := ws.LoadCandidateHeaders(read, f.jobID, 0, 16)
		if err != nil || len(fresh) != 1 || fresh[0].ID != headers[0].ID || fresh[0].PayloadBytes != headers[0].PayloadBytes || !fresh[0].GrowthReserved {
			t.Fatalf("Retry reset prior durable full Q or candidate: before%+v after%+v err%v", headers, fresh, err)
		}
		members := f.client.WorkItem.Query().Where(workitem.JobIDEQ(f.jobID)).AllX(read)
		if len(members) != 1 || !members[0].MemberOwned || members[0].RetryEpoch != 1 || members[0].AlignmentAttempts != 0 || members[0].AlignmentNetworkAttempts != 0 {
			t.Fatalf("Retry S/cursor not preserved with fresh attempt budget: %+v", members)
		}
	}
	// Recreate the production header-only RAM inventory after all old workers
	// joined. A persisted grant owns full Q even in an ordinary, inactive episode.
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, f.runner.pipeCfg.Candidates)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.SetResponseLimits(1<<20, 64<<20); err == nil {
		err = restoreHandoffAcceptanceInventory(read, f, runtime)
	}
	if err != nil {
		runtime.Close()
		t.Fatal(err)
	}
	snap := runtime.Admission.PipelineSnapshot()
	if snap.CandidateBytes+snap.GrowthBytes != 1<<20 || snap.Segments != 1 {
		runtime.Close()
		t.Fatalf("restarted aux grant forgot Q: %+v", snap)
	}
	runtime.Close()
	main, aux, _, _ := f.calls()
	if main != 1 || aux != 1 {
		t.Fatalf("Pause actual HTTP main/aux%d/%d", main, aux)
	}
	if !retry {
		if _, err = f.jobs.ResumeJob(read, f.ownerID, f.jobID); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel = context.WithTimeout(read, 15*time.Second)
	err = f.runner.processJob(ctx, f.jobID)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	f.assertAccepted(t, 0, 1)
	f.assertUsage(t, 3)
	main, aux, _, _ = f.calls()
	if main != 1 || aux != 2 {
		t.Fatalf("Resume replayed main or failed to request the one remaining alignment%d/%d", main, aux)
	}
	if end, _, err := ws.LoadCandidateHeaders(read, f.jobID, 0, 16); err != nil || len(end) != 0 {
		t.Fatalf("terminal aux candidate remains: %+v %v", end, err)
	}
	t.Logf("actual_HTTP_main=1 aux=2(one before/one after Pause) serviceRetry=%v ordinary_episode=false paused_checkpoint=0 persisted_Q=%d restored=%+v", retry, 1<<20, snap)
}

func runHandoffHTTPAuxScopes(t *testing.T, twoJobs bool, counts ...int) {
	count := 0
	for _, n := range counts {
		count += n
	}
	f := newRubyPipelineFixture(t, counts...)
	f.snapshot.Rounds[0].Translate.Concurrency = 16
	f.snapshot.Rounds[0].Translate.BatchSize = 1
	f.snapshot.RubyRetry.Concurrency = 8
	batch, words, waitMS := 1, 0, 0
	f.snapshot.RubyRetry.BatchSize = &batch
	f.snapshot.RubyRetry.MaxWordsPerBatch = &words
	f.snapshot.RubyRetry.BatchWaitMS = &waitMS
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	fixtures := []*rubyPipelineFixture{f, f}
	if twoJobs {
		original := f.client.Job.GetX(context.Background(), f.jobID)
		second := f.client.Job.Create().SetProjectID(original.ProjectID).SetExecutionPlanID(original.ExecutionPlanID).SetCreatedByID(f.ownerID).SetStatus("running").SetResourceCount(1).SaveX(context.Background())
		f.client.JobResource.UpdateOneID(f.jobResources[1]).SetJobID(second.ID).ExecX(context.Background())
		f.client.JobRound.UpdateOneID(f.roundIDs[1]).SetJobID(second.ID).ExecX(context.Background())
		other := &rubyPipelineFixture{client: f.client, jobs: f.jobs, runner: f.runner, factory: f.factory, snapshot: f.snapshot, pool: f.pool, logger: f.logger, ownerID: f.ownerID, jobID: second.ID, resourceIDs: []int{f.resourceIDs[1]}, jobResources: []int{f.jobResources[1]}, roundIDs: []int{f.roundIDs[1]}}
		other.persistSnapshot(t)
		fixtures[1] = other
	}
	entered, release := make(chan struct{}, count), make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	var mu sync.Mutex
	active, peak, calls := 0, 0, 0
	f.respond = func(ctx context.Context, request rubyPipelineRequest) (any, error) {
		if request.Segments != nil {
			return rubyPipelineResponse(request), nil
		}
		mu.Lock()
		active++
		peak = max(peak, active)
		calls++
		mu.Unlock()
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			mu.Lock()
			active--
			mu.Unlock()
			return nil, ctx.Err()
		}
		mu.Lock()
		active--
		mu.Unlock()
		return rubyPipelineResponse(request), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.SetResponseLimits(1<<20, 64<<20); err != nil {
		t.Fatal(err)
	}
	eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	type resourceRound struct {
		doc     *pipeline.Document
		store   *roundStore
		eng     *engine.Engine
		runtime *pipeline.ExecutionRuntime
	}
	var resources []resourceRound
	runtimes := []*pipeline.ExecutionRuntime{runtime}
	for i := range counts {
		currentEng, currentRuntime, fixture, resourceIndex := eng, runtime, f, i
		if twoJobs && i == 1 {
			fixture, resourceIndex = fixtures[1], 0
			currentRuntime, err = NewExecutionRuntime(fixture.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer currentRuntime.Close()
			if err = currentRuntime.SetResponseLimits(1<<20, 64<<20); err != nil {
				t.Fatal(err)
			}
			currentEng, err = fixture.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, currentRuntime), fixture.snapshot, engine.RuntimeResources{}, progress.Nop{})
			if err != nil {
				t.Fatal(err)
			}
			defer currentEng.Close()
			runtimes = append(runtimes, currentRuntime)
		}
		doc, store := fixture.round(t, resourceIndex)
		currentEng.PrepareDocument(doc, nil)
		resources = append(resources, resourceRound{doc, store, currentEng, currentRuntime})
	}
	type outcome struct {
		result pipeline.RunRoundResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		var joined sync.WaitGroup
		var lock sync.Mutex
		var all outcome
		for _, resource := range resources {
			joined.Add(1)
			go func(resource resourceRound) {
				defer joined.Done()
				result, err := pipeline.RunStagedRound(ctx, resource.eng.Rounds()[0], resource.doc, resource.store, resource.runtime, f.logger, progress.Nop{})
				lock.Lock()
				defer lock.Unlock()
				all.result.Resolved = append(all.result.Resolved, result.Resolved...)
				all.result.Unresolved = append(all.result.Unresolved, result.Unresolved...)
				if all.err == nil {
					all.err = err
				}
			}(resource)
		}
		joined.Wait()
		done <- all
	}()
	joined := false
	cleanup := func() {
		open()
		cancel()
		if !joined {
			<-done
			joined = true
		}
	}
	defer cleanup()
	fail := func(format string, args ...any) {
		cleanup()
		logHandoffAcceptanceFailure(t, f, runtime)
		t.Fatalf(format, args...)
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	wantPeak := 8
	if twoJobs {
		wantPeak = 16
	}
	for i := 0; i < wantPeak; i++ {
		select {
		case <-entered:
		case got := <-done:
			joined = true
			fail("aux stopped before real HTTP barrier %d/8: %+v %v", i, got.result, got.err)
		case <-deadline.C:
			fail("only %d/8 actual aux HTTP calls reached barrier: %+v", i, runtime.Admission.PipelineSnapshot())
		}
	}
	atBarrier := runtime.Admission.PipelineSnapshot()
	for _, currentRuntime := range runtimes {
		current := currentRuntime.Admission.PipelineSnapshot()
		if current.AuxOwners != 8 || current.AuxInputBytes <= 0 || current.AuxResponseBytes != 8<<20 || current.GrowthBytes <= 0 || current.CandidateBytes+current.GrowthBytes > 16<<20 || current.HandoffBytes > 64<<20 {
			fail("A8 has no independent perJob full growth/input/response credit: %+v", current)
		}
	}
	open()
	var got outcome
	select {
	case got = <-done:
		joined = true
	case <-ctx.Done():
		fail("aux round failed to finish: %v", ctx.Err())
	}
	if got.err != nil || len(got.result.Resolved) != count || len(got.result.Unresolved) != 0 {
		fail("incomplete A8 round: %+v %v", got.result, got.err)
	}
	mu.Lock()
	actualPeak, actualCalls := peak, calls
	mu.Unlock()
	if actualPeak != wantPeak || actualCalls != count {
		fail("real aux HTTP peak/calls=%d/%d want%d/%d", actualPeak, actualCalls, wantPeak, count)
	}
	for resource, n := range counts {
		f.assertAccepted(t, resource, n)
	}
	if twoJobs {
		seen := map[int]bool{}
		for _, fixture := range fixtures {
			requests := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(fixture.jobID)).AllX(context.Background())
			paid := 0
			for _, request := range requests {
				if request.State == "cancelled_before_dispatch" {
					continue
				}
				paid++
				if request.UsageRecordID == nil || seen[*request.UsageRecordID] || !request.UsageKnown || request.InputTokens != 7 || request.OutputTokens != 11 {
					fail("twoJob duplicate/missing usage: %s", request.Identity)
				}
				seen[*request.UsageRecordID] = true
			}
			if paid != 32 {
				fail("perJob paid requests%d want32", paid)
			}
		}
		usage := f.client.UsageRecord.Query().AllX(context.Background())
		if len(usage) != 64 {
			fail("sharedDB usage rows%d want64", len(usage))
		}
		for _, row := range usage {
			if !seen[row.ID] || row.APICalls != 1 || row.InputTokens != 7 || row.OutputTokens != 11 {
				fail("sharedDB usage not exact: %+v", row)
			}
		}
	} else {
		f.assertUsage(t, 2*count)
	}
	readCtx := context.Background()
	paidMain, paidAux, unsent := 0, 0, 0
	for _, request := range f.client.WorkRequest.Query().AllX(readCtx) {
		if request.State == "cancelled_before_dispatch" {
			unsent++
			if request.UsageKnown || request.UsageRecordID != nil || request.InputTokens != 0 || request.OutputTokens != 0 || len(request.FreezePayload) != 0 || request.FreezeBytes != 0 || len(request.ResponsePayload) != 0 || request.ResponseBytes != 0 || len(request.CorrectedTargets) != 0 || len(request.CorrectedTargetIndex) != 0 || request.CorrectedTargetBytes != 0 || request.BusinessBound != 0 || len(request.GlossaryReceipt) != 0 || len(request.PromotionMembers) != 0 {
				fail("undispatched A8 audit retained paid usage or backing: %+v", request)
			}
			continue
		}
		if request.State != "completed" || len(request.ResponsePayload) != 0 || len(request.FreezePayload) != 0 {
			fail("terminal A8 request retained unfinished/raw work: stage=%s state=%s", request.Stage, request.State)
		}
		if request.Stage == "main" {
			paidMain++
		} else if request.Stage == "alignment" {
			paidAux++
		} else {
			fail("unexpected paid stage%s", request.Stage)
		}
	}
	if paidMain != count || paidAux != count {
		fail("actual completed paid main/aux%d/%d want%d/%d", paidMain, paidAux, count, count)
	}
	t.Logf("paid_completed_main=%d aux=%d cancelled_before_dispatch_audit=%d", paidMain, paidAux, unsent)
	end := runtime.Admission.PipelineSnapshot()
	for _, currentRuntime := range runtimes {
		current := currentRuntime.Admission.PipelineSnapshot()
		if current.AuxOwners != 0 || current.AuxInputBytes != 0 || current.AuxResponseBytes != 0 || current.MainOwners[0] != 0 || current.Segments != 0 || current.CandidateBytes != 0 || current.GrowthBytes != 0 || current.PreparedBytes != 0 || current.HandoffBytes != current.ProgressLane {
			fail("A8 completion retained credits: %+v", current)
		}
		if err := verifyHandoffOwnedTerminal(current.OwnedPayloads, "freeze", "raw", "wire", "candidate", "aux"); err != nil {
			fail("aux actual backing observation: %v", err)
		}
	}
	t.Logf("requestedA=8 resources=%d distinctJobs=%v real_aux_HTTP_peak=%d aux_calls=%d members=%d barrier=%+v terminal=%+v", len(counts), twoJobs, actualPeak, actualCalls, count, atBarrier, end)
}

func logHandoffAcceptanceFailure(t *testing.T, f *rubyPipelineFixture, runtime *pipeline.ExecutionRuntime) {
	t.Helper()
	ctx := context.Background()
	states := map[string]int{}
	if rows, err := f.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(f.roundIDs[0])).Select(workitem.FieldState, workitem.FieldCandidateID, workitem.FieldResponseRequestID).All(ctx); err == nil {
		for _, row := range rows {
			states[fmt.Sprintf("%s/candidate=%v/raw=%v", row.State, row.CandidateID != "", row.ResponseRequestID != "")]++
		}
	} else {
		t.Logf("read work diagnostics: %v", err)
	}
	candidates := map[string]int{}
	if rows, err := f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0]))).Select(workcandidate.FieldState, workcandidate.FieldDtoVersion).All(ctx); err == nil {
		for _, row := range rows {
			candidates[fmt.Sprintf("%s/DTO%d", row.State, row.DtoVersion)]++
		}
	} else {
		t.Logf("read candidate diagnostics: %v", err)
	}
	requests := map[string]int{}
	if rows, err := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).Select(workrequest.FieldState, workrequest.FieldStage, workrequest.FieldPromotionState, workrequest.FieldPromotionCursor, workrequest.FieldPromotionCount).All(ctx); err == nil {
		for _, row := range rows {
			requests[fmt.Sprintf("%s/%s/%s/cursor%dof%d", row.Stage, row.State, row.PromotionState, row.PromotionCursor, row.PromotionCount)]++
		}
	} else {
		t.Logf("read request diagnostics: %v", err)
	}
	f.mu.Lock()
	httpCalls := len(f.requests)
	f.mu.Unlock()
	t.Logf("failure ledger=%+v work=%v candidates=%v requests=%v fixture_request_records=%d", runtime.Admission.PipelineSnapshot(), states, candidates, requests, httpCalls)
}

type handoffCrashBoundaryStore struct {
	*roundStore
	phase string
	once  sync.Once
}

func (s *handoffCrashBoundaryStore) boundary(phase string) {
	if s.phase != phase {
		return
	}
	s.once.Do(func() {
		fmt.Fprintln(os.Stdout, "RUBY_HANDOFF_DURABLE_BOUNDARY/"+phase)
		// This is a process barrier, outside the SQLite transaction. The
		// parent actually kills this process; no graceful Close/recovery runs.
		_, _ = bufio.NewReader(os.Stdin).ReadByte()
	})
}
func (s *handoffCrashBoundaryStore) HandoffResponse(ctx context.Context, response pipeline.MainResponse) error {
	s.boundary("raw_before")
	err := s.roundStore.HandoffResponse(ctx, response)
	if err == nil {
		s.boundary("raw_after")
	}
	return err
}
func (s *handoffCrashBoundaryStore) PromoteMember(ctx context.Context, id string, cursor int, c *pipeline.Candidate, next pipeline.WorkCursor, failure string) error {
	s.boundary("promotion_before")
	err := s.roundStore.PromoteMember(ctx, id, cursor, c, next, failure)
	if err == nil {
		s.boundary("promotion_after")
	}
	return err
}
func (s *handoffCrashBoundaryStore) Commit(ctx context.Context, c *pipeline.Candidate, result pipeline.TranslatedSegment) (pipeline.CommitOutcome, error) {
	s.boundary("commit_before")
	out, err := s.roundStore.Commit(ctx, c, result)
	if err == nil {
		s.boundary("commit_after")
	}
	return out, err
}

func loadHandoffCrashFixture(t *testing.T, path string) *rubyPipelineFixture {
	t.Helper()
	ctx := context.Background()
	client := openRubyBatchProcessClient(t, path)
	jobRow := client.Job.Query().OnlyX(ctx)
	owner := client.User.Query().OnlyX(ctx)
	f := &rubyPipelineFixture{client: client, jobID: jobRow.ID, ownerID: owner.ID, pool: backend.NewLimiterPool(), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Cleanup(f.pool.Shutdown)
	encoded, err := json.Marshal(jobRow.ExecutionConfig)
	if err != nil {
		t.Fatal(err)
	}
	f.snapshot = &service.JobExecutionSnapshot{}
	if err := json.Unmarshal(encoded, f.snapshot); err != nil {
		t.Fatal(err)
	}
	for _, jr := range client.JobResource.Query().Where(jobresource.HasJobWith(job.IDEQ(jobRow.ID))).WithResource().Order(ent.Asc(jobresource.FieldID)).AllX(ctx) {
		f.jobResources = append(f.jobResources, jr.ID)
		f.resourceIDs = append(f.resourceIDs, jr.Edges.Resource.ID)
		row := client.JobRound.Query().Where(jobround.JobResourceIDEQ(jr.ID)).OnlyX(ctx)
		f.roundIDs = append(f.roundIDs, row.ID)
	}
	f.pool.Initialize(map[int]int{f.snapshot.Rounds[0].Backend.ID: 0})
	f.initializeServices(t)
	return f
}

func TestRubyHandoffAcceptanceMainProcessKillRecovery(t *testing.T) {
	for _, phase := range []string{"raw_before", "raw_after", "promotion_before", "promotion_after", "commit_before", "commit_after"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "strict-main-handoff.db")
			barrier := &handoffHTTPBarrier{entered: make(chan struct{}, 16), release: make(chan struct{})}
			barrier.open()
			server := httptest.NewServer(http.HandlerFunc(barrier.serve))
			t.Cleanup(server.Close)
			client := openRubyBatchProcessClient(t, path)
			if err := client.Schema.Create(ctx); err != nil {
				t.Fatal(err)
			}
			seed := newRubyPipelineFixtureWithClient(t, client, server.URL, 3)
			seed.snapshot.Strategy.Ruby.Enabled = false
			seed.snapshot.RubyRetry = nil
			seed.snapshot.Rounds[0].Translate.BatchSize = 3
			seed.snapshot.Rounds[0].Translate.Retry.MaxAttempts = 1
			for _, row := range client.Segment.Query().Where(segment.ResourceIDEQ(seed.resourceIDs[0])).AllX(ctx) {
				client.Segment.UpdateOneID(row.ID).SetSourceText(fmt.Sprintf("kill-source-%d", row.SegmentIndex)).ExecX(ctx)
			}
			seed.snapshot = completeWorkerSnapshot(t, seed.snapshot)
			seed.persistSnapshot(t)
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRubyHandoffAcceptanceMainCrashHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "LINGUAFLOW_MAIN_HANDOFF_CRASH_DB="+path, "LINGUAFLOW_MAIN_HANDOFF_CRASH_PHASE="+phase)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			cleanup := func() {
				cancel()
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					waited = true
				}
			}
			defer cleanup()
			marker := false
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if scanner.Text() == "RUBY_HANDOFF_DURABLE_BOUNDARY/"+phase {
					marker = true
					break
				}
			}
			if !marker {
				cleanup()
				t.Fatalf("real worker never reached %s: scanner=%v stderr=%s", phase, scanner.Err(), stderr.String())
			}
			if err := cmd.Process.Kill(); err != nil {
				cleanup()
				t.Fatal(err)
			}
			death := cmd.Wait()
			waited = true
			if death == nil {
				t.Fatal("worker exited gracefully instead of being actually killed")
			}
			f := loadHandoffCrashFixture(t, path)
			before := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).OnlyX(context.Background())
			identity, digest := before.Identity, before.SnapshotDigest
			if phase != "raw_before" && (before.State != "completed" || before.UsageRecordID == nil || len(before.ResponsePayload) == 0 && before.PromotionCursor < before.PromotionCount) {
				t.Fatalf("kill lost a reliable raw boundary: %s cursor=%d/%d bytes=%d", before.State, before.PromotionCursor, before.PromotionCount, before.ResponseBytes)
			}
			if err := f.jobs.PrepareRecovery(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, result, err := f.execute(t, 0, nil, nil)
			if err != nil || len(result.Resolved) != 3 || len(result.Unresolved) != 0 {
				t.Fatalf("strict raw restart incomplete: %+v %v", result, err)
			}
			for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(context.Background()) {
				if row.TargetText == nil || *row.TargetText != "accepted-"+row.SourceText || row.Status != segment.StatusTranslated {
					t.Fatalf("kill/restart lost or duplicated member %d", row.ID)
				}
			}
			barrier.mu.Lock()
			calls := barrier.calls
			barrier.mu.Unlock()
			wantCalls := 1
			if phase == "raw_before" {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("HTTP replay across %s: %d want%d", phase, calls, wantCalls)
			}
			request := f.client.WorkRequest.Query().Where(workrequest.IdentityEQ(identity)).OnlyX(context.Background())
			if request.SnapshotDigest != digest || request.RetryEpoch != 0 {
				t.Fatal("restart rewrote the frozen request identity/digest/epoch")
			}
			if phase != "raw_before" {
				if request.State != "completed" || request.PromotionCursor != 3 || request.PromotionCount != 3 || len(request.FreezePayload) != 0 || len(request.ResponsePayload) != 0 {
					t.Fatalf("reliable raw restart did not finish atomic promotion: cursor=%d/%d state=%s", request.PromotionCursor, request.PromotionCount, request.State)
				}
				f.assertUsage(t, 1)
			} else {
				if request.State != "unknown" {
					t.Fatalf("unpersisted response silently became reliable: %s", request.State)
				}
				if request.UsageKnown || request.InputTokens != 0 || request.OutputTokens != 0 || request.UsageRecordID == nil {
					t.Fatal("unpersisted response fabricated known usage or lost its sent-call placeholder")
				}
				oldUsage := f.client.UsageRecord.GetX(context.Background(), *request.UsageRecordID)
				if oldUsage.APICalls != 1 || oldUsage.InputTokens != 0 || oldUsage.OutputTokens != 0 {
					t.Fatalf("unknown sent-call placeholder changed: %+v", oldUsage)
				}
				usages := f.client.UsageRecord.Query().AllX(context.Background())
				var apiCalls, input, output int
				for _, usage := range usages {
					apiCalls += usage.APICalls
					input += usage.InputTokens
					output += usage.OutputTokens
				}
				if len(usages) != 2 || apiCalls != 2 || input != 7 || output != 11 {
					t.Fatalf("unknown+retried call usage duplicated/lost known result: records=%d calls=%d input=%d output=%d", len(usages), apiCalls, input, output)
				}
			}
			if n := f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).CountX(context.Background()); n != 3 {
				t.Fatalf("recovery checkpoint count=%d want3", n)
			}
			t.Logf("phase=%s actual_process_kill=true real_HTTP_calls=%d stable_request=%s snapshot_digest=%s original_cursor=%d/%d recovered_members=3", phase, calls, identity, digest, before.PromotionCursor, before.PromotionCount)
		})
	}
}

func TestRubyHandoffAcceptanceMainCrashHelper(t *testing.T) {
	path := os.Getenv("LINGUAFLOW_MAIN_HANDOFF_CRASH_DB")
	if path == "" {
		t.Skip("actual worker subprocess helper")
	}
	f := loadHandoffCrashFixture(t, path)
	phase := os.Getenv("LINGUAFLOW_MAIN_HANDOFF_CRASH_PHASE")
	_, _, err := f.execute(t, 0, nil, func(store *roundStore) pipeline.RoundStore {
		return &handoffCrashBoundaryStore{roundStore: store, phase: phase}
	})
	t.Fatalf("worker passed the requested crash barrier %s: %v", phase, err)
}

func TestRubyHandoffAcceptanceHTTPResourceAndJobScopes(t *testing.T) {
	for _, name := range []string{"same_round_two_resources", "different_rounds_C2_C32", "two_jobs_shared_backend", "two_jobs_shared_RPM", "cold_backend_warm_backend"} {
		t.Run(name, func(t *testing.T) {
			perResource := 65
			if name == "two_jobs_shared_RPM" {
				perResource = 2
			}
			barrier := &handoffHTTPBarrier{entered: make(chan struct{}, 2*perResource), release: make(chan struct{})}
			upstream := httptest.NewServer(http.HandlerFunc(barrier.serve))
			t.Cleanup(upstream.Close)
			f := newRubyPipelineFixtureWithClient(t, newRegistryTestClient(t), upstream.URL, perResource, perResource)
			f.snapshot.Strategy.Ruby.Enabled = false
			f.snapshot.RubyRetry = nil
			f.snapshot.Rounds[0].Translate.Concurrency = 16
			f.snapshot.Rounds[0].Translate.BatchSize = 1
			f.snapshot.Rounds[0].Translate.Retry.MaxAttempts = 0
			for _, row := range f.client.Segment.Query().AllX(context.Background()) {
				u := f.client.Segment.UpdateOneID(row.ID).SetSourceText(fmt.Sprintf("scope-source-%d-%d", *row.ResourceID, row.SegmentIndex))
				if (name == "different_rounds_C2_C32" || name == "cold_backend_warm_backend") && *row.ResourceID == f.resourceIDs[1] {
					u.SetTargetText("old target").SetStatus(segment.StatusTranslated).SetQualityIssues([]qa.QualityIssue{{Code: "calque", Severity: qa.SeverityWarning, Message: "revise"}})
				}
				u.ExecX(context.Background())
			}
			wantPeak := 16
			roundIndices := []int{0, 0}
			if name == "different_rounds_C2_C32" || name == "cold_backend_warm_backend" {
				f.snapshot.Rounds[0].Translate.Concurrency = 2
				f.snapshot.Rounds = append(f.snapshot.Rounds, service.JobRoundSnapshot{Mode: "revise", Backend: f.snapshot.Rounds[0].Backend, Revise: &service.JobReviseRoundSnapshot{TemplateContent: templates.EmbeddedReviseTemplate(), BatchSize: 1, Concurrency: 32, SegmentScope: "with_issues", IssueCodes: []string{"calque"}}})
				if name == "cold_backend_warm_backend" {
					backends := f.initializeServices(t)
					secret := "acceptance-only"
					warm, err := backends.Create(context.Background(), service.CreateBackendInput{Scope: service.ScopeUser, OwnerUserID: &f.ownerID, BackendInput: service.BackendInput{Name: "warm-acceptance", Type: "openai", Secret: &secret, Options: map[string]any{"base_url": upstream.URL, "model": "fixture", "response_format": "none"}}})
					if err != nil {
						t.Fatal(err)
					}
					f.snapshot.Rounds[1].Backend = service.BackendSnapshot{ID: warm.ID, Scope: warm.Scope, Name: warm.Name, Type: warm.Type, Options: warm.Options, Credential: warm.Credential}
					f.pool.Initialize(map[int]int{f.snapshot.Rounds[0].Backend.ID: 1, warm.ID: 0})
				}
				f.client.JobRound.UpdateOneID(f.roundIDs[1]).SetRoundIndex(1).SetMode("revise").ExecX(context.Background())
				roundIndices[1] = 1
				wantPeak = 34
				if name == "cold_backend_warm_backend" {
					wantPeak = 33
				}
			}
			f.snapshot = completeWorkerSnapshot(t, f.snapshot)
			f.persistSnapshot(t)
			fixtures := []*rubyPipelineFixture{f, f}
			if name == "two_jobs_shared_backend" || name == "two_jobs_shared_RPM" {
				original := f.client.Job.GetX(context.Background(), f.jobID)
				second := f.client.Job.Create().SetProjectID(original.ProjectID).SetExecutionPlanID(original.ExecutionPlanID).SetCreatedByID(f.ownerID).SetStatus("running").SetResourceCount(1).SaveX(context.Background())
				f.client.JobResource.UpdateOneID(f.jobResources[1]).SetJobID(second.ID).ExecX(context.Background())
				f.client.JobRound.UpdateOneID(f.roundIDs[1]).SetJobID(second.ID).ExecX(context.Background())
				other := &rubyPipelineFixture{client: f.client, jobs: f.jobs, runner: f.runner, factory: f.factory, snapshot: f.snapshot, pool: f.pool, logger: f.logger, ownerID: f.ownerID, jobID: second.ID, resourceIDs: []int{f.resourceIDs[1]}, jobResources: []int{f.jobResources[1]}, roundIDs: []int{f.roundIDs[1]}}
				other.persistSnapshot(t)
				fixtures[1] = other
				wantPeak = 32
				if name == "two_jobs_shared_RPM" {
					f.pool.Refresh(f.snapshot.Rounds[0].Backend.ID, 1)
					wantPeak = 1
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			runtimes := []*pipeline.ExecutionRuntime{first, first}
			if fixtures[1] != f {
				second, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
				if err != nil {
					t.Fatal(err)
				}
				defer second.Close()
				runtimes[1] = second
			}
			type outcome struct {
				lane   int
				result pipeline.RunRoundResult
				err    error
			}
			done := make(chan outcome, 2)
			joined := 0
			cleanup := func() {
				barrier.open()
				cancel()
				for joined < 2 {
					<-done
					joined++
				}
			}
			fail := func(format string, args ...any) { cleanup(); t.Fatalf(format, args...) }
			for lane := 0; lane < 2; lane++ {
				fixture, runtime := fixtures[lane], runtimes[lane]
				eng, err := fixture.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), fixture.snapshot, engine.RuntimeResources{}, progress.Nop{})
				if err != nil {
					cancel()
					barrier.open()
					for joined < lane {
						<-done
						joined++
					}
					t.Fatal(err)
				}
				defer eng.Close()
				resource := lane
				if fixture != f {
					resource = 0
				}
				doc, store := fixture.round(t, resource)
				store.round = roundIndices[lane]
				eng.PrepareDocument(doc, nil)
				go func(lane int, doc *pipeline.Document, store *roundStore, eng *engine.Engine, runtime *pipeline.ExecutionRuntime) {
					result, err := pipeline.RunStagedRound(ctx, eng.Rounds()[roundIndices[lane]], doc, store, runtime, f.logger, progress.Nop{})
					done <- outcome{lane, result, err}
				}(lane, doc, store, eng, runtime)
			}
			defer cleanup()
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			for i := 0; i < wantPeak; i++ {
				select {
				case <-barrier.entered:
				case result := <-done:
					joined++
					fail("scope stopped before peak: %+v", result)
				case <-deadline.C:
					fail("scope HTTP peak only%d/%d first=%+v second=%+v", i, wantPeak, first.Admission.PipelineSnapshot(), runtimes[1].Admission.PipelineSnapshot())
				}
			}
			atBarrier := first.Admission.PipelineSnapshot()
			if name == "same_round_two_resources" && atBarrier.MainOwners[0] != 16 || name == "different_rounds_C2_C32" && (atBarrier.MainOwners[0] != 2 || atBarrier.MainOwners[1] != 32) || name == "two_jobs_shared_backend" && (atBarrier.MainOwners[0] != 16 || runtimes[1].Admission.PipelineSnapshot().MainOwners[0] != 16) {
				fail("C owner scope is not perJob/perRound: first=%+v second=%+v", atBarrier, runtimes[1].Admission.PipelineSnapshot())
			}
			if name == "two_jobs_shared_RPM" {
				owners := atBarrier.MainOwners[0] + runtimes[1].Admission.PipelineSnapshot().MainOwners[0]
				if owners != 1 {
					fail("shared actual RPM1 granted %d owners across two Jobs", owners)
				}
				for _, runtime := range runtimes {
					wait, err := runtime.Admission.ReadyHint(ctx, backend.RequestAdmissionIntent{Stage: backend.RequestStageMain, RoundIndex: 0, BackendID: f.snapshot.Rounds[0].Backend.ID}, nil)
					if err != nil || wait.Reason != "backend_rpm" || wait.AvailableAt.IsZero() {
						fail("current shared RPM exhausted: %+v %v", wait, err)
					}
				}
				select {
				case <-barrier.entered:
					fail("second Job bypassed current backend RPM")
				default:
				}
				// Updating current policy wakes both genuine production waiters;
				// the frozen execution snapshot is not edited or rebuilt.
				f.pool.Refresh(f.snapshot.Rounds[0].Backend.ID, 0)
			}
			if name == "cold_backend_warm_backend" {
				if atBarrier.MainOwners[0] != 1 || atBarrier.MainOwners[1] != 32 || atBarrier.PreparedBytes != 0 {
					fail("cold queued preparation blocked warm32 or retained completed build: %+v", atBarrier)
				}
				wait, err := first.Admission.ReadyHint(ctx, backend.RequestAdmissionIntent{Stage: backend.RequestStageMain, RoundIndex: 0, BackendID: f.snapshot.Rounds[0].Backend.ID}, nil)
				if err != nil || wait.Reason != "backend_rpm" {
					fail("cold policy probe: %+v %v", wait, err)
				}
				f.pool.Refresh(f.snapshot.Rounds[0].Backend.ID, 0)
			}
			barrier.open()
			for joined < 2 {
				select {
				case result := <-done:
					joined++
					if result.err != nil || len(result.result.Resolved) != perResource || len(result.result.Unresolved) != 0 {
						fail("scope incomplete lane%d: %+v %v", result.lane, result.result, result.err)
					}
				case <-ctx.Done():
					fail("scope completion timed out: %v", ctx.Err())
				}
			}
			barrier.mu.Lock()
			peak, calls := barrier.peak, barrier.calls
			barrier.mu.Unlock()
			if (name != "two_jobs_shared_RPM" && name != "cold_backend_warm_backend" && peak != wantPeak) || (name == "cold_backend_warm_backend" && (peak < 33 || peak > 34)) || calls != 2*perResource {
				fail("HTTP scopes peak/calls=%d/%d want%d/%d", peak, calls, wantPeak, 2*perResource)
			}
			for _, row := range f.client.Segment.Query().AllX(context.Background()) {
				if row.TargetText == nil || *row.TargetText != "accepted-"+row.SourceText || row.Status != segment.StatusTranslated {
					fail("scope lost member%d", row.ID)
				}
			}
			if n := f.client.JobRoundSegment.Query().CountX(context.Background()); n != 2*perResource {
				fail("scope checkpoints=%d", n)
			}
			for _, request := range f.client.WorkRequest.Query().AllX(context.Background()) {
				if len(request.FreezePayload) != 0 || len(request.ResponsePayload) != 0 || len(request.CorrectedTargets) != 0 || request.FreezeBytes != 0 || request.ResponseBytes != 0 || request.CorrectedTargetBytes != 0 {
					fail("distinct Job durable request payload retained: request%s/job%d", request.Identity, request.JobID)
				}
			}
			for _, candidate := range f.client.WorkCandidate.Query().AllX(context.Background()) {
				if len(candidate.Payload) != 0 || candidate.PayloadBytes != 0 {
					fail("distinct Job private candidate payload retained: candidate database%d/member%d", candidate.ID, candidate.WorkItemID)
				}
			}
			for _, runtime := range runtimes {
				end := runtime.Admission.PipelineSnapshot()
				if end.MainOwners[0] != 0 || end.MainOwners[1] != 0 || end.Segments != 0 || end.CandidateBytes != 0 || end.GrowthBytes != 0 || end.PreparedBytes != 0 || end.HandoffBytes != end.ProgressLane {
					fail("scope retained credit: %+v", end)
				}
			}
			t.Logf("scope=%s real_HTTP_peak=%d calls=%d committed=%d distinct_job=%v barrier=%+v", name, peak, calls, 2*perResource, fixtures[1] != f, atBarrier)
		})
	}
}

type handoffPauseBoundaryStore struct {
	*roundStore
	phase string
	once  sync.Once
	pause func(context.Context) error
	err   error
}

func (s *handoffPauseBoundaryStore) boundary(ctx context.Context, phase string) error {
	if s.phase == phase {
		s.once.Do(func() { s.err = s.pause(ctx) })
	}
	return s.err
}
func (s *handoffPauseBoundaryStore) HandoffResponse(ctx context.Context, response pipeline.MainResponse) error {
	if err := s.boundary(ctx, "raw_before"); err != nil {
		return err
	}
	if err := s.roundStore.HandoffResponse(ctx, response); err != nil {
		return err
	}
	return s.boundary(ctx, "raw_after")
}
func (s *handoffPauseBoundaryStore) PromoteMember(ctx context.Context, id string, cursor int, c *pipeline.Candidate, next pipeline.WorkCursor, failure string) error {
	if err := s.boundary(ctx, "promotion_before"); err != nil {
		return err
	}
	if err := s.roundStore.PromoteMember(ctx, id, cursor, c, next, failure); err != nil {
		return err
	}
	return s.boundary(ctx, "promotion_after")
}

func TestRubyHandoffAcceptanceHTTPPauseReliableRawAndPromotion(t *testing.T) {
	for _, phase := range []string{"raw_before", "raw_after", "promotion_before", "promotion_after"} {
		t.Run(phase, func(t *testing.T) {
			f, barrier := newHandoffHTTPFixture(t, "translate", 1, 3, 3)
			barrier.open()
			gate := pipeline.NewPauseGate()
			paused := false
			_, result, err := f.execute(t, 0, gate, func(store *roundStore) pipeline.RoundStore {
				return &handoffPauseBoundaryStore{roundStore: store, phase: phase, pause: func(ctx context.Context) error {
					_, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID)
					if err == nil {
						paused = true
						gate.Pause()
					}
					return err
				}}
			})
			if err != nil || !paused || gate.Inflight() != 0 || len(result.Resolved) != 0 {
				t.Fatalf("pause failed to drain without confirmation: %+v err=%v paused=%v draining=%d", result, err, paused, gate.Inflight())
			}
			ctx := context.Background()
			request := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).OnlyX(ctx)
			if request.State != "completed" || request.UsageRecordID == nil || request.PromotionCount != 3 {
				t.Fatalf("pause lost reliable main response: state=%s cursor%dof%d", request.State, request.PromotionCursor, request.PromotionCount)
			}
			if phase == "raw_before" || phase == "raw_after" {
				if request.PromotionCursor != 0 || len(request.ResponsePayload) == 0 {
					t.Fatal("raw pause prematurely promoted or discarded carrier")
				}
			}
			if n := f.client.JobRoundSegment.Query().CountX(ctx); n != 0 || f.client.Job.GetX(ctx, f.jobID).ProgressCompleted != 0 {
				t.Fatalf("pause confirmed early: checkpoints=%d", n)
			}
			for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(ctx) {
				if row.TargetText != nil || row.Status != segment.StatusPending {
					t.Fatalf("pause changed member%d before Commit", row.ID)
				}
			}
			if err := f.jobs.MarkJobPaused(ctx, f.jobID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
				t.Fatal(err)
			}
			_, recovered, err := f.execute(t, 0, nil, nil)
			if err != nil || len(recovered.Resolved) != 3 || len(recovered.Unresolved) != 0 {
				t.Fatalf("resume raw incomplete: %+v %v", recovered, err)
			}
			barrier.mu.Lock()
			calls := barrier.calls
			barrier.mu.Unlock()
			if calls != 1 {
				t.Fatalf("Pause/Resume replayed reliable HTTP: %d", calls)
			}
			final := f.client.WorkRequest.GetX(ctx, request.ID)
			if final.Identity != request.Identity || final.SnapshotDigest != request.SnapshotDigest || final.PromotionCursor != 3 || final.RetryEpoch != request.RetryEpoch || len(final.ResponsePayload) != 0 || len(final.FreezePayload) != 0 {
				t.Fatal("resume changed reliable identity or did not clear promoted carrier")
			}
			for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(ctx) {
				if row.TargetText == nil || *row.TargetText != "accepted-"+row.SourceText || row.Status != segment.StatusTranslated {
					t.Fatalf("resume lost member%d", row.ID)
				}
			}
			f.assertUsage(t, 1)
			t.Logf("phase=%s paused_raw_cursor=%d/%d draining=0 confirmed_at_pause=0 actual_HTTP_calls=%d resumed_members=3", phase, request.PromotionCursor, request.PromotionCount, calls)
		})
	}
}

func TestRubyHandoffAcceptanceHTTPPauseDuringGrantedMainJoinsRaw(t *testing.T) {
	f, barrier := newHandoffHTTPFixture(t, "translate", 1, 3, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gate := pipeline.NewPauseGate()
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, gate, pipeline.DefaultCandidateLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err = runtime.SetResponseLimits(1<<20, 64<<20); err != nil {
		t.Fatal(err)
	}
	eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	doc, store := f.round(t, 0)
	eng.PrepareDocument(doc, nil)
	type outcome struct {
		result pipeline.RunRoundResult
		err    error
	}
	done := make(chan outcome, 1)
	joined := false
	go func() {
		result, err := pipeline.RunStagedRound(ctx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
		done <- outcome{result, err}
	}()
	cleanup := func() {
		barrier.open()
		cancel()
		if !joined {
			<-done
			joined = true
		}
	}
	defer cleanup()
	fail := func(format string, args ...any) { cleanup(); t.Fatalf(format, args...) }
	select {
	case <-barrier.entered:
	case got := <-done:
		joined = true
		fail("main never reached HTTP: %+v", got)
	case <-ctx.Done():
		fail("main barrier: %v", ctx.Err())
	}
	atGrant := runtime.Admission.PipelineSnapshot()
	if gate.Inflight() != 1 || atGrant.MainOwners[0] != 1 || atGrant.Segments != 3 || atGrant.HandoffBytes <= atGrant.ProgressLane {
		fail("real grant has no S/J/R owner: %+v inflight%d", atGrant, gate.Inflight())
	}
	paused, err := f.jobs.PauseJob(context.Background(), f.ownerID, f.jobID)
	if err != nil || !paused.NeedsDrain {
		fail("grant Pause has no drain: %+v %v", paused, err)
	}
	gate.Pause()
	select {
	case got := <-done:
		joined = true
		fail("Pause returned before granted HTTP joined: %+v", got)
	default:
	}
	if gate.Inflight() != 1 || f.client.JobRoundSegment.Query().CountX(context.Background()) != 0 {
		fail("Pause released network/confirmed before response")
	}
	barrier.open()
	select {
	case got := <-done:
		joined = true
		if got.err != nil || len(got.result.Resolved) != 0 {
			fail("granted response confirmed before Resume: %+v %v", got.result, got.err)
		}
	case <-ctx.Done():
		fail("Pause did not drain main: %v", ctx.Err())
	}
	if gate.Inflight() != 0 {
		fail("granted HTTP remained draining%d", gate.Inflight())
	}
	request := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).OnlyX(context.Background())
	if request.State != "completed" || request.PromotionCursor != 0 || request.PromotionCount != 3 || len(request.ResponsePayload) == 0 || !request.UsageKnown {
		fail("Pause dropped reliable unpromoted raw: state%s cursor%d/%d", request.State, request.PromotionCursor, request.PromotionCount)
	}
	if err = f.jobs.MarkJobPaused(context.Background(), f.jobID); err != nil {
		fail("mark paused: %v", err)
	}
	if _, err = f.jobs.ResumeJob(context.Background(), f.ownerID, f.jobID); err != nil {
		fail("Resume: %v", err)
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Resolved) != 3 {
		fail("Resume reliable granted raw: %+v %v", result, err)
	}
	barrier.mu.Lock()
	calls := barrier.calls
	barrier.mu.Unlock()
	if calls != 1 {
		fail("granted Pause replayed HTTP%d", calls)
	}
	for _, row := range f.client.Segment.Query().AllX(context.Background()) {
		if row.TargetText == nil || *row.TargetText != "accepted-"+row.SourceText {
			fail("granted Pause lost target member%d", row.ID)
		}
	}
	f.assertUsage(t, 1)
	t.Logf("real_HTTP=1 members=3 granted_inflight=1 paused_draining=0 paused_cursor=0/3 checkpoint_before_resume=0 grant=%+v", atGrant)
}

func TestRubyHandoffAcceptanceHTTPGlossaryLargeReceiptReplay(t *testing.T) {
	f, barrier := newHandoffHTTPFixture(t, "translate", 1, 1, 1)
	large := strings.Repeat("规范", 8000)
	barrier.transform = func(content any) any {
		result := content.(map[string]any)
		translations := result["translations"].(map[string]string)
		for id := range translations {
			translations[id] = "候选"
		}
		entries := []map[string]string{{"source": "SDK", "target": "候选"}}
		for i := 0; i < 20; i++ {
			entries = append(entries, map[string]string{"source": fmt.Sprintf("term-%02d", i), "target": "term", "notes": strings.Repeat("n", 2048)})
		}
		result["glossary"] = entries
		return result
	}
	barrier.open()
	ctx := context.Background()
	project := f.client.Job.QueryProject(f.client.Job.GetX(ctx, f.jobID)).OnlyX(ctx)
	old := f.client.GlossaryEntry.Create().SetProjectID(project.ID).SetSourceKey("sdk").SetSource("SDK").SetTarget(large).SetNotes(strings.Repeat("history-note", 10000)).SaveX(ctx)
	f.snapshot.GlossaryEnabled = true
	cfg := execution.DefaultInlineTermExtraction()
	cfg.Enabled = true
	f.snapshot.Rounds[0].Translate.InlineTermExtraction = &cfg
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	var businessBound, correctedAtPause int64
	run := func(gate *pipeline.PauseGate, wrap bool) (pipeline.RunRoundResult, error) {
		runCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		runtime, err := NewExecutionRuntime(f.snapshot, f.pool, gate, pipeline.DefaultCandidateLimits())
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		g, err := service.NewDatabaseGlossary(runCtx, f.client, project)
		if err != nil {
			t.Fatal(err)
		}
		eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(runCtx, runtime), f.snapshot, engine.RuntimeResources{Glossary: g}, progress.Nop{})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()
		doc, durable := f.round(t, 0)
		eng.PrepareDocument(doc, nil)
		var store pipeline.RoundStore = durable
		if wrap {
			store = &handoffPauseBoundaryStore{roundStore: durable, phase: "promotion_before", pause: func(ctx context.Context) error {
				row := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).Select(workrequest.FieldBusinessBound, workrequest.FieldCorrectedTargetBytes).OnlyX(ctx)
				businessBound, correctedAtPause = row.BusinessBound, row.CorrectedTargetBytes
				_, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID)
				if err == nil {
					gate.Pause()
				}
				return err
			}}
		}
		return pipeline.RunStagedRound(runCtx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
	}
	gate := pipeline.NewPauseGate()
	paused, err := run(gate, true)
	if err != nil || !gate.Paused() || len(paused.Resolved) != 0 || gate.Inflight() != 0 {
		t.Fatalf("large business pause: %+v %v", paused, err)
	}
	request := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).OnlyX(ctx)
	var receipt glossary.ResponseReceipt
	if err := json.Unmarshal(request.GlossaryReceipt, &receipt); err != nil || receipt.Version != 2 || receipt.Added != 20 || receipt.Skipped != 1 || len(request.GlossaryReceipt) > 32<<10 {
		t.Fatalf("compact business receipt lost 20x2KiB notes: %+v bytes%d %v", receipt, len(request.GlossaryReceipt), err)
	}
	if businessBound != 1<<20 || correctedAtPause != int64(len(large)) {
		t.Fatalf("business lane bound=%d/corrected bytes%d want nQ/48KiB", businessBound, correctedAtPause)
	}
	if n := f.client.GlossaryEntry.Query().CountX(ctx); n != 21 {
		t.Fatalf("business transaction added%d entries want21", n)
	}
	for _, entry := range f.client.GlossaryEntry.Query().Where(glossaryentry.IDNEQ(old.ID)).AllX(ctx) {
		if len(entry.Notes) != 2048 {
			t.Fatalf("glossary notes truncated to%d", len(entry.Notes))
		}
	}
	originalReceipt := string(request.GlossaryReceipt)
	f.client.GlossaryEntry.UpdateOneID(old.ID).SetTarget("manual later").ExecX(ctx)
	if err := f.jobs.MarkJobPaused(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	result, err := run(pipeline.NewPauseGate(), false)
	if err != nil || len(result.Resolved) != 1 || len(result.Unresolved) != 0 {
		t.Fatalf("large business replay: %+v %v", result, err)
	}
	row := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(ctx)
	if row.TargetText == nil || *row.TargetText != large || row.Status != segment.StatusTranslated {
		t.Fatalf("manual edit changed original48KiB transaction result: target=%v status%s", row.TargetText, row.Status)
	}
	final := f.client.WorkRequest.GetX(ctx, request.ID)
	if string(final.GlossaryReceipt) != originalReceipt || len(final.CorrectedTargets) != 0 || len(final.ResponsePayload) != 0 || final.PromotionCursor != 1 {
		t.Fatal("replay changed receipt or retained business/raw body")
	}
	if f.client.GlossaryEntry.GetX(ctx, old.ID).Target != "manual later" || f.client.GlossaryEntry.Query().CountX(ctx) != 21 {
		t.Fatal("replay rewrote manual glossary or duplicated additions")
	}
	barrier.mu.Lock()
	calls := barrier.calls
	barrier.mu.Unlock()
	if calls != 1 {
		t.Fatalf("business replay repeated HTTP%d", calls)
	}
	f.assertUsage(t, 1)
	t.Logf("real_HTTP_calls=%d Added=20 notes_bytes_each=2048 corrected_target_bytes=%d receipt_bytes=%d Bmax=%d terminal_B=%d manual_edit_replay_preserves_original=true", calls, len(large), len(final.GlossaryReceipt), businessBound, final.BusinessBound)
}

type handoffBlockedDurableStore struct {
	*roundStore
	phase      string
	rawSaved   chan<- string
	entered    chan<- struct{}
	release    <-chan struct{}
	returned   chan<- handoffRawReturn
	promotions atomic.Int64
}

type handoffRawReturn struct {
	id  string
	err error
}

func (s *handoffBlockedDurableStore) HandoffResponse(ctx context.Context, response pipeline.MainResponse) error {
	err := s.roundStore.HandoffResponse(ctx, response)
	if s.returned != nil {
		s.returned <- handoffRawReturn{id: response.ID, err: err}
	}
	if err != nil {
		return err
	}
	s.rawSaved <- response.ID
	return nil
}
func (s *handoffBlockedDurableStore) wait(ctx context.Context, phase string) error {
	if s.phase != phase {
		return nil
	}
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *handoffBlockedDurableStore) PromoteMember(ctx context.Context, id string, cursor int, c *pipeline.Candidate, next pipeline.WorkCursor, failure string) error {
	s.promotions.Add(1)
	if err := s.wait(ctx, "promotion"); err != nil {
		return err
	}
	return s.roundStore.PromoteMember(ctx, id, cursor, c, next, failure)
}
func (s *handoffBlockedDurableStore) Commit(ctx context.Context, c *pipeline.Candidate, result pipeline.TranslatedSegment) (pipeline.CommitOutcome, error) {
	if err := s.wait(ctx, "commit"); err != nil {
		return "", err
	}
	return s.roundStore.Commit(ctx, c, result)
}
func TestRubyHandoffAcceptanceHTTPSimultaneousRawDurableBeforeConsumers(t *testing.T) {
	for _, tc := range []struct {
		name, phase string
		fullJ       bool
	}{{"promotion", "promotion", false}, {"commit", "commit", false}, {"promotion_full_J", "promotion", true}, {"commit_full_J", "commit", true}} {
		t.Run(tc.name, func(t *testing.T) {
			phase := tc.phase
			const count = 32
			f, httpBarrier := newHandoffHTTPFixture(t, "translate", 32, count, 1)
			if tc.fullJ {
				httpBarrier.transform = func(content any) any {
					body := content.(map[string]any)
					// Unknown model fields still belong to normalized raw R; the
					// transport envelope has enough measured headroom to remain <=R.
					body["padding"] = strings.Repeat("x", (1<<20)-1024)
					return body
				}
			}
			// A 32-response wave near 1 MiB exercises durable handoff rather
			// than latency; allow bounded CPU headroom for race and coverage.
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			limits := pipeline.DefaultCandidateLimits()
			// S reserves one progress member; this wave fills ordinary S-1.
			limits.Segments = count + 1
			runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, limits)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			j := int64(64 << 20)
			if tc.fullJ {
				j = 36 << 20
				if err := runtime.SetResponseLimits(1<<20, j); err != nil {
					t.Fatal(err)
				}
			}
			eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			doc, durable := f.round(t, 0)
			eng.PrepareDocument(doc, nil)
			rawSaved, consumerEntered := make(chan string, count), make(chan struct{}, count)
			release := make(chan struct{})
			var releaseOnce sync.Once
			open := func() { releaseOnce.Do(func() { close(release) }) }
			returned := make(chan handoffRawReturn, count)
			store := &handoffBlockedDurableStore{roundStore: durable, phase: phase, rawSaved: rawSaved, entered: consumerEntered, release: release, returned: returned}
			type outcome struct {
				result pipeline.RunRoundResult
				err    error
			}
			done := make(chan outcome, 1)
			joined := false
			go func() {
				result, err := pipeline.RunStagedRound(ctx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
				done <- outcome{result, err}
			}()
			cleanup := func() {
				open()
				httpBarrier.open()
				cancel()
				if !joined {
					<-done
					joined = true
				}
			}
			defer cleanup()
			fail := func(format string, args ...any) {
				httpBarrier.mu.Lock()
				calls, active, peak := httpBarrier.calls, httpBarrier.active, httpBarrier.peak
				members := append([]int(nil), httpBarrier.members...)
				httpBarrier.mu.Unlock()
				t.Logf("pre-cleanup real_HTTP calls=%d active=%d peak=%d request_members=%v ledger=%+v", calls, active, peak, members, runtime.Admission.PipelineSnapshot())
				if ctx.Err() != nil {
					stack := make([]byte, 512<<10)
					t.Logf("pre-cleanup goroutine stack:\n%s", stack[:goRuntime.Stack(stack, true)])
				}
				cleanup()
				for len(returned) > 0 {
					ret := <-returned
					t.Logf("HandoffResponse returned id=%s err=%v", ret.id, ret.err)
				}
				logHandoffAcceptanceFailure(t, f, runtime)
				t.Fatalf(format, args...)
			}
			for i := 0; i < count; i++ {
				select {
				case <-httpBarrier.entered:
				case got := <-done:
					joined = true
					fail("simultaneous round stopped before HTTP %d: %+v %v", i, got.result, got.err)
				case <-ctx.Done():
					fail("only%d/%d real HTTP at full ordinary S-1", i, count)
				}
			}
			atHTTP := runtime.Admission.PipelineSnapshot()
			if atHTTP.Segments != count || atHTTP.MainOwners[0] != count {
				fail("full ordinary S-1/C HTTP wave: %+v", atHTTP)
			}
			if tc.fullJ && j-atHTTP.HandoffBytes >= (1<<20)+(32<<10) {
				fail("HTTP first wave did not fill J ordinary handoff: free%d accounting%+v", j-atHTTP.HandoffBytes, atHTTP)
			}
			httpBarrier.open()
			seen := map[string]bool{}
			diagnostic := time.NewTimer(119 * time.Second)
			defer diagnostic.Stop()
			for len(seen) < count {
				select {
				case <-diagnostic.C:
					stack := make([]byte, 512<<10)
					t.Logf("pre-deadline raw_count=%d promotion_starts=%d ctx=%v stack:\n%s", len(seen), store.promotions.Load(), ctx.Err(), stack[:goRuntime.Stack(stack, true)])
				case id := <-rawSaved:
					if seen[id] {
						fail("durable raw saved twice %s", id)
					}
					seen[id] = true
				case ret := <-returned:
					if ret.err != nil {
						fail("durable raw acknowledgement id%s err%v", ret.id, ret.err)
					}
				case got := <-done:
					joined = true
					fail("round stopped before all raw durable: %+v %v", got.result, got.err)
				case <-ctx.Done():
					fail("only%d/%d reliable raw while %s blocked", len(seen), count, phase)
				}
			}
			select {
			case <-consumerEntered:
			case <-ctx.Done():
				fail("%s consumer never reached barrier", phase)
			}
			readCtx := context.Background()
			requests := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).AllX(readCtx)
			if len(requests) != count {
				fail("durable requests%d want32", len(requests))
			}
			for _, row := range requests {
				if row.State != "completed" || row.UsageRecordID == nil || !row.UsageKnown {
					fail("full-wave raw lost reliable usage: %s %s", row.Identity, row.State)
				}
				if phase == "promotion" && (row.PromotionCursor != 0 || len(row.ResponsePayload) == 0) {
					fail("blocked promotion lost raw carrier %s", row.Identity)
				}
			}
			if f.client.JobRoundSegment.Query().CountX(readCtx) != 0 {
				fail("blocked %s confirmed work", phase)
			}
			atRaw := runtime.Admission.PipelineSnapshot()
			if atRaw.Segments > count || atRaw.CandidateBytes+atRaw.GrowthBytes > 16<<20 || atRaw.HandoffBytes > j || atRaw.MainOwners[0] > 32 {
				fail("all reliable raw exceeded joint bounds: %+v", atRaw)
			}
			if tc.fullJ && phase == "promotion" {
				var rawTotal int64
				for _, row := range requests {
					if row.ResponseBytes < (1<<20)-1024 || row.ResponseBytes > 1<<20 {
						fail("full J raw budget row %s bytes%d", row.Identity, row.ResponseBytes)
					}
					rawTotal += row.ResponseBytes
				}
				if rawTotal < int64(count)*((1<<20)-1024) || j-atRaw.HandoffBytes >= (1<<20)+(32<<10) {
					fail("near-R32 did not fill J ordinary handoff: raw%d free%d accounting%+v", rawTotal, j-atRaw.HandoffBytes, atRaw)
				}
			}
			f.assertUsage(t, count)
			open()
			var got outcome
			select {
			case got = <-done:
				joined = true
			case <-ctx.Done():
				fail("simultaneous resume did not join: %v", ctx.Err())
			}
			if got.err != nil || len(got.result.Resolved) != count || len(got.result.Unresolved) != 0 {
				fail("simultaneous completion: %+v %v", got.result, got.err)
			}
			httpBarrier.mu.Lock()
			calls, peak := httpBarrier.calls, httpBarrier.peak
			httpBarrier.mu.Unlock()
			if calls != count || peak != count {
				fail("durable consumer barrier replayed HTTP %d/peak%d", calls, peak)
			}
			if n := f.client.JobRoundSegment.Query().CountX(readCtx); n != count {
				fail("simultaneous checkpoints%d", n)
			}
			for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(readCtx) {
				if row.TargetText == nil || *row.TargetText != "accepted-"+row.SourceText {
					fail("simultaneous lost member%d", row.ID)
				}
			}
			end := runtime.Admission.PipelineSnapshot()
			if end.MainOwners[0] != 0 || end.Segments != 0 || end.CandidateBytes != 0 || end.GrowthBytes != 0 || end.HandoffBytes != end.ProgressLane {
				fail("simultaneous terminal credit: %+v", end)
			}
			if err := verifyHandoffOwnedTerminal(end.OwnedPayloads, "freeze", "raw", "wire", "candidate"); err != nil {
				fail("simultaneous named owned buffer lifetime: %v", err)
			}
			t.Logf("blocked=%s J=%d near_R=%v real_HTTP_peak=%d all_raw_durable=%d confirmed_at_barrier=0 at_HTTP=%+v at_raw=%+v terminal=%+v", phase, j, tc.fullJ, peak, len(seen), atHTTP, atRaw, end)
		})
	}
}

func TestRubyHandoffAcceptanceHTTPResponseFitsRButOneMemberExceedsQ(t *testing.T) {
	f, barrier := newHandoffHTTPFixture(t, "translate", 1, 2, 2)
	var rejectedID string
	barrier.transform = func(content any) any {
		result := content.(map[string]any)
		translations := result["translations"].(map[string]string)
		ids := make([]string, 0, len(translations))
		for id := range translations {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		rejectedID = ids[0]
		translations[rejectedID] = strings.Repeat("x", 12<<10)
		return result
	}
	barrier.open()
	limits := pipeline.DefaultCandidateLimits()
	limits.ItemBytes = 8 << 10
	limits.Bytes = 16 << 10
	run := func() (pipeline.RunRoundResult, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, limits)
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		if err := runtime.SetResponseLimits(1<<20, 64<<20); err != nil {
			t.Fatal(err)
		}
		eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()
		doc, store := f.round(t, 0)
		eng.PrepareDocument(doc, nil)
		return pipeline.RunStagedRound(ctx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
	}
	result, err := run()
	if err != nil || len(result.Resolved) != 1 || len(result.Unresolved) != 1 {
		for _, row := range f.client.WorkItem.Query().Where(workitem.JobIDEQ(f.jobID)).AllX(context.Background()) {
			t.Logf("member=%d state=%s last_error=%s", row.SegmentID, row.State, row.LastError)
		}
		for _, row := range f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).Select(workrequest.FieldPromotionCursor, workrequest.FieldPromotionCount, workrequest.FieldState).AllX(context.Background()) {
			t.Logf("raw state%s cursor%d/%d", row.State, row.PromotionCursor, row.PromotionCount)
		}
		t.Fatalf("one oversized member contaminated siblings: %+v %v", result, err)
	}
	ctx := context.Background()
	for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(ctx) {
		if fmt.Sprint(row.ID) == rejectedID {
			if row.TargetText != nil || row.Status != segment.StatusPending {
				t.Fatal("Q-invalid target committed")
			}
		} else if row.TargetText == nil || *row.TargetText != "accepted-"+row.SourceText || row.Status != segment.StatusTranslated {
			t.Fatalf("legal sibling%d lost", row.ID)
		}
	}
	request := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).OnlyX(ctx)
	if request.State != "completed" || request.PromotionCursor != 2 || request.PromotionCount != 2 || len(request.ResponsePayload) != 0 || len(request.FreezePayload) != 0 {
		t.Fatalf("single-member failure lost raw prefix/cleanup: state%s cursor%d/%d", request.State, request.PromotionCursor, request.PromotionCount)
	}
	if n := f.client.WorkCandidate.Query().CountX(ctx); n != 1 {
		t.Fatalf("Q-invalid candidate became durable or legal sibling lost: %d", n)
	}
	if n := f.client.JobRoundSegment.Query().CountX(ctx); n != 1 {
		t.Fatalf("one-member acceptance checkpoint%d", n)
	}
	if err := f.jobs.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := run()
	if err != nil || len(replayed.Resolved) != 1 || len(replayed.Unresolved) != 1 {
		t.Fatalf("failure cursor not stable on replay: %+v %v", replayed, err)
	}
	barrier.mu.Lock()
	calls := barrier.calls
	barrier.mu.Unlock()
	if calls != 1 {
		t.Fatalf("deterministic Q rejection replayed provider %d", calls)
	}
	f.assertUsage(t, 1)
	t.Logf("R=%d Q=%d oversized_target=%d real_HTTP_calls=%d legal_siblings_committed=1 rejected_members=1 replay_keeps_cursor=true", 1<<20, limits.ItemBytes, 12<<10, calls)
}

func TestRubyHandoffAcceptanceRunnerFuturePinsLoweredWindow(t *testing.T) {
	for _, business := range []bool{false, true} {
		name := "plain_J64"
		if business {
			name = "saved_business_J3_25"
		}
		t.Run(name, func(t *testing.T) { runHandoffRunnerFuturePins(t, business) })
	}
}

func TestRubyHandoffAcceptanceRunnerFixedFuture16PreservesPaidBatch(t *testing.T) {
	for _, limit := range []int{17, 32} {
		t.Run(fmt.Sprintf("S%d", limit), func(t *testing.T) { runHandoffRunnerFixedFuture16(t, limit) })
	}
}

func runHandoffRunnerFixedFuture16(t *testing.T, limit int) {
	ctx := context.Background()
	f, barrier := newHandoffHTTPFixture(t, "translate", 4, 16, 16)
	barrier.open()
	translation := f.snapshot.Rounds[0]
	f.snapshot.Rounds = []service.JobRoundSnapshot{{Mode: "revise", Backend: translation.Backend, Revise: &service.JobReviseRoundSnapshot{TemplateContent: templates.EmbeddedReviseTemplate(), Concurrency: 4, BatchSize: 16, SegmentScope: "with_issues", IssueCodes: []string{"calque"}}}, translation}
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	futureID := f.roundIDs[0]
	f.client.JobRound.UpdateOneID(futureID).SetRoundIndex(1).ExecX(ctx)
	for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(ctx) {
		f.client.Segment.UpdateOneID(row.ID).SetTargetText("accepted-" + row.SourceText).SetStatus(segment.StatusTranslated).SetQualityIssues([]qa.QualityIssue{{Code: "calque", Severity: qa.SeverityWarning, Message: "noop early revision"}}).ExecX(ctx)
	}
	gate := pipeline.NewPauseGate()
	r, err := NewExecutionRuntime(f.snapshot, f.pool, gate, pipeline.DefaultCandidateLimits())
	if err != nil {
		t.Fatal(err)
	}
	eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, r), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	doc, base := f.round(t, 0)
	base.scope.RoundID = futureID
	base.round = 1
	for i := range doc.Segments {
		doc.Segments[i].Translate = true
	}
	eng.PrepareDocument(doc, nil)
	store := &handoffPauseBoundaryStore{roundStore: base, phase: "raw_after", pause: func(saveCtx context.Context) error {
		_, e := f.jobs.PauseJob(saveCtx, f.ownerID, f.jobID)
		if e == nil {
			gate.Pause()
		}
		return e
	}}
	seedCtx, seedCancel := context.WithTimeout(ctx, 15*time.Second)
	_, err = pipeline.RunStagedRound(seedCtx, eng.Rounds()[1], doc, store, r, f.logger, progress.Nop{})
	seedCancel()
	eng.Close()
	r.Close()
	if err != nil || !gate.Paused() {
		t.Fatalf("future16 seed: %v", err)
	}
	if err = f.jobs.MarkJobPaused(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	f.roundIDs[0] = createJobRoundRow(t, f.client, f.jobID, f.jobResources[0], 0, "revise")
	raw := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID), workrequest.StateEQ("completed")).OnlyX(ctx)
	if raw.PromotionCursor != 0 || raw.PromotionCount != 16 || len(raw.ResponsePayload) == 0 {
		t.Fatal("fixture did not own full reliable future16")
	}
	freezeDigest := raw.SnapshotDigest
	f.runner.pipeCfg.Candidates = pipeline.CandidateLimits{Segments: limit, Bytes: 16 << 20, ItemBytes: 1 << 20}
	f.runner.pipeCfg.MaxInflightResources = 1
	run := func() error {
		runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return f.runner.processJob(runCtx, f.jobID)
	}
	if err = run(); err != nil {
		t.Fatal(err)
	}
	barrier.mu.Lock()
	calls := barrier.calls
	members := append([]int(nil), barrier.members...)
	barrier.mu.Unlock()
	wantCalls, wantNewBatch := 2, 16
	if limit == 17 {
		wantCalls, wantNewBatch = 17, 1
	}
	if calls != wantCalls || len(members) != wantCalls || members[0] != 16 {
		t.Fatalf("S%d replayed or changed paid raw: HTTP=%d members=%v", limit, calls, members)
	}
	for _, n := range members[1:] {
		if n != wantNewBatch {
			t.Fatalf("S%d new prefix must have %d members: %v", limit, wantNewBatch, members)
		}
	}
	seen := make(map[int]int)
	for _, request := range f.client.WorkRequest.Query().Where(workrequest.JobRoundIDEQ(f.roundIDs[0]), workrequest.StateEQ("completed")).AllX(ctx) {
		for _, id := range request.SegmentIds {
			seen[id]++
		}
	}
	for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(ctx) {
		if seen[row.ID] != 1 {
			t.Fatalf("new revision member%d dispatched %d times", row.ID, seen[row.ID])
		}
	}
	if n := f.client.JobRoundSegment.Query().CountX(ctx); n != 32 || f.client.Job.GetX(ctx, f.jobID).Status != service.JobStatusCompleted {
		t.Fatalf("S%d incomplete checkpoint=%d status=%s", limit, n, f.client.Job.GetX(ctx, f.jobID).Status)
	}
	if last := f.client.WorkRequest.GetX(ctx, raw.ID); last.PromotionCount != 16 || last.PromotionCursor != 16 || last.Identity != raw.Identity || last.SnapshotDigest != freezeDigest || len(last.ResponsePayload) != 0 {
		t.Fatal("future raw identity changed or was not consumed exactly once")
	}
	f.assertUsage(t, wantCalls)
	t.Logf("S%d fixed future16: new batch%d, new members unique, original paid n16 unchanged, HTTP/usage%d, checkpoints32", limit, wantNewBatch, calls)
}

func TestRubyHandoffAcceptanceRunnerOneResourceSlotDrainsOtherRaw16(t *testing.T) {
	ctx := context.Background()
	barrier := &handoffHTTPBarrier{entered: make(chan struct{}, 4), release: make(chan struct{})}
	barrier.open()
	server := httptest.NewServer(http.HandlerFunc(barrier.serve))
	t.Cleanup(server.Close)
	f := newRubyPipelineFixtureWithClient(t, newRegistryTestClient(t), server.URL, 16, 16)
	f.snapshot.Strategy.Ruby.Enabled = false
	f.snapshot.RubyRetry = nil
	f.snapshot.Rounds[0].Translate.Concurrency = 4
	f.snapshot.Rounds[0].Translate.BatchSize = 16
	f.snapshot.Rounds[0].Translate.Retry.MaxAttempts = 0
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	gate := pipeline.NewPauseGate()
	_, _, err := f.execute(t, 1, gate, func(store *roundStore) pipeline.RoundStore {
		return &handoffPauseBoundaryStore{roundStore: store, phase: "raw_after", pause: func(saveCtx context.Context) error {
			_, e := f.jobs.PauseJob(saveCtx, f.ownerID, f.jobID)
			if e == nil {
				gate.Pause()
			}
			return e
		}}
	})
	if err != nil || !gate.Paused() {
		t.Fatalf("seed other current-scope raw16: %v", err)
	}
	if err = f.jobs.MarkJobPaused(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	f.runner.pipeCfg.Candidates = pipeline.CandidateLimits{Segments: 17, Bytes: 16 << 20, ItemBytes: 1 << 20}
	f.runner.pipeCfg.MaxInflightResources = 1
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err = f.runner.processJob(runCtx, f.jobID); err != nil {
		t.Fatal(err)
	}
	barrier.mu.Lock()
	calls := barrier.calls
	members := append([]int(nil), barrier.members...)
	barrier.mu.Unlock()
	if calls != 2 || len(members) != 2 || members[0] != 16 || members[1] != 16 {
		t.Fatalf("yield/drain lost original batch or replayed raw: %d %v", calls, members)
	}
	if job := f.client.Job.GetX(ctx, f.jobID); job.Status != service.JobStatusCompleted {
		cause := ""
		if job.ErrorMessage != nil {
			cause = *job.ErrorMessage
		}
		t.Fatalf("legal queued consumer misclassified: %s %s", job.Status, cause)
	}
	if n := f.client.JobRoundSegment.Query().CountX(ctx); n != 32 {
		t.Fatalf("one-slot drain checkpoints=%d", n)
	}
	f.assertUsage(t, 2)
	t.Log("max_resources1 first resource yields at S17/fixed raw16; second current-scope raw drains, then full original16 dispatches once")
}

type handoffRunnerFuturePinsOptions struct {
	segments     int
	emptyEarlier bool
	canProgress  bool
}

func runHandoffRunnerFuturePins(t *testing.T, business bool, options ...handoffRunnerFuturePinsOptions) {
	ctx := context.Background()
	option, runTimeout := handoffRunnerFuturePinsOptions{segments: 1}, 30*time.Second
	if len(options) > 0 {
		option, runTimeout = options[0], 5*time.Second
	}
	barrier := &handoffHTTPBarrier{entered: make(chan struct{}, 32), release: make(chan struct{})}
	barrier.open()
	server := httptest.NewServer(http.HandlerFunc(barrier.serve))
	t.Cleanup(server.Close)
	f := newRubyPipelineFixtureWithClient(t, newRegistryTestClient(t), server.URL, 2, 2)
	translation := f.snapshot.Rounds[0]
	translation.Translate.BatchSize = 2
	// Both reliable future responses retain a same-round owner across Pause.
	// C2 makes these two historical owners legal; early singleton remains C1.
	translation.Translate.Concurrency = 2
	translation.Translate.Retry.MaxAttempts = 0
	if business {
		cfg := execution.DefaultInlineTermExtraction()
		cfg.Enabled = true
		translation.Translate.InlineTermExtraction = &cfg
		f.snapshot.GlossaryEnabled = true
	}
	f.snapshot.Strategy.Ruby.Enabled = false
	f.snapshot.RubyRetry = nil
	f.snapshot.Rounds = []service.JobRoundSnapshot{{Mode: "revise", Backend: translation.Backend, Revise: &service.JobReviseRoundSnapshot{TemplateContent: templates.EmbeddedReviseTemplate(), Concurrency: 1, BatchSize: 2, SegmentScope: "with_issues", IssueCodes: []string{"calque"}}}, translation}
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	futureIDs := make([]int, 2)
	for resource := 0; resource < 2; resource++ {
		// Produce historical late inventory before earlier rounds are scheduled.
		// This avoids fabricating an active recovery episode during ordinary seed.
		f.client.JobRound.UpdateOneID(f.roundIDs[resource]).SetRoundIndex(1).SetMode("translate").ExecX(ctx)
		futureIDs[resource] = f.roundIDs[resource]
	}
	for _, row := range f.client.Segment.Query().AllX(ctx) {
		source := fmt.Sprintf("future-source-%d", row.ID)
		var issues []qa.QualityIssue
		if !option.emptyEarlier {
			issues = []qa.QualityIssue{{Code: "calque", Severity: qa.SeverityWarning, Message: "noop revision"}}
		}
		f.client.Segment.UpdateOneID(row.ID).SetSourceText(source).SetTargetText("accepted-" + source).SetStatus(segment.StatusTranslated).SetQualityIssues(issues).ExecX(ctx)
	}
	project := f.client.Job.QueryProject(f.client.Job.GetX(ctx, f.jobID)).OnlyX(ctx)
	large := strings.Repeat("规范", 130000)
	if business {
		f.client.GlossaryEntry.Create().SetProjectID(project.ID).SetSourceKey("sdk").SetSource("SDK").SetTarget(large).SaveX(ctx)
		barrier.transform = func(content any) any {
			result := content.(map[string]any)
			if translations, ok := result["translations"].(map[string]string); ok {
				for id := range translations {
					translations[id] = "候选"
				}
				result["glossary"] = []map[string]string{{"source": "SDK", "target": "候选"}}
			}
			return result
		}
	}
	for resource := 0; resource < 2; resource++ {
		gate := pipeline.NewPauseGate()
		runtime, err := NewExecutionRuntime(f.snapshot, f.pool, gate, pipeline.DefaultCandidateLimits())
		if err != nil {
			t.Fatal(err)
		}
		if err := restoreHandoffAcceptanceInventory(ctx, f, runtime); err != nil {
			runtime.Close()
			t.Fatalf("restore future seed inventory resource%d: %v", resource, err)
		}
		resources := engine.RuntimeResources{}
		if business {
			g, err := service.NewDatabaseGlossary(ctx, f.client, project)
			if err != nil {
				runtime.Close()
				t.Fatal(err)
			}
			resources.Glossary = g
		}
		eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, resources, progress.Nop{})
		if err != nil {
			runtime.Close()
			t.Fatal(err)
		}
		doc, base := f.round(t, resource)
		base.scope.RoundID = futureIDs[resource]
		base.round = 1
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
		_, err = pipeline.RunStagedRound(seedCtx, eng.Rounds()[1], doc, store, runtime, f.logger, progress.Nop{})
		seedCancel()
		eng.Close()
		runtime.Close()
		if err != nil || !gate.Paused() {
			t.Fatalf("future raw seed resource%d: %v", resource, err)
		}
		if err := f.jobs.MarkJobPaused(ctx, f.jobID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
			t.Fatal(err)
		}
	}
	for resource := 0; resource < 2; resource++ {
		f.roundIDs[resource] = createJobRoundRow(t, f.client, f.jobID, f.jobResources[resource], 0, "revise")
	}
	if n := f.client.WorkRequest.Query().Where(workrequest.PromotionCursorEQ(1), workrequest.PromotionCountEQ(2), workrequest.StateEQ("completed")).CountX(ctx); n != 2 {
		t.Fatalf("future fixture needs2 reliable raw prefix1/2, got%d", n)
	}
	paidOriginal := f.client.WorkRequest.Query().Where(workrequest.StateEQ("completed")).AllX(ctx)
	f.runner.pipeCfg.Candidates = pipeline.CandidateLimits{Segments: option.segments, Bytes: 16 << 20, ItemBytes: 1 << 20}
	f.runner.pipeCfg.MaxInflightResources = 1
	f.runner.pipeCfg.MaxResponseBytes = 1 << 20
	f.runner.pipeCfg.ResponseHandoffBytes = 64 << 20
	barrier.mu.Lock()
	before := barrier.calls
	barrier.mu.Unlock()
	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	err := f.runner.processJob(runCtx, f.jobID)
	cancel()
	barrier.mu.Lock()
	failedCalls := barrier.calls
	barrier.mu.Unlock()
	if option.emptyEarlier {
		if err != nil || failedCalls != before || f.client.Job.GetX(ctx, f.jobID).Status != service.JobStatusCompleted || f.client.JobRoundSegment.Query().CountX(ctx) != 4 {
			t.Fatalf("empty early rounds did not reach consumable future raw: HTTP%d->%d err=%v", before, failedCalls, err)
		}
		f.assertUsage(t, before)
		return
	}
	if option.canProgress {
		barrier.mu.Lock()
		members := append([]int(nil), barrier.members...)
		barrier.mu.Unlock()
		if err != nil || f.client.Job.GetX(ctx, f.jobID).Status != service.JobStatusCompleted || f.client.JobRoundSegment.Query().CountX(ctx) != 8 {
			t.Fatalf("fixed future inventory left a legal singleton but work did not finish: HTTP%d->%d err=%v", before, failedCalls, err)
		}
		newMembers := 0
		for _, n := range members[before:] {
			if n < 1 || n > 2 {
				t.Fatalf("new request exceeds source batch: %v", members)
			}
			newMembers += n
		}
		if len(members) <= before || members[before] != 1 || newMembers != 4 {
			t.Fatalf("fixed-prefix singleton or original work lost: %v", members)
		}
		for _, original := range paidOriginal {
			recovered := f.client.WorkRequest.GetX(ctx, original.ID)
			if recovered.Identity != original.Identity || recovered.PromotionCount != 2 || recovered.PromotionCursor != 2 || recovered.SnapshotDigest != original.SnapshotDigest {
				t.Fatalf("fixed prefix changed a paid original request: %+v", recovered)
			}
		}
		seen := make(map[int]int)
		for _, request := range f.client.WorkRequest.Query().Where(workrequest.StateEQ("completed"), workrequest.JobRoundIDIn(f.roundIDs...)).AllX(ctx) {
			for _, id := range request.SegmentIds {
				seen[id]++
			}
		}
		for _, row := range f.client.Segment.Query().AllX(ctx) {
			if seen[row.ID] != 1 {
				t.Fatalf("new source member was missed or sent twice: segment%d count%d", row.ID, seen[row.ID])
			}
		}
		f.assertUsage(t, failedCalls)
		t.Logf("S%d/fixed4 made a singleton prefix; original paid n2 unchanged; HTTP=%d new_members=%d checkpoints=8", option.segments, failedCalls, newMembers)
		return
	}
	if failedCalls != before {
		t.Fatalf("blocked future n2 dispatched under S%d: HTTP %d -> %d err=%v", option.segments, before, failedCalls, err)
	}
	if f.client.WorkRequest.Query().Where(workrequest.PromotionCursorEQ(1), workrequest.PromotionCountEQ(2), workrequest.StateEQ("completed")).CountX(ctx) != 2 {
		t.Fatal("capacity failure deleted reliable future raw")
	}
	if f.client.Job.GetX(ctx, f.jobID).Status != service.JobStatusFailed {
		t.Fatalf("capacity failure did not aggregate failed: %v", err)
	}
	// Changing policy alone never revives failed work. Explicit Retry uses the
	// original two-member batches and replays both reliable future responses.
	f.runner.pipeCfg.Candidates = pipeline.CandidateLimits{Segments: 8, Bytes: 16 << 20, ItemBytes: 1 << 20}
	if f.client.Job.GetX(ctx, f.jobID).Status != service.JobStatusFailed {
		t.Fatal("configuration change revived failed work")
	}
	if _, err = f.jobs.RetryJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	barrier.transform = nil
	runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
	err = f.runner.processJob(runCtx, f.jobID)
	cancel()
	if err != nil {
		logHandoffFutureFailure(t, f, barrier)
		t.Fatal(err)
	}
	if n := f.client.JobRoundSegment.Query().CountX(ctx); n != 8 {
		t.Fatalf("Retry lost checkpoints: %d", n)
	}
	barrier.mu.Lock()
	calls := barrier.calls
	members := append([]int(nil), barrier.members...)
	barrier.mu.Unlock()
	if calls != before+2 {
		t.Fatalf("Retry split original n2 or replayed reliable raw: HTTP %d want %d", calls, before+2)
	}
	for _, n := range members[before:] {
		if n != 2 {
			t.Fatalf("Retry changed original batch: %v", members)
		}
	}
	terminal, err := workstate.NewStore(f.client).LoadRuntimeState(ctx, f.jobID)
	if err != nil || terminal.ProgressID != "" || terminal.ResponseID != "" || terminal.BusinessOwner != "" {
		t.Fatalf("terminal lifecycle leaked: %+v %v", terminal, err)
	}
	f.assertUsage(t, calls)
	t.Logf("S%d n2 failed HTTP0 preserving raw; S8 explicit Retry original n2; actual_HTTP=%d", option.segments, calls)
}

// Rebuild the same header-only global inventory as the production Job runner
// before a resource is admitted. No saved candidate/response body is decoded.
func restoreHandoffAcceptanceInventory(ctx context.Context, f *rubyPipelineFixture, runtime *pipeline.ExecutionRuntime) error {
	store := workstate.NewStore(f.client)
	if err := runtime.ConfigureEpisodeStore(ctx, f.jobID, store); err != nil {
		return err
	}
	indices := map[int]int{}
	for _, row := range f.client.JobRound.Query().AllX(ctx) {
		indices[row.ID] = row.RoundIndex
	}
	for after := 0; ; {
		headers, next, err := store.LoadResponseHeaders(ctx, f.jobID, after, 16)
		if err != nil {
			return err
		}
		if len(headers) == 0 {
			break
		}
		for _, h := range headers {
			if err := runtime.Admission.RestoreResponseOwner(h.RequestID, indices[h.Scope.RoundID], h.Scope.ResourceID, h.FreezeBytes, h.ResponseBytes, h.BusinessBound); err != nil {
				return err
			}
			if err := runtime.Admission.RestoreResponseBusiness(h.RequestID, h.CorrectedBytes); err != nil {
				return err
			}
			if err := runtime.Window.RestoreRaw(h.RequestID, h.MemberCount-h.Cursor, h.Scope.ResourceID, h.Scope.RoundID, nil); err != nil {
				return err
			}
		}
		if next <= after {
			return workstate.ErrManifest
		}
		after = next
	}
	for after := 0; ; {
		headers, next, err := store.LoadCandidateHeaders(ctx, f.jobID, after, 16)
		if err != nil {
			return err
		}
		if len(headers) == 0 {
			break
		}
		for _, h := range headers {
			if err := restoreCandidateHeader(runtime.Window, h); err != nil {
				return err
			}
		}
		if next <= after {
			return workstate.ErrManifest
		}
		after = next
	}
	return nil
}

func logHandoffFutureFailure(t *testing.T, f *rubyPipelineFixture, http *handoffHTTPBarrier) {
	t.Helper()
	ctx := context.Background()
	value := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	j := f.client.Job.GetX(ctx, f.jobID)
	t.Logf("future_failure job_status=%s error=%s", j.Status, value(j.ErrorMessage))
	for _, row := range f.client.JobResource.Query().AllX(ctx) {
		t.Logf("future_failure resource%d status%s error%s", row.ID, row.Status, value(row.ErrorMessage))
	}
	for _, row := range f.client.JobRound.Query().AllX(ctx) {
		t.Logf("future_failure round%d/index%d status%s error%s", row.ID, row.RoundIndex, row.Status, value(row.ErrorMessage))
	}
	state, err := workstate.NewStore(f.client).LoadRuntimeState(ctx, f.jobID)
	t.Logf("future_failure runtime_state=%+v err=%v", state, err)
	for _, row := range f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).Select(workrequest.FieldIdentity, workrequest.FieldState, workrequest.FieldRetryEpoch, workrequest.FieldJobRoundID, workrequest.FieldResourceID, workrequest.FieldPromotionCursor, workrequest.FieldPromotionCount, workrequest.FieldPromotionState, workrequest.FieldFreezeBytes, workrequest.FieldResponseBytes, workrequest.FieldBusinessBound, workrequest.FieldCorrectedTargetBytes).AllX(ctx) {
		t.Logf("request%s/round%d/res%d/epoch%d %s phase%s cursor%d/%d freeze%d raw%d business%d corrected%d", row.Identity, row.JobRoundID, row.ResourceID, row.RetryEpoch, row.State, row.PromotionState, row.PromotionCursor, row.PromotionCount, row.FreezeBytes, row.ResponseBytes, row.BusinessBound, row.CorrectedTargetBytes)
	}
	for _, row := range f.client.WorkCandidate.Query().Select(workcandidate.FieldIdentity, workcandidate.FieldPayloadBytes, workcandidate.FieldState, workcandidate.FieldVersion, workcandidate.FieldDtoVersion).AllX(ctx) {
		t.Logf("candidate%s state%s bytes%d version%d DTO%d", row.Identity, row.State, row.PayloadBytes, row.Version, row.DtoVersion)
	}
	for _, row := range f.client.WorkItem.Query().Where(workitem.JobIDEQ(f.jobID)).AllX(ctx) {
		t.Logf("member%d/round%d/res%d state%s candidate%s raw%s attempts%d", row.SegmentID, row.JobRoundID, row.ResourceID, row.State, row.CandidateID, row.ResponseRequestID, row.MainAttempts)
	}
	http.mu.Lock()
	calls := http.calls
	members := append([]int(nil), http.members...)
	http.mu.Unlock()
	t.Logf("future_failure actual_HTTP=%d members=%v checkpoints=%d", calls, members, f.client.JobRoundSegment.Query().CountX(ctx))
}
