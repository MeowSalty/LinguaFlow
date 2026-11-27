package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

type parseRetryBackend struct {
	results  []string
	errors   map[int]error
	requests []backend.Request
}

func (*parseRetryBackend) Name() string { return "parse-retry" }
func (*parseRetryBackend) Close() error { return nil }
func (b *parseRetryBackend) Translate(_ context.Context, req backend.Request) (*backend.Response, error) {
	b.requests = append(b.requests, req)
	if err := b.errors[len(b.requests)-1]; err != nil {
		return nil, err
	}
	if len(b.requests) > len(b.results) {
		return nil, errors.New("fixture invocation exceeded")
	}
	return &backend.Response{Text: b.results[len(b.requests)-1], Usage: backend.Usage{PromptTokens: 7, CompletionTokens: 3}}, nil
}

func TestStagedPromptUpgradeProviderFailure(t *testing.T) {
	for _, tc := range []struct{ status, maxAttempts, resolved, calls int }{{500, 0, 0, 2}, {401, 1, 1, 3}, {403, 1, 1, 3}} {
		t.Run(fmt.Sprintf("status_%d", tc.status), func(t *testing.T) {
			runtime := stagedTestRuntime(t, DefaultCandidateLimits())
			providerErr := errors.New("upgrade provider refused request")
			provider := &parseRetryBackend{
				results: []string{"broken JSON", "", `{"translations":{"1":"fallback"}}`},
				errors:  map[int]error{1: &backend.StatusError{StatusCode: tc.status, Err: providerErr}},
			}
			store := NewMemoryRoundStore(nil)
			t.Cleanup(func() { _ = store.Close() })
			h := stagedTestHandler(t, runtime, provider)
			h.Repair.PromptUpgrade, h.RetryReminderTemplate = true, repair.DefaultRetryReminderTemplate
			h.Retry = backend.RetryPolicy{MaxAttempts: tc.maxAttempts}
			doc := newTestDoc(1)
			defer runtime.CloseDocument(doc)
			result, err := RunStagedRound(t.Context(), Round{Handler: h, Retry: h.Retry, Concurrency: 1}, doc, store, runtime, quietLogger(), nil)
			requests := parseRetryRequests(store)
			if err != nil || len(provider.requests) != tc.calls || len(requests) != tc.calls || len(result.Resolved) != tc.resolved {
				t.Fatalf("calls=%d ledger=%d result=%+v err=%v", len(provider.requests), len(requests), result, err)
			}
			failed := requests[1]
			if failed.record.State != "failed" || failed.record.Error != providerErr.Error() || failed.record.UsageKnown || !failed.response.PromptUpgrade {
				t.Fatalf("upgrade did not retain the provider failure: %+v", failed)
			}
			if tc.calls == 3 && (requests[2].response.Pool != 1 || requests[2].response.MainAttempt != 1 || requests[2].response.PromptUpgrade) {
				t.Fatalf("fallback invocation did not start its next pool: %+v", requests[2].response)
			}
		})
	}
}

func parseRetryRequests(store *MemoryRoundStore) []*memoryResponse {
	requests := make([]*memoryResponse, 0, len(store.requests))
	for _, request := range store.requests {
		requests = append(requests, request)
	}
	slices.SortFunc(requests, func(a, b *memoryResponse) int { return a.response.DatabaseID - b.response.DatabaseID })
	return requests
}

func TestStagedPromptUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		enabled    bool
		results    []string
		resolved   int
	}{
		{name: "disabled", results: []string{"broken JSON"}},
		{name: "recovered JSON", enabled: true, results: []string{"broken JSON", `{"translations":{"1":"good"}}`}, resolved: 1},
		{name: "recovered text", mode: "text", enabled: true, results: []string{"broken text", "[1] good"}, resolved: 1},
		{name: "empty", enabled: true, results: []string{"", `{"translations":{"1":"good"}}`}, resolved: 1},
		{name: "upgrade failed", enabled: true, results: []string{"broken JSON", "still broken"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := stagedTestRuntime(t, DefaultCandidateLimits())
			provider := &parseRetryBackend{results: tc.results}
			store := NewMemoryRoundStore(nil)
			t.Cleanup(func() { _ = store.Close() })
			h := stagedTestHandler(t, runtime, provider)
			h.Repair.PromptUpgrade, h.RetryReminderTemplate, h.ResponseMode = tc.enabled, repair.DefaultRetryReminderTemplate, tc.mode
			doc := newTestDoc(1)
			defer runtime.CloseDocument(doc)
			result, err := RunStagedRound(t.Context(), Round{Handler: h, Concurrency: 1}, doc, store, runtime, quietLogger(), nil)
			if err != nil || len(result.Resolved) != tc.resolved || len(provider.requests) != len(tc.results) {
				t.Fatalf("calls=%d, result=%+v, err=%v", len(provider.requests), result, err)
			}
			requests := parseRetryRequests(store)
			if len(requests) != len(tc.results) || doc.InputTokens != int64(7*len(requests)) || doc.OutputTokens != int64(3*len(requests)) {
				t.Fatalf("ledger=%d usage=%d/%d", len(requests), doc.InputTokens, doc.OutputTokens)
			}
			for i, request := range requests {
				if request.record.State != "completed" || request.response.MainAttempt != 1 || request.response.Pool != 0 || request.response.PromptUpgrade != (i == 1) {
					t.Fatalf("invocation %d: %+v state=%s", i, request.response, request.record.State)
				}
			}
			if tc.enabled {
				first, second := provider.requests[0], provider.requests[1]
				if first.User != second.User || !strings.HasPrefix(second.System, first.System) || !strings.Contains(second.System, "previous response could not be processed") || !strings.Contains(second.System, tc.results[0]) {
					t.Fatalf("upgrade did not replay the batch with a reminder: %+v", provider.requests)
				}
			}
		})
	}
}

type parseRetryArchiveFaultStore struct {
	*MemoryRoundStore
	upgrade bool
	fault   bool
}

func (s *parseRetryArchiveFaultStore) PublishOutputs(ctx context.Context, in OutputPublication) (MainResponseHeader, error) {
	if s.fault {
		s.mu.Lock()
		upgrade := s.requests[in.Key.RequestID].response.PromptUpgrade
		s.mu.Unlock()
		if upgrade == s.upgrade {
			return MainResponseHeader{}, terminalRuntimeStoreError{errors.New("restart after capture")}
		}
	}
	return s.MemoryRoundStore.PublishOutputs(ctx, in)
}

func TestStagedPromptUpgradeRecovery(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(fmt.Sprintf("capture_upgrade=%v", upgrade), func(t *testing.T) {
			store := &parseRetryArchiveFaultStore{MemoryRoundStore: NewMemoryRoundStore(nil), upgrade: upgrade, fault: true}
			t.Cleanup(func() { _ = store.Close() })
			provider := &parseRetryBackend{results: []string{"broken initial JSON", "broken upgraded JSON"}}
			execute := func(runtime *ExecutionRuntime) (RunRoundResult, error) {
				h := stagedTestHandler(t, runtime, provider)
				h.Repair.PromptUpgrade, h.RetryReminderTemplate = true, repair.DefaultRetryReminderTemplate
				doc := newTestDoc(1)
				defer runtime.CloseDocument(doc)
				return RunStagedRound(t.Context(), Round{Handler: h, Concurrency: 1}, doc, store, runtime, quietLogger(), nil)
			}
			first := stagedTestRuntime(t, DefaultCandidateLimits())
			if _, err := execute(first); !storageFailure(err) {
				t.Fatalf("expected archive fault after durable capture, got %v", err)
			}
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			store.fault = false
			result, err := execute(stagedTestRuntime(t, DefaultCandidateLimits()))
			if err != nil || len(provider.requests) != 2 || len(result.Unresolved) != 1 {
				t.Fatalf("recovery calls=%d result=%+v err=%v", len(provider.requests), result, err)
			}
		})
	}
}

func TestStagedPromptUpgradeResumesLegacyArchiveWithSplitBatch(t *testing.T) {
	store := NewMemoryRoundStore(nil)
	t.Cleanup(func() { _ = store.Close() })
	doc := newTestDoc(2)
	if _, err := store.Seal(t.Context(), []int{0, 1}); err != nil {
		t.Fatal(err)
	}
	legacy := []frozenMainMember{
		{Index: 0, PromptID: "1", CandidateID: NewWorkID(), Segment: doc.Segments[0]},
		{Index: 1, PromptID: "2", CandidateID: NewWorkID(), Segment: doc.Segments[1]},
	}
	header := memberFixtureReserve(t, t.Context(), store, NewWorkID(), 0, legacy)
	store.requests[header.ID].response.MainAttempt = 1
	memberFixtureOutputs(t, t.Context(), store, header, []archivedOutput{{Failure: invalidModelResponse}, {Failure: invalidModelResponse}}, nil)
	limits := DefaultCandidateLimits()
	limits.Segments = 1
	runtime := stagedTestRuntime(t, limits)
	defer runtime.CloseDocument(doc)
	provider := &parseRetryBackend{results: []string{`{"translations":{"1":"first"}}`, `{"translations":{"1":"second"}}`}}
	h := stagedTestHandler(t, runtime, provider)
	h.Repair.PromptUpgrade, h.RetryReminderTemplate = true, repair.DefaultRetryReminderTemplate
	result, err := RunStagedRound(t.Context(), Round{Handler: h, Concurrency: 1}, doc, store, runtime, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 2 || len(provider.requests) != 2 || doc.Segments[0].Target != "first" || doc.Segments[1].Target != "second" {
		t.Fatalf("calls=%d result=%+v target=%q/%q err=%v", len(provider.requests), result, doc.Segments[0].Target, doc.Segments[1].Target, err)
	}
	for _, req := range provider.requests {
		if !strings.Contains(req.System, invalidModelResponse) || strings.Contains(req.System, "previous response started with") {
			t.Fatal("legacy archive did not use its available diagnostic")
		}
	}
	for _, request := range parseRetryRequests(store)[1:] {
		if request.response.MemberCount != 1 || request.response.MainAttempt != 1 || !request.response.PromptUpgrade {
			t.Fatalf("split upgrade ledger: %+v", request.response)
		}
	}
}

func TestStagedReviseParseRetry(t *testing.T) {
	for _, tc := range []struct {
		name, first            string
		pool, attempt, members int
	}{
		{name: "whole batch parse failure", first: `{"revisions":false}`, pool: 0, attempt: 2, members: 2},
		{name: "parsed with one missing", first: `{"revisions":[{"id":"0","target":"first"}]}`, pool: 1, attempt: 1, members: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := stagedTestRuntime(t, DefaultCandidateLimits())
			provider := &parseRetryBackend{results: []string{tc.first, `{"revisions":[{"id":"0","target":"first"},{"id":"1","target":"second"}]}`}}
			store := NewMemoryRoundStore(nil)
			t.Cleanup(func() { _ = store.Close() })
			retry := backend.RetryPolicy{MaxAttempts: 1}
			h := &ReviseHandler{Backend: BindRequestBackend(provider, 1, 0, runtime), Renderer: newReviseRenderer(t), IssueCodes: qa.SemanticQACodes(), BatchSize: 2, Retry: retry}
			doc := reviseDoc()
			doc.Segments = doc.Segments[:2]
			doc.Segments[1].Issues[0].Disposition = qa.DispositionPending
			defer runtime.CloseDocument(doc)
			result, err := RunStagedRound(t.Context(), Round{Handler: h, Retry: retry, Concurrency: 1}, doc, store, runtime, quietLogger(), nil)
			requests := parseRetryRequests(store)
			if err != nil || len(result.Resolved) != 2 || len(requests) != 2 {
				t.Fatalf("ledger=%d result=%+v err=%v", len(requests), result, err)
			}
			second := requests[1].response
			if second.Pool != tc.pool || second.MainAttempt != tc.attempt || second.MemberCount != tc.members {
				t.Fatalf("second ledger header=%+v, want pool=%d attempt=%d members=%d", second, tc.pool, tc.attempt, tc.members)
			}
		})
	}
}

func TestMainSchedulerKeepsIndependentParseRetryBatches(t *testing.T) {
	runtime := stagedTestRuntime(t, DefaultCandidateLimits())
	h := stagedTestHandler(t, runtime, &parseRetryBackend{})
	doc := newTestDoc(4)
	for i, phase := range []string{"parse_retry:parent-a", "parse_retry:parent-b", "parse_retry:parent-a", "initial"} {
		doc.Segments[i].RoundWork.Member = true
		doc.Segments[i].RoundWork.Cursor = WorkCursor{Pool: 0, Attempt: 1, Phase: phase, State: "pending"}
	}
	scheduler, err := newMainScheduler(t.Context(), doc, h, runtime, 1, "retry-group-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.close()
	a, b, c, d := scheduler.nodes[0].group, scheduler.nodes[1].group, scheduler.nodes[2].group, scheduler.nodes[3].group
	if a != c || a == b || a == d || b == d {
		t.Fatalf("unexpected logical batch grouping: %d %d %d %d", a, b, c, d)
	}
}
