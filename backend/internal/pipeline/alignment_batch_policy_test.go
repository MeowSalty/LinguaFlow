package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

type batchPolicyBackend struct {
	calls atomic.Int64
	call  func(context.Context, backend.Request) (*backend.Response, error)
}

type batchPolicyReporter struct {
	progress.Nop
	events []progress.BatchEvent
}

func (r *batchPolicyReporter) OnBatchEvent(event progress.BatchEvent) {
	r.events = append(r.events, event)
}

func (*batchPolicyBackend) Name() string { return "batch-policy-test" }
func (*batchPolicyBackend) Close() error { return nil }
func (b *batchPolicyBackend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	b.calls.Add(1)
	return b.call(ctx, req)
}

type batchPolicyStore struct {
	*MemoryRoundStore
	mu       sync.Mutex
	intents  []RequestIntent
	records  []RequestRecord
	events   []string
	onSave   func(context.Context, *Candidate) error
	onCommit func(context.Context, *Candidate) error
}

func (s *batchPolicyStore) event(value string) {
	s.mu.Lock()
	s.events = append(s.events, value)
	s.mu.Unlock()
}
func (s *batchPolicyStore) Reserve(_ context.Context, intent RequestIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.intents = append(s.intents, intent)
	s.events = append(s.events, "reserve")
	return nil
}
func (s *batchPolicyStore) Record(_ context.Context, _ string, record RequestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	s.events = append(s.events, record.State)
	return nil
}
func (s *batchPolicyStore) Save(ctx context.Context, c *Candidate) error {
	s.event(fmt.Sprintf("save:%d", c.Index))
	if s.onSave != nil {
		if err := s.onSave(ctx, c); err != nil {
			return err
		}
	}
	return s.MemoryRoundStore.Save(ctx, c)
}
func (s *batchPolicyStore) Commit(ctx context.Context, c *Candidate, result TranslatedSegment) (CommitOutcome, error) {
	s.event(fmt.Sprintf("commit:%d", c.Index))
	if s.onCommit != nil {
		if err := s.onCommit(ctx, c); err != nil {
			return "", err
		}
	}
	return s.MemoryRoundStore.Commit(ctx, c, result)
}
func (s *batchPolicyStore) snapshot() ([]RequestIntent, []RequestRecord, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.intents), slices.Clone(s.records), slices.Clone(s.events)
}

func batchPolicyFixture(t *testing.T, logicalBudget int) (*ExecutionRuntime, *batchPolicyStore, []*Candidate, *batchPolicyBackend, Round) {
	t.Helper()
	runtime := stagedTestRuntime(t, DefaultCandidateLimits())
	store := &batchPolicyStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
	candidates := make([]*Candidate, 2)
	for i := range candidates {
		c := newCandidate(Segment{ID: fmt.Sprintf("segment-%d", i), Source: "original", Status: "pending"}, i, RoundModeTranslate, "text")
		c.ID, c.WorkID = fmt.Sprintf("candidate-%d", i), fmt.Sprintf("work-%d", i)
		c.Segment.Target = "alpha beta"
		var err error
		c.Alignment, err = PrepareAlignment(c.Segment.Target, c.Format, []ruby.Item{
			{ID: "1", SourceBase: "original-one", SourceText: "read-one"},
			{ID: "2", SourceBase: "original-two", SourceText: "read-two"},
		}, nil, ruby.ProtocolV2, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MemoryRoundStore.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		if err := runtime.Window.Restore(c.ID, c.StoredBytes); err != nil {
			t.Fatal(err)
		}
		candidates[i] = c
	}
	remote := &batchPolicyBackend{}
	round := Round{Retry: backend.RetryPolicy{MaxAttempts: 2}, Handler: &TranslateHandler{
		RubyRetryBackends: []backend.Backend{BindRequestBackend(remote, 1, 0, runtime)}, RubyRetryAttempts: logicalBudget,
		RubyTemplates: prompt.RubyTemplates{JSON: prompt.RubyAlignmentJSONTemplate, Text: prompt.RubyAlignmentTextTemplate, BatchJSON: prompt.RubyAlignmentBatchJSONTemplate, BatchText: prompt.RubyAlignmentBatchTextTemplate},
		RubyBatch:     AlignmentBatchConfig{BatchSize: 4, ProtocolVersion: ruby.BatchProtocolVersion},
	}}
	return runtime, store, candidates, remote, round
}

func runBatchPolicyWorker(t *testing.T, runtime *ExecutionRuntime, store RoundStore, round Round, candidates []*Candidate) alignmentUpdate {
	t.Helper()
	worker := &AlignmentWorker{runtime: runtime, store: store, reporter: progress.Nop{}}
	update := worker.RunBatch(context.Background(), round, candidates)
	if err := worker.finish(update.err); err != nil && update.err == nil {
		update.err = err
	}
	return update
}

func batchPolicyRow(id, base string) string {
	return fmt.Sprintf(`{"id":%q,"base":%q,"text":"reading","kind":"creative","occurrence":1}`, id, base)
}
func batchPolicyMember(c *Candidate, rows ...string) string {
	return fmt.Sprintf(`{"work_id":%q,"candidate_id":%q,"ruby_output":[%s]}`, c.WorkID, c.ID, strings.Join(rows, ","))
}
func batchPolicyResponse(members ...string) string {
	return `{"alignments":[` + strings.Join(members, ",") + `]}`
}

func TestAlignmentBatchPolicyPartialProgressRebatchesOnlyMissing(t *testing.T) {
	runtime, store, candidates, remote, round := batchPolicyFixture(t, 3)
	remote.call = func(_ context.Context, req backend.Request) (*backend.Response, error) {
		var input struct {
			Alignments []struct {
				Missing []ruby.Item `json:"missing"`
			} `json:"alignments"`
		}
		if err := json.Unmarshal([]byte(req.User), &input); err != nil {
			return nil, err
		}
		if len(input.Alignments) != 2 || req.System != prompt.RubyAlignmentBatchJSONTemplate {
			return nil, errors.New("partial candidates stopped batching")
		}
		row := batchPolicyRow("1", "alpha")
		if remote.calls.Load() == 2 {
			for _, member := range input.Alignments {
				if len(member.Missing) != 1 || member.Missing[0].ID != "2" {
					return nil, errors.New("verified item was resent")
				}
			}
			row = batchPolicyRow("2", "beta")
		}
		return &backend.Response{Text: batchPolicyResponse(batchPolicyMember(candidates[1], row), batchPolicyMember(candidates[0], row)), Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 9}}, nil
	}
	var lastRequest string
	for attempt := 1; attempt <= 2; attempt++ {
		update := runBatchPolicyWorker(t, runtime, store, round, candidates)
		if update.err != nil || update.inputTokens != 17 || update.outputTokens != 9 {
			t.Fatalf("one request usage changed: %+v", update)
		}
		for _, c := range candidates {
			if c.LogicalAttempt != attempt || c.NetworkAttempt != 0 || len(c.Alignment.Verified) != attempt || c.ForceSingleAlignment || c.Ready != (attempt == 2) {
				t.Fatalf("member progress/budget changed: %+v", c)
			}
			if c.LastAlignmentRequestID == "" || c.LastAlignmentRequestID != candidates[0].LastAlignmentRequestID || c.LastAlignmentRequestID == lastRequest {
				t.Fatal("members did not share their current request identity")
			}
		}
		lastRequest = candidates[0].LastAlignmentRequestID
	}
	intents, records, _ := store.snapshot()
	if len(intents) != 2 || remote.calls.Load() != 2 {
		t.Fatalf("request was counted per member: %+v", intents)
	}
	for _, intent := range intents {
		if len(intent.Members) != 2 || intent.CandidateID != "" || intent.Stage != backend.RequestStageAlignment {
			t.Fatalf("batch request lost member ledger: %+v", intent)
		}
	}
	var promptTokens, completionTokens int64
	for _, record := range records {
		if record.UsageKnown {
			promptTokens += record.Usage.PromptTokens
			completionTokens += record.Usage.CompletionTokens
		}
	}
	if promptTokens != 34 || completionTokens != 18 {
		t.Fatalf("usage counted more than once: %d/%d", promptTokens, completionTokens)
	}
}

func TestAlignmentBatchPolicyEmptyResultEarlyStopsWithoutFallback(t *testing.T) {
	for _, textMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("text=%t", textMode), func(t *testing.T) {
			runtime, store, candidates, remote, round := batchPolicyFixture(t, 3)
			if textMode {
				round.Handler.(*TranslateHandler).ResponseMode = "text"
			}
			remote.call = func(_ context.Context, req backend.Request) (*backend.Response, error) {
				response := batchPolicyResponse(batchPolicyMember(candidates[0]), batchPolicyMember(candidates[1], batchPolicyRow("1", "alpha")))
				if textMode {
					if req.JSONSchema != nil || req.ResponseFormat != "none" || req.System != prompt.RubyAlignmentBatchTextTemplate {
						return nil, errors.New("text batch request used JSON settings")
					}
					response = `"work-0" | "candidate-0" | "" | "" | "" | "" | 0` + "\n" + `"work-1" | "candidate-1" | "alpha" | "reading" | "creative" | "1" | 1`
				}
				return &backend.Response{Text: response}, nil
			}
			update := runBatchPolicyWorker(t, runtime, store, round, candidates)
			if update.err != nil || !candidates[0].Ready || candidates[1].Ready || candidates[0].ForceSingleAlignment || candidates[1].ForceSingleAlignment {
				t.Fatalf("valid empty response changed batching policy: %+v %+v", update, candidates)
			}
			result, accepted, err := FinalizeCandidate(candidates[0])
			if err != nil || !accepted || len(result.Issues) != 1 {
				t.Fatalf("translate warning lost: %+v %t %v", result, accepted, err)
			}
			candidates[0].Mode = RoundModeRevise
			if _, accepted, err := FinalizeCandidate(candidates[0]); err != nil || accepted {
				t.Fatalf("unresolved revision accepted: %t %v", accepted, err)
			}
		})
	}
}

func TestAlignmentBatchPolicyOnlyAffectedMembersFallback(t *testing.T) {
	for _, failure := range []string{"missing", "duplicate", "truncated", "truncated_duplicate", "invalid_row", "unknown"} {
		t.Run(failure, func(t *testing.T) {
			runtime, store, candidates, remote, round := batchPolicyFixture(t, 3)
			remote.call = func(context.Context, backend.Request) (*backend.Response, error) {
				good := batchPolicyMember(candidates[0], batchPolicyRow("1", "alpha"), batchPolicyRow("2", "beta"))
				partial := batchPolicyMember(candidates[1], batchPolicyRow("1", "alpha"))
				response := &backend.Response{}
				switch failure {
				case "missing":
					response.Text = batchPolicyResponse(good)
				case "duplicate":
					response.Text = batchPolicyResponse(good, partial, partial)
				case "truncated":
					response.Text, response.Truncated = batchPolicyResponse(good, partial), true
				case "truncated_duplicate":
					response.Text = `{"alignments":[` + good + `,` + partial + `,{"work_id":"work-1","candidate_id":"candidate-1","ruby_output":[`
				case "invalid_row":
					response.Text = batchPolicyResponse(good, batchPolicyMember(candidates[1], `{"id":"1","base":"alpha","text":"reading","kind":"creative","occurrence":0}`))
				case "unknown":
					response.Text = batchPolicyResponse(good, partial, `{"work_id":"other","candidate_id":"other","ruby_output":[]}`)
				}
				return response, nil
			}
			update := runBatchPolicyWorker(t, runtime, store, round, candidates)
			if update.err != nil || !candidates[0].Ready || candidates[0].ForceSingleAlignment || len(candidates[0].Alignment.Verified) != 2 {
				t.Fatalf("safe completed member was affected: %+v %+v", update, candidates[0])
			}
			wantFallback := failure != "invalid_row" && failure != "unknown"
			if candidates[1].ForceSingleAlignment != wantFallback || candidates[1].Ready != (failure == "invalid_row") || candidates[1].LogicalAttempt != 1 {
				t.Fatalf("wrong fallback/early stop policy: %+v", candidates[1])
			}
			wantVerified := 0
			if failure == "truncated" || failure == "unknown" {
				wantVerified = 1
			}
			if len(candidates[1].Alignment.Verified) != wantVerified {
				t.Fatalf("safe partial mappings lost or duplicate accepted: %+v", candidates[1].Alignment.Verified)
			}
			page, _, err := store.Candidates(context.Background(), 0, 10)
			if err != nil || len(page) != 2 || page[1].ForceSingleAlignment != wantFallback || page[1].LastAlignmentRequestID == "" {
				t.Fatalf("fallback/request proof did not survive reload: %+v %v", page, err)
			}
		})
	}
}

func TestAlignmentBatchPolicyUnknownMembersAreDiagnosedWithoutChangingProgress(t *testing.T) {
	runtime, store, candidates, remote, round := batchPolicyFixture(t, 3)
	remote.call = func(context.Context, backend.Request) (*backend.Response, error) {
		return &backend.Response{Text: batchPolicyResponse(
			batchPolicyMember(candidates[0], batchPolicyRow("1", "alpha")),
			`{"work_id":"unknown-valid","candidate_id":"other","ruby_output":[]}`,
			batchPolicyMember(candidates[1], batchPolicyRow("1", "alpha")),
			`{"work_id":"unknown-duplicate","candidate_id":"other","ruby_output":[]}`,
			`{"work_id":"unknown-duplicate","candidate_id":"other","ruby_output":[]}`,
			`null`,
		), Usage: backend.Usage{PromptTokens: 11, CompletionTokens: 5}}, nil
	}
	reporter := &batchPolicyReporter{}
	worker := &AlignmentWorker{runtime: runtime, store: store, reporter: reporter}
	update := worker.RunBatch(context.Background(), round, candidates)
	if err := worker.finish(update.err); err != nil || update.err != nil {
		t.Fatalf("diagnostic response failed: %+v %v", update, err)
	}
	if len(reporter.events) != 1 {
		t.Fatalf("batch diagnostics emitted per member: %+v", reporter.events)
	}
	event := reporter.events[0]
	if event.UnknownAlignmentMembers != 2 || !event.EnvelopeInvalid || len(event.ProtocolDiagnostics) < 2 || event.Status != "partial" || event.SegmentCount != 2 || len(event.AlignmentMembers) != 2 {
		t.Fatalf("unknown/duplicate/envelope diagnostics lost: %+v", event)
	}
	if event.InputTokens != 11 || event.OutputTokens != 5 || update.inputTokens != 11 || update.outputTokens != 5 {
		t.Fatalf("diagnostic members changed request accounting: %+v %+v", event, update)
	}
	for _, c := range candidates {
		if c.Ready || c.ForceSingleAlignment || c.LogicalAttempt != 1 || c.NetworkAttempt != 0 || len(c.Alignment.Verified) != 1 {
			t.Fatalf("unknown members changed legitimate progress or budgets: %+v", c)
		}
	}
	intents, _, _ := store.snapshot()
	if len(intents) != 1 || len(intents[0].Members) != 2 {
		t.Fatalf("unrequested member entered request ledger: %+v", intents)
	}
}

func TestAlignmentBatchPolicyBudgetDoesNotGrantFallbackCalls(t *testing.T) {
	runtime, store, candidates, remote, round := batchPolicyFixture(t, 1)
	remote.call = func(context.Context, backend.Request) (*backend.Response, error) {
		return &backend.Response{Text: `{"alignments":[]}`}, nil
	}
	for pass := 0; pass < 2; pass++ {
		if update := runBatchPolicyWorker(t, runtime, store, round, candidates); update.err != nil {
			t.Fatal(update.err)
		}
	}
	for _, c := range candidates {
		if !c.Ready || !c.ForceSingleAlignment || c.LogicalAttempt != 1 || remote.calls.Load() != 1 {
			t.Fatalf("fallback added a logical attempt: %+v calls=%d", c, remote.calls.Load())
		}
	}
}

func TestAlignmentBatchPolicyNetworkRetriesKeepBatchAndMemberBudgets(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(fmt.Sprintf("status=%d", status), func(t *testing.T) {
			runtime, store, candidates, remote, round := batchPolicyFixture(t, 3)
			candidates[0].LogicalAttempt = 1
			remote.call = func(context.Context, backend.Request) (*backend.Response, error) {
				if remote.calls.Load() == 1 {
					return nil, &backend.StatusError{StatusCode: status, RetryAfter: time.Second, Err: errors.New("temporary")}
				}
				return &backend.Response{Text: batchPolicyResponse(batchPolicyMember(candidates[0], batchPolicyRow("1", "alpha")), batchPolicyMember(candidates[1], batchPolicyRow("1", "alpha")))}, nil
			}
			before := time.Now()
			if update := runBatchPolicyWorker(t, runtime, store, round, candidates); update.err != nil {
				t.Fatal(update.err)
			}
			for i, c := range candidates {
				if c.Ready || c.ForceSingleAlignment || c.NetworkAttempt != 1 || c.LogicalAttempt != 1-i || c.NextAttemptAt.Before(before.Add(time.Second)) {
					t.Fatalf("network retry changed policy/budgets: %+v", c)
				}
			}
			// The coordinator waits for NextAttemptAt; direct RunBatch tests only
			// the policy after that deadline, without sleeping for provider backoff.
			if update := runBatchPolicyWorker(t, runtime, store, round, candidates); update.err != nil {
				t.Fatal(update.err)
			}
			for i, c := range candidates {
				if c.Ready || c.ForceSingleAlignment || c.NetworkAttempt != 0 || c.LogicalAttempt != 2-i || !c.NextAttemptAt.IsZero() {
					t.Fatalf("successful retry shared/reset logical budgets: %+v", c)
				}
			}
			intents, _, _ := store.snapshot()
			if len(intents) != 2 || len(intents[1].Members) != 2 || intents[1].Members[0].NetworkAttempt != 1 || intents[1].Members[0].LogicalAttempt != 1 || intents[1].Members[1].LogicalAttempt != 0 {
				t.Fatalf("member attempt ledger flattened: %+v", intents)
			}
		})
	}
}

func TestAlignmentBatchPolicyNetworkExhaustionIsMemberLocal(t *testing.T) {
	runtime, store, candidates, remote, round := batchPolicyFixture(t, 3)
	candidates[0].NetworkAttempt = 1
	remote.call = func(_ context.Context, req backend.Request) (*backend.Response, error) {
		if remote.calls.Load() == 1 {
			return nil, &backend.StatusError{StatusCode: 503, Err: errors.New("temporary")}
		}
		if req.System != prompt.RubyAlignmentJSONTemplate || strings.Contains(req.User, `"alignments"`) {
			return nil, errors.New("exhausted member was resent in the remaining member's request")
		}
		return &backend.Response{Text: `{"ruby_output":[` + batchPolicyRow("1", "alpha") + `]}`}, nil
	}
	if update := runBatchPolicyWorker(t, runtime, store, round, candidates); update.err != nil {
		t.Fatal(update.err)
	}
	if !candidates[0].Ready || candidates[1].Ready || candidates[0].NetworkAttempt != 2 || candidates[1].NetworkAttempt != 1 {
		t.Fatalf("network budgets were shared across members: %+v %+v", candidates[0], candidates[1])
	}
	if update := runBatchPolicyWorker(t, runtime, store, round, candidates); update.err != nil {
		t.Fatal(update.err)
	}
	if candidates[0].LogicalAttempt != 0 || candidates[1].LogicalAttempt != 1 || candidates[0].ForceSingleAlignment || candidates[1].ForceSingleAlignment {
		t.Fatalf("natural tail batch was treated as protocol fallback: %+v %+v", candidates[0], candidates[1])
	}
	intents, _, _ := store.snapshot()
	if len(intents) != 2 || len(intents[1].Members) != 1 || intents[1].Members[0].CandidateID != candidates[1].ID {
		t.Fatalf("terminal member remained in request ledger: %+v", intents)
	}
}

func TestAlignmentBatchPolicyTransientSaveFailureRetriesOnlyPersistence(t *testing.T) {
	runtime, store, candidates, remote, round := batchPolicyFixture(t, 1)
	remote.call = func(context.Context, backend.Request) (*backend.Response, error) {
		return &backend.Response{Text: batchPolicyResponse(batchPolicyMember(candidates[0], batchPolicyRow("1", "alpha")), batchPolicyMember(candidates[1], batchPolicyRow("1", "alpha")))}, nil
	}
	failed := false
	store.onSave = func(_ context.Context, c *Candidate) error {
		if c.Index == 1 && !failed {
			failed = true
			return errors.New("temporary save failure")
		}
		return nil
	}
	if update := runBatchPolicyWorker(t, runtime, store, round, candidates); update.err != nil {
		t.Fatal(update.err)
	}
	_, _, events := store.snapshot()
	if remote.calls.Load() != 1 || !slices.Equal(events, []string{"reserve", "sent", "received", "save:0", "save:1", "save:1", "completed"}) {
		t.Fatalf("save retry re-entered model or completed early: calls=%d events=%v", remote.calls.Load(), events)
	}
	page, _, err := store.Candidates(context.Background(), 0, 10)
	if err != nil || len(page) != 2 {
		t.Fatalf("saved candidates missing: %+v %v", page, err)
	}
	for _, c := range page {
		if c.Version != 2 || c.LogicalAttempt != 1 || len(c.Alignment.Verified) != 1 {
			t.Fatalf("local save retry changed logical progress: %+v", c)
		}
	}
}

func TestAlignmentBatchPolicyCompletionWaitsForAllSavesAndCommits(t *testing.T) {
	for _, failure := range []string{"none", "second_save", "second_commit"} {
		t.Run(failure, func(t *testing.T) {
			runtime, store, candidates, remote, round := batchPolicyFixture(t, 1)
			remote.call = func(context.Context, backend.Request) (*backend.Response, error) {
				return &backend.Response{Text: batchPolicyResponse(batchPolicyMember(candidates[0], batchPolicyRow("1", "alpha"), batchPolicyRow("2", "beta")), batchPolicyMember(candidates[1], batchPolicyRow("1", "alpha"), batchPolicyRow("2", "beta")))}, nil
			}
			injected := errors.New(failure)
			capacityChecks := 0
			checkCapacity := func() error {
				capacityChecks++
				_, _, events := store.snapshot()
				capacity := runtime.Admission.Snapshot()
				if slices.Contains(events, "completed") || capacity.PendingResults != 1 || capacity.AlignmentInflight != 0 || len(runtime.parse) != 1 {
					return terminalRuntimeStoreError{fmt.Errorf("batch completion released before member persistence: %+v %v", capacity, events)}
				}
				return nil
			}
			store.onSave = func(_ context.Context, c *Candidate) error {
				if err := checkCapacity(); err != nil {
					return err
				}
				if c.Index == 1 && failure == "second_save" {
					return terminalRuntimeStoreError{injected}
				}
				return nil
			}
			store.onCommit = func(_ context.Context, c *Candidate) error {
				if err := checkCapacity(); err != nil {
					return err
				}
				if c.Index == 1 && failure == "second_commit" {
					return terminalRuntimeStoreError{injected}
				}
				return nil
			}
			result := processCandidates(context.Background(), round, candidates, store, runtime, quietLogger(), progress.Nop{})
			if failure == "none" && result.err != nil || failure != "none" && !errors.Is(result.err, injected) {
				t.Fatalf("member storage error propagation: %v", result.err)
			}
			_, _, events := store.snapshot()
			if slices.Contains(events, "completed") != (failure == "none") || remote.calls.Load() != 1 {
				t.Fatalf("storage error caused request completion or model replay: %v calls=%d", events, remote.calls.Load())
			}
			wantChecks, wantCompleted := 4, 2
			if failure == "second_save" {
				wantChecks, wantCompleted = 2, 0
			} else if failure == "second_commit" {
				wantCompleted = 1
			}
			state, err := store.Load(context.Background())
			if err != nil || capacityChecks != wantChecks || len(state.Completed) != wantCompleted {
				t.Fatalf("member confirmations changed: checks=%d state=%+v err=%v", capacityChecks, state, err)
			}
			if capacity := runtime.Admission.Snapshot(); capacity.PendingResults != 0 || capacity.AlignmentInflight != 0 || len(runtime.parse) != 0 {
				t.Fatalf("batch leaked completion capacity: %+v", capacity)
			}
			if failure != "none" {
				page, _, err := store.Candidates(context.Background(), 0, 10)
				if err != nil || len(page) == 0 {
					t.Fatalf("storage error lost recoverable candidates: %+v %v", page, err)
				}
				foundSaved := false
				for _, c := range page {
					if c.Ready && len(c.Alignment.Verified) == 2 && c.LastAlignmentRequestID != "" {
						foundSaved = true
					}
				}
				if !foundSaved {
					t.Fatal("previously saved member progress was lost")
				}
			}
		})
	}
}
