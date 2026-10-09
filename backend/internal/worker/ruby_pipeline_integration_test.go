package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/backend/openai"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

const rubyPipelineTarget = "<ruby>alpha<rt>reading-a</rt></ruby> <ruby>beta<rt>reading-b</rt></ruby>"

type rubyPipelineMissing struct {
	ID         string `json:"id"`
	SourceBase string `json:"source_base"`
	SourceText string `json:"source_text"`
}

type rubyPipelineRequest struct {
	Segments map[string]prompt.SegmentDetail `json:"segments"`
	Missing  []rubyPipelineMissing           `json:"missing"`
}

// These tests use the provider's real HTTP adapter, real credential lookup,
// SQLite transactions and the production worker round store. Faults are placed
// outside transactions so a single-connection database cannot deadlock the test.
type rubyPipelineFixture struct {
	client       *ent.Client
	jobs         *service.JobService
	runner       *JobRunner
	factory      *EngineFactory
	snapshot     *service.JobExecutionSnapshot
	pool         *backend.LimiterPool
	logger       *slog.Logger
	jobID        int
	ownerID      int
	resourceIDs  []int
	jobResources []int
	roundIDs     []int

	mu       sync.Mutex
	requests []rubyPipelineRequest
	respond  func(context.Context, rubyPipelineRequest) (any, error)
}

func newRubyPipelineFixture(t *testing.T, segmentCounts ...int) *rubyPipelineFixture {
	t.Helper()
	ctx := context.Background()
	client := newRegistryTestClient(t)
	jobID, first, second := registryFixture(t, client)
	owner := client.User.Query().OnlyX(ctx)
	f := &rubyPipelineFixture{
		client: client, jobID: jobID, ownerID: owner.ID,
		jobResources: []int{first, second}, pool: backend.NewLimiterPool(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	t.Cleanup(f.pool.Shutdown)
	upstream := httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(upstream.Close)
	users := service.NewUserService(client, nil)
	keyJSON, err := json.Marshal(map[string]any{
		"version": 1, "active_key_id": "integration",
		"keys": map[string]string{"integration": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))},
	})
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := credential.ParseKeyring(keyJSON)
	if err != nil {
		t.Fatal(err)
	}
	credentials := service.NewCredentialService(client, keyring, users)
	backends := service.NewBackendService(client, users, f.pool)
	backends.SetCredentials(credentials)
	secret := "integration-only"
	provider, err := backends.Create(ctx, service.CreateBackendInput{
		Scope: service.ScopeUser, OwnerUserID: &owner.ID,
		BackendInput: service.BackendInput{
			Name: "ruby-pipeline", Type: "openai", Secret: &secret,
			Options: map[string]any{"base_url": upstream.URL, "model": "fixture", "response_format": "none"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.pool.Initialize(map[int]int{provider.ID: 0})
	spec := translateSnapshot(t)
	spec.Rounds[0].Backend = service.BackendSnapshot{
		ID: provider.ID, Scope: provider.Scope, Name: provider.Name, Type: provider.Type,
		Options: provider.Options, Credential: provider.Credential,
	}
	spec.Rounds[0].Translate.BatchSize = 2
	spec.Rounds[0].Translate.Retry.MaxAttempts = 2
	spec.Strategy.Ruby.Enabled = true
	spec.Strategy.Ruby.PreserveKinds = []string{"creative"}
	spec.RubyRetry = &service.ExecutionPlanRubyRetrySnapshot{
		Enabled: true, Backend: spec.Rounds[0].Backend, MaxAttempts: 2, Concurrency: 1,
	}
	f.snapshot = completeWorkerSnapshot(t, spec)
	f.persistSnapshot(t)
	client.Job.UpdateOneID(jobID).SetCreatedByID(owner.ID).ExecX(ctx)
	for i, jrID := range f.jobResources {
		resource := client.JobResource.Query().Where(jobresource.IDEQ(jrID)).WithResource().OnlyX(ctx).Edges.Resource
		n := 0
		if i < len(segmentCounts) {
			n = segmentCounts[i]
		}
		f.resourceIDs = append(f.resourceIDs, resource.ID)
		update := client.JobResource.UpdateOneID(jrID).SetSegmentCount(n).
			SetSourceGeneration(resource.SourceGeneration).SetNillableSourceRevisionID(resource.CurrentSourceRevisionID)
		if n == 0 {
			update.SetStatus(service.JobResourceStatusCompleted)
		}
		update.ExecX(ctx)
		client.Resource.UpdateOneID(resource.ID).SetTotalSegments(n).ExecX(ctx)
		for index := range n {
			source := fmt.Sprintf("<ruby>source-a<rt>original-a</rt></ruby> <ruby>source-b<rt>original-b</rt></ruby> segment-%d-%d", i, index)
			client.Segment.Create().SetResourceID(resource.ID).SetSegmentIndex(index).SetSourceText(source).ExecX(ctx)
		}
		f.roundIDs = append(f.roundIDs, createJobRoundRow(t, client, jobID, jrID, 0, "translate"))
	}
	broker := event.NewBroker(nil)
	f.jobs = service.NewJobService(client, service.NewProjectService(client, users), nil, backends, nil, nil, nil, nil, broker)
	f.runner = NewJobRunner(f.logger, client, f.jobs, nil, nil, broker, f.pool, NewResourceMutex(), "sqlite",
		PipelineConfig{Candidates: pipeline.DefaultCandidateLimits(), MaxInflightResources: 1}, nil)
	f.runner.SetCredentials(credentials, credentials)
	f.factory = NewEngineFactoryWithCredentials(f.logger, f.pool, credentials, credentials)
	return f
}

func (f *rubyPipelineFixture) persistSnapshot(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var frozen map[string]any
	if err := json.Unmarshal(data, &frozen); err != nil {
		t.Fatal(err)
	}
	f.client.Job.UpdateOneID(f.jobID).SetExecutionConfig(frozen).ExecX(context.Background())
}

func rubyPipelineResponse(request rubyPipelineRequest) any {
	if request.Segments != nil {
		translations := make(map[string]string, len(request.Segments))
		for id, segment := range request.Segments {
			if segment.Translate {
				translations[id] = "alpha beta"
			}
		}
		return map[string]any{"translations": translations}
	}
	entries := make([]ruby.OutputEntry, 0, len(request.Missing))
	for _, missing := range request.Missing {
		base, reading := "alpha", "reading-a"
		if missing.SourceBase == "source-b" {
			base, reading = "beta", "reading-b"
		}
		entries = append(entries, ruby.OutputEntry{ID: missing.ID, Base: base, Text: reading, Kind: "creative", Occurrence: 1})
	}
	return map[string]any{"ruby_output": entries}
}

func (f *rubyPipelineFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var request rubyPipelineRequest
	for _, message := range payload.Messages {
		if message.Role == "user" {
			if err := json.Unmarshal([]byte(message.Content), &request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	}
	if request.Segments == nil && len(request.Missing) == 0 {
		http.Error(w, "missing translation or alignment payload", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, request)
	respond := f.respond
	f.mu.Unlock()
	content := rubyPipelineResponse(request)
	if respond != nil {
		var err error
		content, err = respond(r.Context(), request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "fixture", "object": "chat.completion",
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(encoded)}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 11, "total_tokens": 18},
	})
}

func (f *rubyPipelineFixture) calls() (main, align int, sources []string, alignment [][]rubyPipelineMissing) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, request := range f.requests {
		if request.Segments != nil {
			main++
			for _, segment := range request.Segments {
				if segment.Translate {
					sources = append(sources, segment.Source)
				}
			}
		} else {
			align++
			alignment = append(alignment, slices.Clone(request.Missing))
		}
	}
	sort.Strings(sources)
	return
}

func (f *rubyPipelineFixture) round(t *testing.T, resource int) (*pipeline.Document, *roundStore) {
	t.Helper()
	ctx := context.Background()
	res := f.client.Resource.GetX(ctx, f.resourceIDs[resource])
	job := f.client.Job.GetX(ctx, f.jobID)
	rows := f.client.Segment.Query().Where(segment.ResourceIDEQ(res.ID)).Order(ent.Asc(segment.FieldSegmentIndex)).AllX(ctx)
	doc := pipeline.BuildDocumentFromSegments(buildSegmentInputs(rows), f.snapshot.SourceLang, f.snapshot.TargetLang, res.Format)
	indices := make(map[int]int, len(rows))
	for index, row := range rows {
		indices[index] = row.ID
	}
	scope := workstate.Scope{
		JobID: f.jobID, ResourceID: res.ID, JobResourceID: f.jobResources[resource], RoundID: f.roundIDs[resource],
		RetryEpoch: job.RetryEpoch, SourceGeneration: res.SourceGeneration, SourceRevisionID: res.CurrentSourceRevisionID,
	}
	store := newRoundStore(f.client, scope, indices, f.snapshot, 0, nil, false, "sqlite", nil, nil)
	return doc, store
}

func (f *rubyPipelineFixture) execute(t *testing.T, resource int, gate *pipeline.PauseGate, wrap func(*roundStore) pipeline.RoundStore) (*pipeline.Document, pipeline.RunRoundResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if gate == nil {
		gate = pipeline.NewPauseGate()
	}
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, gate, pipeline.DefaultCandidateLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	ctx = pipeline.WithExecutionRuntime(ctx, runtime)
	eng, err := f.factory.BuildEngine(ctx, f.snapshot, engine.RuntimeResources{}, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	doc, durable := f.round(t, resource)
	eng.PrepareDocument(doc, nil)
	var store pipeline.RoundStore = durable
	if wrap != nil {
		store = wrap(durable)
	}
	result, err := pipeline.RunStagedRound(ctx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
	return doc, result, err
}

func (f *rubyPipelineFixture) assertAccepted(t *testing.T, resource, count int) {
	t.Helper()
	ctx := context.Background()
	rows := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[resource])).AllX(ctx)
	if len(rows) != count {
		t.Fatalf("segments=%d, want %d", len(rows), count)
	}
	for _, row := range rows {
		if row.TargetText == nil || *row.TargetText != rubyPipelineTarget || row.Status != segment.StatusTranslated || row.ContentVersion != 2 {
			t.Fatalf("accepted segment: %+v", row)
		}
	}
	if got := f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[resource])).CountX(ctx); got != count {
		t.Fatalf("round checkpoints=%d, want %d", got, count)
	}
	round := f.client.JobRound.GetX(ctx, f.roundIDs[resource])
	if round.SegmentTotal != count || round.SegmentCompleted != count {
		t.Fatalf("round progress=%d/%d, want %d/%d", round.SegmentCompleted, round.SegmentTotal, count, count)
	}
}

func (f *rubyPipelineFixture) assertUsage(t *testing.T, expected int) {
	t.Helper()
	ctx := context.Background()
	requests := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).AllX(ctx)
	if len(requests) != expected {
		t.Fatalf("durable requests=%d, want %d", len(requests), expected)
	}
	seen := map[int]bool{}
	for _, request := range requests {
		if request.UsageRecordID == nil || seen[*request.UsageRecordID] || !request.UsageKnown || request.InputTokens != 7 || request.OutputTokens != 11 {
			t.Fatalf("request usage is not unique and exact: %+v", request)
		}
		seen[*request.UsageRecordID] = true
	}
	usage := f.client.UsageRecord.Query().AllX(ctx)
	apiCalls, input, output := 0, 0, 0
	for _, row := range usage {
		apiCalls += row.APICalls
		input += row.InputTokens
		output += row.OutputTokens
	}
	if len(usage) != expected || apiCalls != expected || input != 7*expected || output != 11*expected {
		t.Fatalf("usage rows=%d HTTP=%d tokens=%d/%d, want %d rows/calls and %d/%d", len(usage), apiCalls, input, output, expected, 7*expected, 11*expected)
	}
}

type rubyPipelineFaultStore struct {
	*roundStore
	beforeSave func(context.Context, *pipeline.Candidate) error
	afterSave  func(context.Context, *pipeline.Candidate) error
	commit     func(context.Context, *pipeline.Candidate, pipeline.TranslatedSegment) (pipeline.CommitOutcome, error)
}

func (s *rubyPipelineFaultStore) Save(ctx context.Context, candidate *pipeline.Candidate) error {
	if s.beforeSave != nil {
		if err := s.beforeSave(ctx, candidate); err != nil {
			return err
		}
	}
	if err := s.roundStore.Save(ctx, candidate); err != nil {
		return err
	}
	if s.afterSave != nil {
		return s.afterSave(ctx, candidate)
	}
	return nil
}

func (s *rubyPipelineFaultStore) Commit(ctx context.Context, candidate *pipeline.Candidate, result pipeline.TranslatedSegment) (pipeline.CommitOutcome, error) {
	if s.commit != nil {
		return s.commit(ctx, candidate, result)
	}
	return s.roundStore.Commit(ctx, candidate, result)
}

func TestRubyPipelineSQLiteWorkerCountsActualRequestsOnce(t *testing.T) {
	f := newRubyPipelineFixture(t, 3, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.runner.processJob(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	f.assertAccepted(t, 0, 3)
	f.assertAccepted(t, 1, 2)
	main, align, _, _ := f.calls()
	if main != 3 || align != 5 {
		t.Fatalf("HTTP main/alignment=%d/%d, want 3/5", main, align)
	}
	f.assertUsage(t, 8)
	row := f.client.Job.GetX(ctx, f.jobID)
	if row.Status != service.JobStatusCompleted || row.ProgressTotal != 5 || row.ProgressCompleted != 5 {
		t.Fatalf("job=%s progress=%d/%d", row.Status, row.ProgressCompleted, row.ProgressTotal)
	}
	// A duplicated queue delivery must consume existing checkpoints, not usage.
	if err := f.runner.processJob(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	f.assertUsage(t, 8)
	if afterMain, afterAlign, _, _ := f.calls(); afterMain != main || afterAlign != align {
		t.Fatal("completed worker replay sent more HTTP requests")
	}
}

func TestRubyPipelineSQLitePauseResumeReusesMainDrafts(t *testing.T) {
	f := newRubyPipelineFixture(t, 2)
	gate := pipeline.NewPauseGate()
	var paused bool
	_, result, err := f.execute(t, 0, gate, func(store *roundStore) pipeline.RoundStore {
		return &rubyPipelineFaultStore{roundStore: store, afterSave: func(ctx context.Context, c *pipeline.Candidate) error {
			if paused {
				return nil
			}
			paused = true
			if _, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID); err != nil {
				return err
			}
			gate.Pause()
			return nil
		}}
	})
	if err != nil || !paused || len(result.Resolved) != 0 {
		t.Fatalf("pause result=%+v err=%v", result, err)
	}
	ctx := context.Background()
	if err := f.jobs.MarkJobPaused(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	main, align, _, _ := f.calls()
	if main != 1 || align != 0 {
		t.Fatalf("pause HTTP=%d/%d, want 1/0", main, align)
	}
	before := f.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(f.roundIDs[0])).Order(ent.Asc(workitem.FieldID)).AllX(ctx)
	if len(before) != 2 || before[0].MainAttempts != 1 || before[1].MainAttempts != 1 {
		t.Fatalf("saved main cursors: %+v", before)
	}
	resumed, err := f.jobs.ResumeJob(ctx, f.ownerID, f.jobID)
	if err != nil || resumed.RetryEpoch != 0 {
		t.Fatalf("resume epoch: row=%+v err=%v", resumed, err)
	}
	_, result, err = f.execute(t, 0, nil, nil)
	if err != nil || len(result.Unresolved) != 0 {
		t.Fatalf("resume result=%+v err=%v", result, err)
	}
	main, align, _, _ = f.calls()
	if main != 1 || align != 2 {
		t.Fatalf("resume repeated main work: HTTP=%d/%d", main, align)
	}
	f.assertAccepted(t, 0, 2)
	f.assertUsage(t, 3)
}

func TestRubyPipelineSQLiteRetryPreservesVerifiedMappingsAndResetsBudgets(t *testing.T) {
	f := newRubyPipelineFixture(t, 1)
	f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
		if len(request.Missing) == 2 {
			request.Missing = request.Missing[:1]
		}
		return rubyPipelineResponse(request), nil
	}
	gate := pipeline.NewPauseGate()
	_, _, err := f.execute(t, 0, gate, func(store *roundStore) pipeline.RoundStore {
		return &rubyPipelineFaultStore{roundStore: store, afterSave: func(ctx context.Context, c *pipeline.Candidate) error {
			if c.LogicalAttempt != 1 || c.Ready {
				return nil
			}
			if _, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID); err != nil {
				return err
			}
			gate.Pause()
			return nil
		}}
	})
	if err != nil || !gate.Paused() {
		t.Fatalf("pause after partial alignment: %v", err)
	}
	ctx := context.Background()
	row := f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0]))).OnlyX(ctx)
	draft, err := pipeline.DecodeCandidate(row.Payload)
	if err != nil || len(draft.Alignment.Verified) != 1 || len(draft.Alignment.Missing()) != 1 {
		t.Fatalf("partial draft=%+v err=%v", draft, err)
	}
	verified := draft.Alignment.Verified[0]
	missingID := draft.Alignment.Missing()[0].ID
	if _, err := f.jobs.CancelJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	before := f.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(f.roundIDs[0])).OnlyX(ctx)
	if before.MainAttempts != 1 || before.AlignmentAttempts != 1 {
		t.Fatalf("pre-retry budgets: %+v", before)
	}
	retried, err := f.jobs.RetryJob(ctx, f.ownerID, f.jobID)
	if err != nil || retried.RetryEpoch != 1 {
		t.Fatalf("retry epoch: row=%+v err=%v", retried, err)
	}
	if _, err := f.jobs.RetryJob(ctx, f.ownerID, f.jobID); !errors.Is(err, service.ErrJobNotRetryable) {
		t.Fatalf("duplicate retry allocated another epoch: %v", err)
	}
	reset := f.client.WorkItem.GetX(ctx, before.ID)
	if reset.RetryEpoch != 1 || reset.MainAttempts != 0 || reset.AlignmentAttempts != 0 || reset.MainNetworkAttempts != 0 || reset.AlignmentNetworkAttempts != 0 || reset.NextAttemptAt != nil || reset.CandidateID != row.Identity {
		t.Fatalf("retry budgets were not reset with the draft retained: %+v", reset)
	}
	_, store := f.round(t, 0)
	recovered, _, err := store.Candidates(ctx, 0, 16)
	if err != nil || len(recovered) != 1 {
		t.Fatalf("retry candidate load: %v", err)
	}
	c := recovered[0]
	if c.RetryEpoch != 1 || c.LogicalAttempt != 0 || c.NetworkAttempt != 0 || c.MainAttempt != 0 || len(c.Alignment.Verified) != 1 || c.Alignment.Verified[0] != verified {
		t.Fatalf("retry lost verified mapping or retained exhausted budget: %+v", c)
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Unresolved) != 0 {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
	main, align, _, requests := f.calls()
	if main != 1 || align != 2 || len(requests[1]) != 1 || requests[1][0].ID != missingID {
		t.Fatalf("retry did not send only the missing mapping: main=%d alignment=%+v", main, requests)
	}
	f.assertAccepted(t, 0, 1)
	f.assertUsage(t, 3)
}

func TestRubyPipelineSQLitePartialMainSaveRecoverySkipsSavedSegments(t *testing.T) {
	f := newRubyPipelineFixture(t, 2)
	injected := errors.New("second candidate save failed")
	_, _, err := f.execute(t, 0, nil, func(store *roundStore) pipeline.RoundStore {
		return &rubyPipelineFaultStore{roundStore: store, beforeSave: func(_ context.Context, c *pipeline.Candidate) error {
			if c.Index == 1 && c.Version == 1 {
				return permanentStoreError{injected}
			}
			return nil
		}}
	})
	if !errors.Is(err, injected) {
		t.Fatalf("expected durable handoff failure, got %v", err)
	}
	ctx := context.Background()
	saved := f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0]))).AllX(ctx)
	if len(saved) != 1 {
		t.Fatalf("partial main response saved %d candidates, want 1", len(saved))
	}
	request := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).OnlyX(ctx)
	if request.State != "received" || !request.UsageKnown {
		t.Fatalf("failed save claimed reliable completion: %+v", request)
	}
	if err := f.jobs.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Unresolved) != 0 {
		t.Fatalf("partial-save recovery result=%+v err=%v", result, err)
	}
	main, align, sources, _ := f.calls()
	occurrences := map[string]int{}
	for _, source := range sources {
		if strings.Contains(source, "segment-0-0") {
			occurrences["saved"]++
		}
		if strings.Contains(source, "segment-0-1") {
			occurrences["unsaved"]++
		}
	}
	if main != 2 || align != 2 || occurrences["saved"] != 1 || occurrences["unsaved"] != 2 {
		t.Fatalf("partial save caused replay of durable work: main=%d alignment=%d sources=%v", main, align, sources)
	}
	f.assertAccepted(t, 0, 2)
	f.assertUsage(t, 4)
}

func TestRubyPipelineSQLiteReadyRecoveryCommitsWithoutRequests(t *testing.T) {
	f := newRubyPipelineFixture(t, 1)
	injected := errors.New("acceptance transaction unavailable")
	_, _, err := f.execute(t, 0, nil, func(store *roundStore) pipeline.RoundStore {
		return &rubyPipelineFaultStore{roundStore: store, commit: func(context.Context, *pipeline.Candidate, pipeline.TranslatedSegment) (pipeline.CommitOutcome, error) {
			return "", permanentStoreError{injected}
		}}
	})
	if !errors.Is(err, injected) {
		t.Fatalf("expected commit failure, got %v", err)
	}
	ctx := context.Background()
	row := f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0]))).OnlyX(ctx)
	if row.State != "ready_to_commit" {
		t.Fatalf("ready draft not durable: %s", row.State)
	}
	main, align, _, _ := f.calls()
	if main != 1 || align != 1 {
		t.Fatalf("HTTP before recovery=%d/%d", main, align)
	}
	if err := f.jobs.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, result, err := f.execute(t, 0, nil, nil)
		if err != nil || len(result.Unresolved) != 0 {
			t.Fatalf("ready recovery result=%+v err=%v", result, err)
		}
	}
	if afterMain, afterAlign, _, _ := f.calls(); afterMain != main || afterAlign != align {
		t.Fatal("ready candidate recovery made additional network requests")
	}
	f.assertAccepted(t, 0, 1)
	f.assertUsage(t, 2)
}

func TestRubyPipelineSQLiteCancellationRejectsLateContent(t *testing.T) {
	f := newRubyPipelineFixture(t, 1)
	f.respond = func(ctx context.Context, request rubyPipelineRequest) (any, error) {
		if _, err := f.jobs.CancelJob(ctx, f.ownerID, f.jobID); err != nil {
			return nil, err
		}
		return rubyPipelineResponse(request), nil
	}
	_, _, err := f.execute(t, 0, nil, nil)
	if !errors.Is(err, workstate.ErrStopped) {
		t.Fatalf("cancelled job accepted late content: %v", err)
	}
	ctx := context.Background()
	row := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(ctx)
	if row.TargetText != nil || row.Status != segment.StatusPending || row.ContentVersion != 1 {
		t.Fatalf("late response changed content: %+v", row)
	}
	if f.client.WorkCandidate.Query().CountX(ctx) != 0 || f.client.JobRoundSegment.Query().CountX(ctx) != 0 {
		t.Fatal("late response created candidate or acceptance checkpoint")
	}
	if got := f.client.JobRound.Query().Where(jobround.IDEQ(f.roundIDs[0])).OnlyX(ctx).SegmentCompleted; got != 0 {
		t.Fatalf("late response advanced progress to %d", got)
	}
	main, align, _, _ := f.calls()
	if main != 1 || align != 0 {
		t.Fatalf("cancellation HTTP=%d/%d", main, align)
	}
	f.assertUsage(t, 1)
}

func TestRubyPipelineSQLiteRecoveryYieldsToDraftsBeyondLoweredWindow(t *testing.T) {
	f := newRubyPipelineFixture(t, 2, 2)
	f.snapshot.Rounds[0].Translate.BatchSize = 1
	f.persistSnapshot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Reconstruct a paused job with one saved main response and one unsent
	// member in each resource. Both candidates were accepted under larger limits.
	for resource := range 2 {
		if err := f.jobs.MarkJobRunning(ctx, f.jobID); err != nil {
			t.Fatal(err)
		}
		gate := pipeline.NewPauseGate()
		_, _, err := f.execute(t, resource, gate, func(store *roundStore) pipeline.RoundStore {
			return &rubyPipelineFaultStore{roundStore: store, afterSave: func(ctx context.Context, c *pipeline.Candidate) error {
				if c.Index != 0 || c.Version != 1 {
					return nil
				}
				if _, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID); err != nil {
					return err
				}
				gate.Pause()
				return nil
			}}
		})
		if err != nil || !gate.Paused() {
			t.Fatalf("seed resource %d: %v", resource, err)
		}
		if err := f.jobs.MarkJobPaused(ctx, f.jobID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
			t.Fatal(err)
		}
	}
	if f.client.WorkCandidate.Query().Where(workcandidate.StateEQ("pending_alignment")).CountX(ctx) != 2 {
		t.Fatal("expected two durable drafts before lowering the window")
	}
	main, align, _, _ := f.calls()
	if main != 2 || align != 0 {
		t.Fatalf("seed HTTP=%d/%d, want 2/0", main, align)
	}
	f.runner.pipeCfg.Candidates.Segments = 1
	f.runner.pipeCfg.MaxInflightResources = 1
	if err := f.runner.processJob(ctx, f.jobID); err != nil {
		t.Fatalf("single resource admission failed to drain recovered candidates: %v", err)
	}
	f.assertAccepted(t, 0, 2)
	f.assertAccepted(t, 1, 2)
	main, align, sources, _ := f.calls()
	if main != 4 || align != 4 || len(sources) != 4 {
		t.Fatalf("restored work was replayed: main=%d alignment=%d sources=%v", main, align, sources)
	}
	for index, source := range sources {
		if index > 0 && source == sources[index-1] {
			t.Fatalf("main segment was resent after resource yield: %s", source)
		}
	}
	f.assertUsage(t, 8)
	job := f.client.Job.GetX(ctx, f.jobID)
	if job.Status != service.JobStatusCompleted || job.ProgressTotal != 4 || job.ProgressCompleted != 4 {
		t.Fatalf("yield recovery job=%s progress=%d/%d", job.Status, job.ProgressCompleted, job.ProgressTotal)
	}
}
