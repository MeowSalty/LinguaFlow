package pipeline

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type mainFailureStore struct {
	*MemoryRoundStore
	reserveErr, accountingErr error
	failed                    RequestRecord
}

func (s *mainFailureStore) Reserve(ctx context.Context, intent RequestIntent) error {
	if s.reserveErr != nil {
		return terminalRuntimeStoreError{s.reserveErr}
	}
	return s.MemoryRoundStore.Reserve(ctx, intent)
}

func (s *mainFailureStore) Record(ctx context.Context, id string, record RequestRecord) error {
	if record.State == "failed" {
		s.failed = record
		if s.accountingErr != nil {
			return terminalRuntimeStoreError{s.accountingErr}
		}
	}
	return s.MemoryRoundStore.Record(ctx, id, record)
}

type atomicMainFailureTestStore struct {
	*mainFailureStore
	gate *PauseGate
}

func (*atomicMainFailureTestStore) PersistsMainFailure() bool { return true }

func (*atomicMainFailureTestStore) Cursors(context.Context, []CursorTransition) error {
	return terminalRuntimeStoreError{errors.New("the request recorder already persisted the failure cursor")}
}

func (s *atomicMainFailureTestStore) Record(ctx context.Context, id string, record RequestRecord) error {
	if err := s.mainFailureStore.Record(ctx, id, record); err != nil || record.MainFailure == nil {
		return err
	}
	if err := s.MemoryRoundStore.Cursors(ctx, []CursorTransition{{Index: 0, Cursor: *record.MainFailure}}); err != nil {
		return err
	}
	s.gate.Pause()
	return nil
}

func TestStagedMainProviderFailureDispatchAndCursor(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		status, initialPool, attempt int
		maxAttempts, nextPool        int
		networkErr                   error
	}{
		{name: "fatal skips remaining pools", status: 403, maxAttempts: 3, nextPool: 4},
		{name: "exhausted retry advances without another backoff", status: 503, initialPool: 1, attempt: 1, maxAttempts: 1, nextPool: 2},
		{name: "gateway has no route to host", initialPool: 1, attempt: 1, maxAttempts: 1, nextPool: 2,
			networkErr: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("no route to host")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			store := &mainFailureStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
			defer store.Close()
			if _, err := store.Seal(ctx, []int{0}); err != nil {
				t.Fatal(err)
			}
			initial := WorkCursor{Pool: tc.initialPool, Attempt: tc.attempt, State: "pending"}
			if err := store.Cursors(ctx, []CursorTransition{{Index: 0, Cursor: initial}}); err != nil {
				t.Fatal(err)
			}
			upstream := tc.networkErr
			wantLedgerError := "dial tcp: no route to host"
			if upstream == nil {
				upstream = &backend.StatusError{StatusCode: tc.status, Err: errors.New(strings.Repeat("界", 705))}
				wantLedgerError = strings.Repeat("界", 682)
			}
			message := upstream.Error()
			provider := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) { return nil, upstream }}
			h := stagedTestHandler(t, r, provider)
			h.Retry.MaxAttempts = tc.maxAttempts
			doc := newTestDoc(1)
			doc.Segments[0].ID = "source-segment"
			observer := &recordingBatchObserver{}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))

			result, err := RunStagedRound(ctx, Round{Handler: h, Retry: h.Retry, Concurrency: 1}, doc, store, r, logger, observer)
			if err != nil || len(result.Unresolved) != 1 || provider.calls.Load() != 1 {
				t.Fatalf("result=%+v provider calls=%d err=%v", result, provider.calls.Load(), err)
			}
			state, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if cursor := state.Cursors[0]; cursor.Pool != tc.nextPool || cursor.State != "unresolved" || !cursor.NextAttemptAt.IsZero() {
				t.Fatalf("unexpected exhausted cursor %+v", cursor)
			}
			if record := store.failed; len(store.requests) != 1 || record.State != "failed" || record.MainFailure == nil || *record.MainFailure != state.Cursors[0] || record.Error != wantLedgerError || !utf8.ValidString(record.Error) {
				t.Fatalf("expected one classified request and a UTF-8 error prefix, got %d requests and %+v", len(store.requests), record)
			}
			for _, request := range store.requests {
				if request.record.State != "failed" || request.record.Error != wantLedgerError {
					t.Fatalf("persisted request lost its provider error: %+v", request.record)
				}
			}
			if len(observer.events) != 1 {
				t.Fatalf("expected a single failure event, got %#v", observer.events)
			}
			event := observer.events[0]
			if event.Status != "failed" || event.ErrorType != "backend_error" || event.ErrorMessage != message || event.HTTPStatus != tc.status || event.Attempt != tc.attempt || event.PoolIndex == nil || *event.PoolIndex != tc.initialPool || len(event.SegmentIDs) != 1 || event.SegmentIDs[0] != "source-segment" || event.UserMessage == "" {
				t.Fatalf("unexpected provider failure event %+v", event)
			}
			if strings.Count(logs.String(), `"err":"`+message+`"`) != 1 || !strings.Contains(logs.String(), `"segment_ids":["source-segment"]`) {
				t.Fatalf("provider failure missing from ordinary logs: %s", logs.String())
			}
			if tc.networkErr != nil && !strings.Contains(logs.String(), `"http_status":0`) {
				t.Fatalf("network failure has no HTTP status: %s", logs.String())
			}
		})
	}
}

func TestStagedMainStorageFailureKeepsActualProviderCause(t *testing.T) {
	for _, at := range []string{"reserve", "failed"} {
		t.Run(at, func(t *testing.T) {
			ctx := context.Background()
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			store := &mainFailureStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
			defer store.Close()
			saveErr := errors.New("storage write failed")
			upstream := &backend.StatusError{StatusCode: 503, Err: errors.New("upstream unavailable")}
			var wantCalls int64
			if at == "failed" {
				store.accountingErr = saveErr
				wantCalls = 1
			} else {
				store.reserveErr = saveErr
			}
			provider := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) { return nil, upstream }}
			h := stagedTestHandler(t, r, provider)
			observer := &recordingBatchObserver{}
			_, err := RunStagedRound(ctx, Round{Handler: h, Concurrency: 1}, newTestDoc(1), store, r, quietLogger(), observer)
			outer, ok := err.(*StorageError)
			if !ok || outer.Operation != at || !errors.Is(err, saveErr) || errors.Is(err, upstream) != (wantCalls > 0) {
				t.Fatalf("unexpected operation and error chain: %T %v", err, err)
			}
			if provider.calls.Load() != wantCalls || int64(len(observer.events)) != wantCalls {
				t.Fatalf("calls=%d events=%#v", provider.calls.Load(), observer.events)
			}
			if wantCalls > 0 && observer.events[0].HTTPStatus != 503 {
				t.Fatalf("actual provider status missing: %+v", observer.events[0])
			}
		})
	}
}

func TestStagedMainSamePoolRetryUsesRecordedCursor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	r.Gate = NewPauseGate()
	memory := &mainFailureStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
	store := &atomicMainFailureTestStore{mainFailureStore: memory, gate: r.Gate}
	defer store.Close()
	const retryAfter = 30 * time.Second
	upstream := &backend.StatusError{StatusCode: 429, Err: errors.New("rate limited"), RetryAfter: retryAfter}
	provider := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) { return nil, upstream }}
	h := stagedTestHandler(t, r, provider)
	h.Retry.MaxAttempts = 1
	observer := &recordingBatchObserver{}
	started := time.Now().UTC().Truncate(time.Microsecond)
	result, err := RunStagedRound(ctx, Round{Handler: h, Retry: h.Retry, Concurrency: 1}, newTestDoc(1), store, r, quietLogger(), observer)
	if err != nil || !r.Gate.Paused() || len(result.Unresolved) != 1 || provider.calls.Load() != 1 || len(observer.events) != 1 {
		t.Fatalf("paused after its only atomic failure: result=%+v, calls=%d, events=%d, err=%v", result, provider.calls.Load(), len(observer.events), err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := state.Cursors[0]
	if memory.failed.MainFailure == nil || *memory.failed.MainFailure != got || got.Pool != 0 || got.Attempt != 1 || got.State != "pending" || got.NextAttemptAt.Before(started.Add(retryAfter)) || got.NextAttemptAt.Nanosecond()%1000 != 0 {
		t.Fatalf("retry cursor does not match the provider failure record or its Retry-After: cursor=%+v record=%+v", got, memory.failed.MainFailure)
	}
}

func TestStagedMainPreDispatchPauseRetainsCursor(t *testing.T) {
	ctx := context.Background()
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	r.Gate = NewPauseGate()
	store := &mainFailureStore{MemoryRoundStore: NewMemoryRoundStore(nil), reserveErr: backend.ErrDispatchPaused}
	defer store.Close()
	if _, err := store.Seal(ctx, []int{0}); err != nil {
		t.Fatal(err)
	}
	initial := WorkCursor{Pool: 1, Attempt: 1, State: "pending"}
	if err := store.Cursors(ctx, []CursorTransition{{Index: 0, Cursor: initial}}); err != nil {
		t.Fatal(err)
	}
	provider := &runtimeTestBackend{}
	h := stagedTestHandler(t, r, provider)
	h.Retry.MaxAttempts = 1
	observer := &recordingBatchObserver{}
	if _, err := RunStagedRound(ctx, Round{Handler: h, Retry: h.Retry, Concurrency: 1}, newTestDoc(1), store, r, quietLogger(), observer); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Cursors[0] != initial || !r.Gate.Paused() || provider.calls.Load() != 0 || len(observer.events) != 0 || len(store.requests) != 0 {
		t.Fatalf("pre-dispatch pause changed cursor or produced work: cursor=%+v, calls=%d, events=%v, ledger=%d", state.Cursors[0], provider.calls.Load(), observer.events, len(store.requests))
	}
}

func TestStagedMainProviderContextCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stopRound bool
	}{
		{name: "provider cancellation with a live round"},
		{name: "caller cancellation", stopRound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			store := &mainFailureStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
			defer store.Close()
			provider := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
				if tc.stopRound {
					cancel()
				}
				return nil, context.Canceled
			}}
			h := stagedTestHandler(t, r, provider)
			observer := &recordingBatchObserver{}
			result, err := RunStagedRound(ctx, Round{Handler: h, Concurrency: 1}, newTestDoc(1), store, r, quietLogger(), observer)
			if provider.calls.Load() != 1 || len(store.requests) != 1 || store.failed.State != "failed" {
				t.Fatalf("unexpected dispatched calls or facts: calls=%d requests=%d record=%+v", provider.calls.Load(), len(store.requests), store.failed)
			}
			if tc.stopRound {
				if !errors.Is(err, context.Canceled) || len(observer.events) != 0 || store.failed.MainFailure != nil {
					t.Fatalf("caller cancellation became a provider failure: err=%v events=%#v", err, observer.events)
				}
			} else if err != nil || len(result.Unresolved) != 1 || store.failed.MainFailure == nil || store.failed.MainFailure.State != "unresolved" || len(observer.events) != 1 || observer.events[0].ErrorMessage != context.Canceled.Error() {
				t.Fatalf("provider cancellation did not resolve its dispatch: result=%+v err=%v events=%#v", result, err, observer.events)
			}
		})
	}
}

func TestStagedReviseTemplateFailurePreservesPriorTranslation(t *testing.T) {
	ctx := context.Background()
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	renderer, err := prompt.NewReviseRenderer(`{{.UnknownField}}`)
	if err != nil {
		t.Fatal(err)
	}
	provider := &runtimeTestBackend{}
	h := &ReviseHandler{Backend: BindRequestBackend(provider, 1, 0, r), Renderer: renderer, IssueCodes: qa.SemanticQACodes(), BatchSize: 1}
	h.Retry.MaxAttempts = 2
	doc := reviseDoc()
	doc.Segments = doc.Segments[:1]
	observer := &recordingBatchObserver{}
	result, err := RunStagedRound(ctx, Round{Handler: h, Retry: h.Retry, Concurrency: 1}, doc, store, r, quietLogger(), observer)
	if err != nil || result.FailedBatches != 1 || len(result.FailedSegments) != 1 || doc.Segments[0].Target != "你好" {
		t.Fatalf("deterministic build failure result=%+v target=%q err=%v", result, doc.Segments[0].Target, err)
	}
	if provider.calls.Load() != 0 || len(store.requests) != 0 || len(observer.events) != 1 || observer.events[0].ErrorType != "render_error" {
		t.Fatalf("failed render dispatched or emitted wrong event: calls=%d requests=%d events=%#v", provider.calls.Load(), len(store.requests), observer.events)
	}
}
