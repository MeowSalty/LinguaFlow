package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

const rubyEntrySource = "<ruby>source-a<rt>original-a</rt></ruby> <ruby>source-b<rt>original-b</rt></ruby>"
const rubyEntryOriginal = "<ruby>prior-a<rt>old-a</rt></ruby> <ruby>prior-b<rt>old-b</rt></ruby>"
const rubyEntryTarget = "<ruby>alpha<rt>reading-a</rt></ruby> <ruby>beta<rt>reading-b</rt></ruby>"

type rubyEntryRequest struct {
	Task     string          `json:"task"`
	Segments json.RawMessage `json:"segments"`
	Missing  []struct {
		ID         string `json:"id"`
		SourceBase string `json:"source_base"`
	} `json:"missing"`
	Regions []struct {
		Text string `json:"text"`
	} `json:"translation_regions"`
	ViewDigest string `json:"region_digest"`
}

type rubyEntryFixture struct {
	client   *ent.Client
	factory  *EngineFactory
	snapshot *service.JobExecutionSnapshot
	started  chan struct{}
	canceled chan struct{}
	mu       sync.Mutex
	requests []rubyEntryRequest
}

func newRubyEntryFixture(t *testing.T, cancelAlignment bool) *rubyEntryFixture {
	t.Helper()
	f := &rubyEntryFixture{client: newRegistryTestClient(t), started: make(chan struct{}, 1), canceled: make(chan struct{}, 1)}
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		var request rubyEntryRequest
		for _, message := range payload.Messages {
			if message.Role == "user" {
				if err := json.Unmarshal([]byte(message.Content), &request); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			}
		}
		f.mu.Lock()
		f.requests = append(f.requests, request)
		f.mu.Unlock()
		var content any
		if len(request.Missing) > 0 {
			select {
			case f.started <- struct{}{}:
			default:
			}
			if cancelAlignment {
				select {
				case <-r.Context().Done():
					select {
					case f.canceled <- struct{}{}:
					default:
					}
					return
				case <-release:
					return
				}
			}
			entries := make([]ruby.OutputEntry, 0, len(request.Missing))
			for _, missing := range request.Missing {
				base, reading := "alpha", "reading-a"
				if strings.HasSuffix(missing.SourceBase, "-b") {
					base, reading = "beta", "reading-b"
				}
				entries = append(entries, ruby.OutputEntry{ID: missing.ID, Base: base, Text: reading, Kind: "creative", Occurrence: 1})
			}
			content = map[string]any{"ruby_output": entries}
		} else if request.Task == "revise_translation" {
			var segments []prompt.ReviseSegment
			if err := json.Unmarshal(request.Segments, &segments); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			revisions := make([]prompt.ReviseRevision, 0, len(segments))
			for _, segment := range segments {
				revisions = append(revisions, prompt.ReviseRevision{ID: segment.ID, Target: "alpha beta"})
			}
			content = map[string]any{"revisions": revisions}
		} else {
			var segments map[string]prompt.SegmentDetail
			if err := json.Unmarshal(request.Segments, &segments); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			translations := make(map[string]string)
			for id, segment := range segments {
				if segment.Translate {
					translations[id] = "alpha beta"
				}
			}
			content = map[string]any{"translations": translations}
		}
		encoded, err := json.Marshal(content)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "entry", "object": "chat.completion",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(encoded)}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 11, "total_tokens": 18},
		})
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(func() { close(release) })
	pool := backend.NewLimiterPool()
	pool.Initialize(map[int]int{1: 0})
	t.Cleanup(pool.Shutdown)
	credentials := credential.NewMemory()
	binding, err := credentials.Register("openai", upstream.URL, "entry-test")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := translateSnapshot(t)
	snapshot.Rounds[0].Backend = service.BackendSnapshot{ID: 1, Name: "entry", Type: "openai", Credential: binding, Options: map[string]any{"base_url": upstream.URL, "model": "entry", "response_format": "none", "stream": false}}
	snapshot.Strategy.Ruby.Enabled = true
	snapshot.Strategy.Ruby.PreserveKinds = []string{"creative"}
	snapshot.Strategy.QA.Enabled = false
	snapshot.Rounds[0].Translate.Concurrency = 1
	snapshot.RubyRetry = &service.ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: snapshot.Rounds[0].Backend, MaxAttempts: 1, Concurrency: 2}
	f.snapshot, err = execution.Resolve(*snapshot)
	if err != nil {
		t.Fatal(err)
	}
	f.factory = NewEngineFactoryWithCredentials(slog.New(slog.NewTextHandler(io.Discard, nil)), pool, credentials, credentials)
	return f
}

type rubyEntryResult struct {
	status, target string
	metrics        []backend.MeterMetrics
	err            error
}

func (f *rubyEntryFixture) run(ctx context.Context, kind string) rubyEntryResult {
	project := &ent.Project{ID: 1}
	resource := &ent.Resource{ID: 1, Format: "txt"}
	segments := []*ent.Segment{{ID: 1, ResourceID: new(1), SegmentIndex: 0, SourceText: rubyEntrySource, Status: service.SegmentStatusPending}}
	switch kind {
	case "preview":
		runner := NewPreviewRunner(f.factory.logger, f.client, f.factory.limiterPool)
		runner.factory = f.factory
		result, err := runner.RunPreview(ctx, f.snapshot, project, resource, segments, 0, "")
		if err != nil {
			return rubyEntryResult{err: err}
		}
		return rubyEntryResult{status: result.Status, target: result.TargetText, metrics: result.Metrics}
	case "revision_preview":
		snapshot := *f.snapshot
		snapshot.Rounds = []service.JobRoundSnapshot{{Mode: "revise", Backend: snapshot.Rounds[0].Backend, Revise: &service.JobReviseRoundSnapshot{
			TemplateContent: templates.EmbeddedReviseTemplate(), BatchSize: 1, Concurrency: 1, SegmentScope: "with_issues", IssueCodes: []string{"calque"},
		}}}
		segments[0].TargetText = new(rubyEntryOriginal)
		segments[0].Status = service.SegmentStatusTranslated
		segments[0].QualityIssues = []qa.QualityIssue{{Code: "calque", Severity: qa.SeverityWarning, Message: "revise"}}
		runner := NewRevisionPreviewRunner(f.factory.logger, f.client, f.factory.limiterPool)
		runner.factory = f.factory
		result, err := runner.RunRevisionPreview(ctx, &snapshot, project, resource, segments, 0, qa.Config{}, repair.Options{}, false)
		if err != nil {
			return rubyEntryResult{err: err}
		}
		return rubyEntryResult{status: result.Status, target: result.TargetText, metrics: result.Metrics}
	case "quick":
		runner := NewQuickTranslateRunner(f.factory.logger, f.client, f.factory.limiterPool)
		runner.factory = f.factory
		result, err := runner.Run(ctx, service.QuickTranslateRunnerInput{Snapshot: f.snapshot, SourceLang: "en", TargetLang: "zh", SourceText: rubyEntrySource, Format: "txt", Glossary: glossary.Nop{}})
		if err != nil {
			return rubyEntryResult{err: err}
		}
		return rubyEntryResult{status: result.Status, target: result.TargetText, metrics: result.Metrics}
	default:
		return rubyEntryResult{err: fmt.Errorf("unknown entry %s", kind)}
	}
}

func (f *rubyEntryFixture) checkRequestsAndNoJobDrafts(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	requests := append([]rubyEntryRequest(nil), f.requests...)
	f.mu.Unlock()
	if len(requests) != 2 || len(requests[0].Missing) != 0 || len(requests[1].Missing) != 2 || len(requests[1].Regions) != 1 || requests[1].Regions[0].Text != "alpha beta" || requests[1].ViewDigest == "" {
		t.Fatalf("entry did not execute main then frozen single-candidate alignment: %+v", requests)
	}
	ctx := context.Background()
	if f.client.Job.Query().CountX(ctx) != 0 || f.client.JobRound.Query().CountX(ctx) != 0 || f.client.WorkItem.Query().CountX(ctx) != 0 || f.client.WorkCandidate.Query().CountX(ctx) != 0 || f.client.WorkRequest.Query().CountX(ctx) != 0 {
		t.Fatal("short invocation manufactured Job work or persistent drafts")
	}
}

func TestRubyEntryPointsUseStagedHTTPWithoutJobDrafts(t *testing.T) {
	for _, kind := range []string{"preview", "revision_preview", "quick"} {
		t.Run(kind, func(t *testing.T) {
			f := newRubyEntryFixture(t, false)
			before, _ := json.Marshal(f.snapshot)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result := f.run(ctx, kind)
			if result.err != nil || result.status != "success" || result.target != rubyEntryTarget {
				t.Fatalf("entry result: %+v", result)
			}
			var calls int64
			for _, metrics := range result.metrics {
				calls += metrics.APICalls
			}
			if calls != 2 {
				t.Fatalf("metered calls=%d, want main + alignment", calls)
			}
			f.checkRequestsAndNoJobDrafts(t)
			after, _ := json.Marshal(f.snapshot)
			if string(before) != string(after) {
				t.Fatal("entry changed its frozen snapshot")
			}
		})
	}
}

func TestRubyEntryPointsCancelAlignmentWithoutAcceptingCandidate(t *testing.T) {
	for _, kind := range []string{"preview", "revision_preview", "quick"} {
		t.Run(kind, func(t *testing.T) {
			f := newRubyEntryFixture(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan rubyEntryResult, 1)
			go func() { done <- f.run(ctx, kind) }()
			select {
			case <-f.started:
				cancel()
			case result := <-done:
				t.Fatalf("entry ended before alignment: %+v", result)
			case <-ctx.Done():
				t.Fatal("alignment was not dispatched")
			}
			select {
			case result := <-done:
				wantTarget := ""
				if kind == "revision_preview" {
					wantTarget = rubyEntryOriginal
				}
				if result.err != nil || result.status == "success" || result.target != wantTarget {
					t.Fatalf("cancel accepted a private candidate: %+v", result)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("entry did not join its canceled HTTP request")
			}
			select {
			case <-f.canceled:
			case <-time.After(10 * time.Second):
				t.Fatal("provider HTTP request was not canceled")
			}
			f.checkRequestsAndNoJobDrafts(t)
		})
	}
}

func TestRubyEntryFactorySharesAllowanceAcrossRounds(t *testing.T) {
	f := newRubyEntryFixture(t, false)
	snapshot := *f.snapshot
	snapshot.Rounds = append(append([]service.JobRoundSnapshot(nil), snapshot.Rounds...), snapshot.Rounds[0])
	e, err := f.factory.BuildEngine(context.Background(), &snapshot, engine.RuntimeResources{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var permits []*backend.RequestPermit
	defer func() {
		for _, permit := range permits {
			permit.Release()
		}
	}()
	runtime := e.Rounds()[0].Runtime
	if runtime == nil || e.Rounds()[1].Runtime != runtime {
		t.Fatal("one invocation created per-round runtimes")
	}
	admit := func(round int, stage backend.RequestStage, expected bool) {
		t.Helper()
		permit, _, err := runtime.Admission.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RoundIndex: round, BackendID: 1, Stage: stage}, runtime.Gate)
		if permit != nil {
			permits = append(permits, permit)
		}
		if err != nil || (permit != nil) != expected {
			t.Fatalf("round=%d stage=%s admission=%v err=%v", round, stage, permit != nil, err)
		}
	}
	admit(0, backend.RequestStageMain, true)
	admit(0, backend.RequestStageMain, false)
	admit(1, backend.RequestStageMain, true)
	admit(1, backend.RequestStageMain, false)
	admit(0, backend.RequestStageAlignment, true)
	admit(1, backend.RequestStageAlignment, true)
	admit(0, backend.RequestStageAlignment, false)
	admit(1, backend.RequestStageAlignment, false)
}
