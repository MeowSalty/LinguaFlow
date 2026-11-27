package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

func TestJobStreamStagedBatchDiagnosticsReplay(t *testing.T) {
	const ringCapacity = 16
	s, client, user := jobStreamTestServer(t, ringCapacity, 2, 16)
	job, err := seedJobEventsN(t, client, user, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	providerErr := errors.New("gateway unavailable")
	provider := jobStreamTestProvider(func(ctx context.Context) (*backend.Response, error) {
		if calls.Add(1) == 1 {
			return nil, &backend.StatusError{StatusCode: http.StatusServiceUnavailable, Err: providerErr}
		}
		return &backend.Response{Text: `{"translations":{"1":"translated"}}`}, nil
	})
	result, err := runJobStreamStagedRound(t, s, job.ID, nil, provider)
	if err != nil || len(result.Resolved) != 1 || calls.Load() != 2 {
		t.Fatalf("staged round: resolved=%v calls=%d err=%v", result.Resolved, calls.Load(), err)
	}

	firstBody := replayJobStream(t, s, job.ID, user, "")
	batches := jobStreamBatchFrames(t, firstBody)
	if len(batches) != 2 {
		t.Fatalf("expected one failed and one successful batch, got %v", batches)
	}
	failed, succeeded := batches[0], batches[1]
	if failed.Level != "error" || failed.Stage != "translate" || failed.Metadata["status"] != "failed" ||
		failed.Metadata["error_type"] != "backend_error" || failed.Metadata["error_message"] != providerErr.Error() ||
		failed.Metadata["http_status"] != float64(http.StatusServiceUnavailable) || failed.Metadata["backend_name"] != provider.Name() ||
		failed.Metadata["attempt"] != float64(0) || failed.Metadata["round_index"] != float64(0) ||
		succeeded.Metadata["status"] != "success" || failed.Seq >= succeeded.Seq {
		t.Fatalf("unexpected batch diagnostics: failed=%+v succeeded=%+v", failed, succeeded)
	}

	// The process restarted and published a new event before the client reconnected.
	// The new ring can only serve that event, while the failure is still in SQLite.
	s.eventBroker = newJobStreamTestBroker(t, client, ringCapacity)
	s.eventBroker.Publish(job.ID, event.Event{JobID: job.ID, Type: "stage_counts", Level: "info", Message: "progress after restart"})
	latest, ok, err := s.eventBroker.LatestSeq(t.Context(), job.ID)
	if err != nil || !ok || latest <= succeeded.Seq {
		t.Fatalf("expected a new sequence after restart: seq=%d ok=%t err=%v", latest, ok, err)
	}
	firstSeqs := parseIDLines(firstBody)
	if len(firstSeqs) == 0 || firstSeqs[0] >= failed.Seq {
		t.Fatalf("expected an event before the failure, got seqs %v", firstSeqs)
	}
	resumedBody := replayJobStream(t, s, job.ID, user, strconv.FormatInt(firstSeqs[0], 10))
	expected := append(firstSeqs[1:], latest)
	if resumed := parseIDLines(resumedBody); !slices.Equal(resumed, expected) {
		t.Fatalf("resumed seqs = %v, want %v", resumed, expected)
	}
	if resumed := jobStreamBatchFrames(t, resumedBody); len(resumed) != 2 || resumed[0].Seq != failed.Seq ||
		resumed[0].Metadata["error_type"] != "backend_error" || resumed[0].Metadata["http_status"] != failed.Metadata["http_status"] ||
		resumed[0].Metadata["attempt"] != float64(0) || resumed[0].Metadata["round_index"] != float64(0) {
		t.Fatalf("persisted failure did not replay: %+v", resumed)
	}
}

func TestJobStreamStagedCaptureFailureIsNotBackendError(t *testing.T) {
	s, client, user := jobStreamTestServer(t, 16, 2, 16)
	job, err := seedJobEventsN(t, client, user, 0)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("capture persistence failed")
	store := &jobStreamCaptureFailureStore{MemoryRoundStore: pipeline.NewMemoryRoundStore(nil), cause: cause}
	defer store.Close()
	var calls atomic.Int64
	provider := jobStreamTestProvider(func(context.Context) (*backend.Response, error) {
		calls.Add(1)
		return &backend.Response{Text: `{"translations":{"1":"translated"}}`}, nil
	})
	_, err = runJobStreamStagedRound(t, s, job.ID, store, provider)
	if !errors.Is(err, cause) || calls.Load() != 1 {
		t.Fatalf("staged round: calls=%d err=%v, want one request and capture failure", calls.Load(), err)
	}
	for _, frame := range jobStreamBatchFrames(t, replayJobStream(t, s, job.ID, user, "")) {
		if frame.Metadata["error_type"] == "backend_error" {
			t.Fatalf("storage failure mislabeled as backend failure: %+v", frame)
		}
	}
}

type jobStreamTestProvider func(context.Context) (*backend.Response, error)

func (jobStreamTestProvider) Name() string { return "job-stream-test-provider" }
func (jobStreamTestProvider) Close() error { return nil }
func (send jobStreamTestProvider) Translate(ctx context.Context, _ backend.Request) (*backend.Response, error) {
	return send(ctx)
}

type jobStreamCaptureFailureStore struct {
	*pipeline.MemoryRoundStore
	cause error
}

func (s *jobStreamCaptureFailureStore) CaptureResponse(context.Context, pipeline.CapturedResponse) (pipeline.MainResponseHeader, error) {
	return pipeline.MainResponseHeader{}, &pipeline.StorageError{Operation: "response_capture", Err: s.cause}
}

func runJobStreamStagedRound(t *testing.T, s *Server, jobID int, store pipeline.RoundStore, provider backend.Backend) (pipeline.RunRoundResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, AlignmentConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	runtime := pipeline.NewExecutionRuntime(admission, pipeline.DefaultCandidateLimits(), nil)
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	reporter := progress.NewDBReporter(progress.DBReporterOptions{
		Client: s.entClient, JobID: jobID, Broker: s.eventBroker, Logger: s.logger,
		Saver: runtime.PersistenceSaver(), Lifetime: ctx, Ticker: time.Hour,
	})
	defer func() {
		if err := reporter.Close(); err != nil {
			t.Error(err)
		}
	}()
	renderer, err := prompt.NewRenderer("Translate from {{.SourceLang}} to {{.TargetLang}}.")
	if err != nil {
		t.Fatal(err)
	}
	retry := backend.RetryPolicy{MaxAttempts: 1, Backoff: time.Millisecond}
	handler := &pipeline.TranslateHandler{
		Backend: pipeline.BindRequestBackend(provider, 1, 0, runtime), Renderer: renderer,
		BatchSize: 1, Retry: retry, Logger: s.logger,
	}
	round := pipeline.Round{Handler: handler, Runtime: runtime, Store: store, Concurrency: 1, Retry: retry}
	doc := &pipeline.Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []pipeline.Segment{{ID: "one", Source: "source", Translate: true}},
	}
	return pipeline.RunRound(ctx, round, doc, nil, s.logger, reporter)
}

func jobStreamBatchFrames(t *testing.T, body string) []event.Event {
	t.Helper()
	var batches []event.Event
	for _, frame := range strings.Split(body, "\n\n") {
		id, rest, ok := strings.Cut(frame, "\nevent: batch\ndata: ")
		if !ok {
			continue
		}
		var evt event.Event
		if err := json.Unmarshal([]byte(rest), &evt); err != nil {
			t.Fatalf("decode batch event: %v", err)
		}
		if id != "id: "+strconv.FormatInt(evt.Seq, 10) || evt.Type != "batch" {
			t.Fatalf("unexpected batch SSE frame: %q", frame)
		}
		batches = append(batches, evt)
	}
	return batches
}
