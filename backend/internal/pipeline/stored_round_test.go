package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type nativeStoredRoundTestStore struct {
	*MemoryRoundStore
	intent    RequestIntent
	sent      []RequestIntent
	received  RequestRecord
	fail      string
	onFailure func(WorkCursor)
}

func (*nativeStoredRoundTestStore) ResourceIdentity() int { return 73 }
func (*nativeStoredRoundTestStore) RoundIdentity() int    { return 4 }

func (s *nativeStoredRoundTestStore) Reserve(ctx context.Context, intent RequestIntent) error {
	if s.fail == "paused" {
		return terminalRuntimeStoreError{backend.ErrDispatchPaused}
	}
	if s.fail == "reserve" {
		return terminalRuntimeStoreError{errors.New("reservation unavailable")}
	}
	if err := s.MemoryRoundStore.Reserve(ctx, intent); err != nil {
		return err
	}
	s.intent = RequestIntent{ID: intent.ID, InputDigest: intent.InputDigest, ResourceID: intent.ResourceID,
		RoundIndex: intent.RoundIndex, BackendID: intent.BackendID, Stage: intent.Stage, Phase: intent.Phase,
		Pool: intent.Pool, Attempt: intent.Attempt, Indices: append([]int(nil), intent.Indices...)}
	s.sent = append(s.sent, s.intent)
	return nil
}

func (s *nativeStoredRoundTestStore) Record(ctx context.Context, id string, record RequestRecord) error {
	if s.fail == "record" && record.State != "sent" {
		return terminalRuntimeStoreError{errors.New("accounting unavailable")}
	}
	if err := s.MemoryRoundStore.Record(ctx, id, record); err != nil {
		return err
	}
	if s.onFailure != nil && record.MainFailure != nil {
		s.onFailure(*record.MainFailure)
	}
	return nil
}

func (s *nativeStoredRoundTestStore) CaptureResponse(ctx context.Context, in CapturedResponse) (MainResponseHeader, error) {
	if s.fail == "record" {
		return MainResponseHeader{}, terminalRuntimeStoreError{errors.New("response capture unavailable")}
	}
	header, err := s.MemoryRoundStore.CaptureResponse(ctx, in)
	if err == nil {
		s.received = in.Record
	}
	return header, err
}

func storedTestExecutionRuntime(t *testing.T) *ExecutionRuntime {
	t.Helper()
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, AlignmentConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	return runtime
}

func awaitStoredRoundSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("stored round did not reach the expected barrier")
	}
}

func newSuccessfulStoredQAProvider() *runtimeTestBackend {
	return &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"issues":[]}`}, nil
	}}
}

func newNativeStoredQA(t *testing.T, runtime *ExecutionRuntime, provider backend.Backend, retry backend.RetryPolicy) *SemanticQAHandler {
	t.Helper()
	return &SemanticQAHandler{Backend: BindRequestBackend(provider, 9, 0, runtime), Renderer: newSemanticQARenderer(t), SegmentScope: "all", Retry: retry}
}

func (s *nativeStoredRoundTestStore) SealCanonical(ctx context.Context, doc *Document) error {
	if s.fail == "seal" {
		return terminalRuntimeStoreError{errors.New("seal unavailable")}
	}
	return s.MemoryRoundStore.SealCanonical(ctx, doc)
}

func (s *nativeStoredRoundTestStore) SettleStoredResponse(ctx context.Context, doc *Document, in StoredResponseSettlement) error {
	if s.fail == "settle" {
		return terminalRuntimeStoreError{errors.New("settlement unavailable")}
	}
	if err := s.MemoryRoundStore.SettleStoredResponse(ctx, doc, in); err != nil {
		return err
	}
	if s.fail == "settled_ack" {
		return terminalRuntimeStoreError{errors.New("settlement acknowledgement interrupted")}
	}
	return nil
}

func TestStoredRoundStorageFailuresDoNotRetryModel(t *testing.T) {
	for _, tc := range []struct {
		fail  string
		calls int64
	}{{"seal", 0}, {"reserve", 0}, {"record", 1}, {"settle", 1}} {
		t.Run(tc.fail, func(t *testing.T) {
			runtime := storedTestExecutionRuntime(t)
			store := &nativeStoredRoundTestStore{MemoryRoundStore: NewMemoryRoundStore(nil), fail: tc.fail}
			defer store.Close()
			provider := newSuccessfulStoredQAProvider()
			retry := backend.RetryPolicy{MaxAttempts: 2}
			h := newNativeStoredQA(t, runtime, provider, retry)
			result, err := RunRound(t.Context(), Round{Runtime: runtime, Store: store, Handler: h, Retry: retry}, semanticQADoc([]string{"translated"}, nil), nil, quietLogger(), nil)
			var storage *StorageError
			if !errors.As(err, &storage) || provider.calls.Load() != tc.calls || len(result.Resolved) != 0 {
				t.Fatalf("result=%+v, err=%v, calls=%d", result, err, provider.calls.Load())
			}
			if tc.fail == "settle" {
				header, err := store.ResponseHeader(t.Context(), store.intent.ID)
				if err != nil || header.Phase != responsecontract.CapturePending || header.CaptureBytes == 0 {
					t.Fatalf("failed settlement did not preserve capture: %+v, err=%v", header, err)
				}
			}
		})
	}
}

func TestStoredRoundCallbackFailurePreservesArchiveAndRecovers(t *testing.T) {
	runtime := storedTestExecutionRuntime(t)
	store := &nativeStoredRoundTestStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
	defer store.Close()
	provider := newSuccessfulStoredQAProvider()
	retry := backend.RetryPolicy{MaxAttempts: 2}
	h := newNativeStoredQA(t, runtime, provider, retry)
	doc := semanticQADoc([]string{"translated"}, nil)
	ctx := t.Context()
	failure := errors.New("callback interrupted publication")
	callbackCalls := 0
	result, err := RunRound(ctx, Round{Runtime: runtime, Store: store, Handler: h, Retry: retry}, doc, func(context.Context, BatchResult) error {
		callbackCalls++
		doc.Segments[0].Issues = []qa.QualityIssue{{Code: qa.IssueCodeGrammar}}
		return terminalRuntimeStoreError{failure}
	}, quietLogger(), nil)
	if !errors.Is(err, failure) || len(result.Resolved) != 0 || callbackCalls != 1 || provider.calls.Load() != 1 || len(doc.Segments[0].Issues) != 0 {
		t.Fatalf("interrupted callback: result=%+v err=%v callbacks=%d issues=%v", result, err, callbackCalls, doc.Segments[0].Issues)
	}
	header, err := store.ResponseHeader(ctx, store.intent.ID)
	if err != nil || header.Phase != responsecontract.CapturePending || header.CaptureBytes == 0 {
		t.Fatalf("callback failure lost captured response: %+v, err=%v", header, err)
	}
	resumed := storedTestExecutionRuntime(t)
	h = newNativeStoredQA(t, resumed, provider, retry)
	result, err = RunRound(ctx, Round{Runtime: resumed, Store: store, Handler: h, Retry: retry}, semanticQADoc([]string{"translated"}, nil), nil, quietLogger(), nil)
	if err != nil || !reflect.DeepEqual(result.Resolved, []int{0}) || provider.calls.Load() != 1 {
		t.Fatalf("callback recovery: result=%+v err=%v calls=%d", result, err, provider.calls.Load())
	}
	header, err = store.ResponseHeader(ctx, store.intent.ID)
	if err != nil || header.Phase != responsecontract.Promoted || header.CaptureBytes != 0 {
		t.Fatalf("recovered capture not settled: %+v, err=%v", header, err)
	}
}

func TestStoredRoundRecoversAttemptsAndTerminalMembers(t *testing.T) {
	runtime := storedTestExecutionRuntime(t)
	store := &nativeStoredRoundTestStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
	defer store.Close()
	store.state = RoundRecovery{Sealed: true, Members: []int{0, 1, 2, 3, 4}, Completed: []int{4}, Cursors: map[int]WorkCursor{
		0: {Pool: 1, Attempt: 1, State: "pending"},
		1: {Pool: 0, Attempt: 2, State: "pending"},
		2: {Pool: 2, State: "unresolved"},
		3: {Pool: 2, State: "unresolved", Phase: "terminal_failure"},
		4: {State: "resolved"},
	}}
	provider := newSuccessfulStoredQAProvider()
	retry := backend.RetryPolicy{MaxAttempts: 1}
	h := newNativeStoredQA(t, runtime, provider, retry)
	h.BatchSize = 1
	doc := semanticQADoc([]string{"translated", "translated", "translated", "translated", "translated"}, nil)
	result, err := RunRound(t.Context(), Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1, Retry: retry}, doc, nil, quietLogger(), nil)
	if err != nil || !reflect.DeepEqual(result.Resolved, []int{0, 1, 4}) || !reflect.DeepEqual(result.Unresolved, []int{2}) || !reflect.DeepEqual(result.FailedSegments, []int{3}) || provider.calls.Load() != 2 {
		t.Fatalf("recovered round = %+v, calls=%d, err=%v", result, provider.calls.Load(), err)
	}
	if got := store.sent; len(got) != 2 || got[0].Pool != 1 || got[0].Attempt != 0 || !reflect.DeepEqual(got[0].Indices, []int{1}) || got[1].Pool != 1 || got[1].Attempt != 1 || !reflect.DeepEqual(got[1].Indices, []int{0}) {
		t.Fatalf("recovery did not preserve retry budgets: %+v", got)
	}
}

func TestStoredRoundRecoveredSettlementDoesNotReissueAcceptedWork(t *testing.T) {
	runtime := storedTestExecutionRuntime(t)
	store := &nativeStoredRoundTestStore{MemoryRoundStore: NewMemoryRoundStore(nil), fail: "settled_ack"}
	defer store.Close()
	provider := newSuccessfulStoredQAProvider()
	ctx := t.Context()
	h := newNativeStoredQA(t, runtime, provider, backend.RetryPolicy{})
	var storage *StorageError
	_, err := RunRound(ctx, Round{Runtime: runtime, Store: store, Handler: h}, semanticQADoc([]string{"translated"}, nil), nil, quietLogger(), nil)
	if !errors.As(err, &storage) || provider.calls.Load() != 1 {
		t.Fatalf("interrupted acknowledgement: err=%v calls=%d", err, provider.calls.Load())
	}
	header, err := store.ResponseHeader(ctx, store.intent.ID)
	if err != nil || header.Phase != responsecontract.Promoted {
		t.Fatalf("accepted archive state: %+v, err=%v", header, err)
	}
	resumed := storedTestExecutionRuntime(t)
	h = newNativeStoredQA(t, resumed, provider, backend.RetryPolicy{})
	result, err := RunRound(ctx, Round{Runtime: resumed, Store: store, Handler: h}, semanticQADoc([]string{"translated"}, nil), nil, quietLogger(), nil)
	if err != nil || !reflect.DeepEqual(result.Resolved, []int{0}) || provider.calls.Load() != 1 {
		t.Fatalf("settled recovery: result=%+v err=%v calls=%d", result, err, provider.calls.Load())
	}
}

func TestStoredRoundPauseWhileConfirmingLeavesLaterMembersPending(t *testing.T) {
	runtime := storedTestExecutionRuntime(t)
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	provider := newSuccessfulStoredQAProvider()
	h := newNativeStoredQA(t, runtime, provider, backend.RetryPolicy{MaxAttempts: 1})
	h.BatchSize = 1
	reporter := &countingReporter{}
	ctx := t.Context()
	result, err := RunRound(ctx, Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1, Retry: h.Retry}, semanticQADoc([]string{"translated", "translated"}, nil), func(context.Context, BatchResult) error {
		runtime.Gate.Pause()
		return nil
	}, quietLogger(), reporter)
	if err != nil || !reflect.DeepEqual(result.Resolved, []int{0}) || !reflect.DeepEqual(result.Unresolved, []int{1}) || provider.calls.Load() != 1 {
		t.Fatalf("paused confirmation: result=%+v err=%v calls=%d", result, err, provider.calls.Load())
	}
	state, err := store.Load(ctx)
	if err != nil || !reflect.DeepEqual(state.Completed, []int{0}) || state.Cursors[1] != (WorkCursor{}) || reporter.stageStartCalls != 1 || reporter.segmentDones != 1 {
		t.Fatalf("paused state=%+v stages=%d confirmations=%d err=%v", state, reporter.stageStartCalls, reporter.segmentDones, err)
	}
}

func TestStoredRoundRequestUsesFrozenScopeAndUsage(t *testing.T) {
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{4: 1}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
	defer runtime.Close()
	store := &nativeStoredRoundTestStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
	defer store.Close()
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"issues":[]}`, Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 3}}, nil
	}}
	h := &SemanticQAHandler{Backend: BindRequestBackend(inner, 9, 4, runtime), Renderer: newSemanticQARenderer(t), SegmentScope: "all", RoundIndex: 4}
	result, err := RunRound(context.Background(), Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1}, semanticQADoc([]string{"translated", "translated"}, nil), nil, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 2 {
		t.Fatalf("semantic QA result=%+v err=%v", result, err)
	}
	intent := store.intent
	if intent.ID == "" || intent.InputDigest == "" || intent.ResourceID != 73 || intent.RoundIndex != 4 || intent.BackendID != 9 || intent.Stage != backend.RequestStageMain || intent.Phase != "main" || !reflect.DeepEqual(intent.Indices, []int{0, 1}) {
		t.Fatalf("request identity or scope lost: %+v", intent)
	}
	header, err := store.ResponseHeader(t.Context(), intent.ID)
	if err != nil || header.Phase != responsecontract.Promoted || header.MemberCount != 2 || header.Cursor != 2 {
		t.Fatalf("captured response was not settled: header=%+v err=%v", header, err)
	}
	received := store.received
	if received.State != "received" || !received.UsageKnown || received.Usage.PromptTokens != 17 || received.Usage.CompletionTokens != 3 {
		t.Fatalf("request accounting=%+v", received)
	}
}

func TestStoredRoundPauseRetainsRetryDeadline(t *testing.T) {
	runtime := storedTestExecutionRuntime(t)
	memory := NewMemoryRoundStore(nil)
	defer memory.Close()
	store := &nativeStoredRoundTestStore{MemoryRoundStore: memory, onFailure: func(cursor WorkCursor) {
		if cursor.Attempt == 1 {
			runtime.Gate.Pause()
		}
	}}
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return nil, &backend.StatusError{StatusCode: 429, RetryAfter: time.Hour, Err: errors.New("rate limited")}
	}}
	h := &SemanticQAHandler{Backend: BindRequestBackend(inner, 9, 0, runtime), Renderer: newSemanticQARenderer(t), SegmentScope: "all", Retry: backend.RetryPolicy{MaxAttempts: 1}}
	start := time.Now().UTC().Truncate(time.Microsecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := RunRound(ctx, Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1, Retry: h.Retry}, semanticQADoc([]string{"translated"}, nil), nil, quietLogger(), nil)
	if err != nil || !reflect.DeepEqual(result.Unresolved, []int{0}) || len(result.Resolved) != 0 {
		t.Fatalf("pause while backing off: %+v %v", result, err)
	}
	state, _ := memory.Load(context.Background())
	cursor := state.Cursors[0]
	if cursor.Pool != 0 || cursor.Attempt != 1 || cursor.NextAttemptAt.Before(start.Add(time.Hour)) || inner.calls.Load() != 1 {
		t.Fatalf("pause reset retry state: %+v, calls=%d", cursor, inner.calls.Load())
	}
	if runtime.Admission.Snapshot().PendingResults != 0 || runtime.Gate.Inflight() != 0 {
		t.Fatal("backoff retained request resources")
	}
}

func TestStoredSemanticQAPoolEventsFollowActualPools(t *testing.T) {
	var calls atomic.Int64
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		switch calls.Add(1) {
		case 1:
			return nil, &backend.StatusError{StatusCode: 401, Err: errors.New("unauthorized")}
		case 2:
			return nil, &backend.StatusError{StatusCode: 400, Err: errors.New("invalid input")}
		default:
			return &backend.Response{Text: `{"issues":[]}`}, nil
		}
	}}
	rep := &recordingPoolObserver{}
	retry := backend.RetryPolicy{MaxAttempts: 1}
	runtime := storedTestExecutionRuntime(t)
	h := &SemanticQAHandler{Backend: BindRequestBackend(inner, 9, 0, runtime), Renderer: newSemanticQARenderer(t), SegmentScope: "all", BatchSize: 1, Retry: retry, Reporter: rep}
	round := Round{Handler: h, Runtime: runtime, Concurrency: 1, Retry: retry}
	doc := semanticQADoc([]string{"translated", "translated", "translated"}, nil)
	result, err := RunRound(t.Context(), round, doc, nil, quietLogger(), rep)
	if err != nil || !reflect.DeepEqual(result.Resolved, []int{1, 2}) || !reflect.DeepEqual(result.Unresolved, []int{0}) || calls.Load() != 4 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
	want := []progress.PoolEvent{
		{Mode: RoundModeSemanticQA, PoolIndex: 0, MaxPools: 2, Batches: 3, Pending: 3, ShrinkRate: 1, Phase: "pool_start"},
		{Mode: RoundModeSemanticQA, PoolIndex: 0, MaxPools: 2, Batches: 3, Pending: 1, ShrinkRate: 1, Phase: "pool_advance"},
		{Mode: RoundModeSemanticQA, PoolIndex: 1, MaxPools: 2, Batches: 1, Pending: 1, ShrinkRate: 1, Phase: "pool_start"},
	}
	if !reflect.DeepEqual(rep.events, want) {
		t.Fatalf("pool events=%+v want=%+v", rep.events, want)
	}
	if len(rep.batches) != 4 || rep.batches[0].HTTPStatus != 401 || rep.batches[1].HTTPStatus != 400 {
		t.Fatalf("batch events=%+v", rep.batches)
	}
}

func TestStoredSemanticQAPauseBeforeDispatchLeavesAttemptPending(t *testing.T) {
	runtime := storedTestExecutionRuntime(t)
	store := &nativeStoredRoundTestStore{MemoryRoundStore: NewMemoryRoundStore(nil), fail: "paused"}
	defer store.Close()
	inner := &runtimeTestBackend{}
	rep := &recordingPoolObserver{}
	h := &SemanticQAHandler{Backend: BindRequestBackend(inner, 9, 0, runtime), Renderer: newSemanticQARenderer(t), SegmentScope: "all", Reporter: rep}
	result, err := RunRound(t.Context(), Round{Handler: h, Runtime: runtime, Store: store}, semanticQADoc([]string{"translated"}, nil), nil, quietLogger(), rep)
	if err != nil || !reflect.DeepEqual(result.Unresolved, []int{0}) || inner.calls.Load() != 0 || !runtime.Gate.Paused() || len(rep.batches) != 0 {
		t.Fatalf("paused result=%+v calls=%d events=%+v err=%v", result, inner.calls.Load(), rep.batches, err)
	}
	state, err := store.Load(t.Context())
	if err != nil || state.Cursors[0] != (WorkCursor{}) {
		t.Fatalf("undispatched work consumed retry budget: state=%+v err=%v", state, err)
	}
}

func TestStoredSemanticQACancellationDoesNotAcknowledgeBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runtime := storedTestExecutionRuntime(t)
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		cancel()
		return nil, context.Canceled
	}}
	rep := &recordingPoolObserver{}
	h := &SemanticQAHandler{Backend: BindRequestBackend(inner, 9, 0, runtime), Renderer: newSemanticQARenderer(t), SegmentScope: "all", Reporter: rep}
	callbacks := 0
	result, err := RunRound(ctx, Round{Handler: h, Runtime: runtime}, semanticQADoc([]string{"translated"}, nil), func(context.Context, BatchResult) error {
		callbacks++
		return nil
	}, quietLogger(), rep)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result.Unresolved, []int{0}) || len(result.Resolved) != 0 || len(result.FailedSegments) != 0 || result.FailedBatches != 0 || inner.calls.Load() != 1 || callbacks != 0 || len(rep.batches) != 0 {
		t.Fatalf("cancelled result=%+v calls=%d callbacks=%d events=%+v err=%v", result, inner.calls.Load(), callbacks, rep.batches, err)
	}
}

func TestStoredSemanticQARecordsOnlyProviderFailureWhenAccountingFails(t *testing.T) {
	providerErr := &backend.StatusError{StatusCode: 503, Err: errors.New("upstream unavailable")}
	for _, tc := range []struct {
		name    string
		callErr error
	}{
		{name: "provider and accounting", callErr: providerErr},
		{name: "accounting only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := storedTestExecutionRuntime(t)
			store := &nativeStoredRoundTestStore{MemoryRoundStore: NewMemoryRoundStore(nil), fail: "record"}
			defer store.Close()
			inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
				if tc.callErr != nil {
					return nil, tc.callErr
				}
				return &backend.Response{Text: `{"issues":[]}`}, nil
			}}
			rep := &recordingPoolObserver{}
			h := &SemanticQAHandler{Backend: BindRequestBackend(inner, 9, 0, runtime), Renderer: newSemanticQARenderer(t), SegmentScope: "all", Reporter: rep}
			_, err := RunRound(t.Context(), Round{Handler: h, Runtime: runtime, Store: store}, semanticQADoc([]string{"translated"}, nil), nil, quietLogger(), rep)
			var storage *StorageError
			if !errors.As(err, &storage) || inner.calls.Load() != 1 {
				t.Fatalf("calls=%d err=%v", inner.calls.Load(), err)
			}
			if tc.callErr == nil {
				if len(rep.batches) != 0 {
					t.Fatalf("accounting failure emitted provider failure: %+v", rep.batches)
				}
				return
			}
			if !errors.Is(err, providerErr) || len(rep.batches) != 1 {
				t.Fatalf("provider failure lost: events=%+v err=%v", rep.batches, err)
			}
			event := rep.batches[0]
			if event.ErrorType != "backend_error" || event.HTTPStatus != 503 || event.ErrorMessage != providerErr.Error() {
				t.Fatalf("provider failure event=%+v", event)
			}
		})
	}
}

func TestStoredSemanticQAPublishesBoundedRequestAndResponseDiagnostics(t *testing.T) {
	runtime := storedTestExecutionRuntime(t)
	reply := `{"issues":[]}` + strings.Repeat(" ", progress.MaxSSEBatchContentBytes+100)
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: reply}, nil
	}}
	rep := &recordingPoolObserver{}
	h := &SemanticQAHandler{Backend: BindRequestBackend(inner, 9, 0, runtime), Renderer: newSemanticQARenderer(t), SegmentScope: "all", Reporter: rep}
	doc := semanticQADoc([]string{"translated"}, nil)
	doc.Segments[0].Source = strings.Repeat("界", progress.MaxSSEBatchContentBytes/3+100)
	result, err := RunRound(t.Context(), Round{Handler: h, Runtime: runtime}, doc, nil, quietLogger(), rep)
	if err != nil || !reflect.DeepEqual(result.Resolved, []int{0}) || len(rep.batches) != 1 {
		t.Fatalf("result=%+v diagnostics=%d err=%v", result, len(rep.batches), err)
	}
	evt := rep.batches[0]
	if evt.Status != "success" || evt.SystemPrompt == "" || len(evt.JSONSchema) == 0 || evt.ParentRequestID == "" || evt.PoolIndex == nil || *evt.PoolIndex != 0 {
		t.Fatal("batch diagnostics lost the request context")
	}
	if evt.UserMessage != evt.SentContent || len(evt.UserMessage) > progress.MaxSSEBatchContentBytes || evt.SentLength <= len(evt.UserMessage) || !utf8.ValidString(evt.UserMessage) {
		t.Fatalf("prompt preview bytes=%d original=%d", len(evt.UserMessage), evt.SentLength)
	}
	if evt.ResponseContent != evt.ReceivedContent || evt.ResponseContent != reply[:progress.MaxSSEBatchContentBytes] || evt.ReceivedLength != len(reply) || unsafe.StringData(evt.ResponseContent) == unsafe.StringData(reply) {
		t.Fatalf("response preview bytes=%d original=%d", len(evt.ResponseContent), evt.ReceivedLength)
	}
}
