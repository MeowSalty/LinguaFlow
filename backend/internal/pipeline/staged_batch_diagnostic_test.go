package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

func TestStagedTranslateBatchDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name, raw, status, op string
	}{
		{"success", strings.Repeat(" ", progress.MaxSSEBatchContentBytes+3) + `{"translations":{"1":"一","2":"二"}}`, "success", ""},
		{"partial", `{"translations":{"1":"一",},"glossary":[{"source":"new term","target":"新词"}]}`, "partial", "json.trailing-comma"},
		{"parse_error", "\ufeff{}", "failed", "json.strip-bom-zw"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			runtime := stagedTestRuntime(t, DefaultCandidateLimits())
			store := NewMemoryRoundStore(nil)
			defer store.Close()
			doc := newTestDoc(2)
			defer runtime.CloseDocument(doc)
			doc.Segments[0].Source = "known term in the source"
			main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
				return &backend.Response{Text: tc.raw, Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 3}, Truncated: true}, nil
			}}
			handler := stagedTestHandler(t, runtime, main)
			handler.Repair = repair.Options{JSONStructural: true}
			gloss := glossary.NewMemory()
			if _, err := gloss.Add(ctx, glossary.Entry{Source: "known term", Target: "术语"}); err != nil {
				t.Fatal(err)
			}
			handler.Glossary = gloss
			reporter := &recordingBatchObserver{}
			if _, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: handler}, doc, store, runtime, quietLogger(), reporter); err != nil {
				t.Fatal(err)
			}
			if len(reporter.events) != 1 || main.calls.Load() != 1 {
				t.Fatalf("diagnostics=%d calls=%d", len(reporter.events), main.calls.Load())
			}
			event := reporter.events[0]
			if event.Stage != "translate" || event.Status != tc.status || !slices.Equal(event.SegmentIDs, []string{"seg-0", "seg-1"}) || event.SegmentCount != 2 {
				t.Fatalf("unexpected status or segments: %s %s %v (%d)", event.Stage, event.Status, event.SegmentIDs, event.SegmentCount)
			}
			if event.ParentRequestID == "" || event.PoolIndex == nil || *event.PoolIndex != 0 || !slices.Equal(event.TriedBackends, []string{main.Name()}) ||
				event.SystemPrompt == "" || event.UserMessage == "" || event.JSONSchema == nil || event.SentLength != len(event.UserMessage) {
				t.Fatalf("incorrect request metadata: %+v", event)
			}
			if !event.Truncated || event.ReceivedLength != len(tc.raw) || event.ReceivedContent != progress.SSEContentPrefix(tc.raw) ||
				event.InputTokens != 17 || event.OutputTokens != 3 || doc.InputTokens != 17 || doc.OutputTokens != 3 {
				t.Fatalf("incorrect response metadata: length=%d truncated=%t usage=%d/%d totals=%d/%d",
					event.ReceivedLength, event.Truncated, event.InputTokens, event.OutputTokens, doc.InputTokens, doc.OutputTokens)
			}
			if tc.op != "" && !slices.Contains(event.Repaired, tc.op) {
				t.Fatalf("repair chain %v lacks %q", event.Repaired, tc.op)
			}
			if tc.status == "failed" && (event.ErrorType != "parse_error" || event.ErrorMessage == "" || !event.ShrinkAttempted) {
				t.Fatalf("incorrect parse error: %+v", event)
			}
			if len(event.UsedGlossary) != 1 || event.UsedGlossary[0].Source != "known term" {
				t.Fatalf("used glossary = %+v", event.UsedGlossary)
			}
			if tc.status == "partial" && (len(event.AddedGlossary) != 1 || event.AddedGlossary[0].Source != "new term") {
				t.Fatalf("added glossary = %+v", event.AddedGlossary)
			}
		})
	}
}

func TestStagedTranslateBatchDiagnosticIsNotReplayed(t *testing.T) {
	ctx := t.Context()
	main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"一"}}`, Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 3}}, nil
	}}
	store := &handoffStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
	defer store.Close()
	store.onSave = func(context.Context, *Candidate) error {
		return terminalRuntimeStoreError{errors.New("pause after output archive")}
	}
	reporter := &recordingBatchObserver{}
	first := stagedTestRuntime(t, DefaultCandidateLimits())
	doc := newTestDoc(1)
	round := Round{Concurrency: 1, Handler: stagedTestHandler(t, first, main)}
	if _, err := RunStagedRound(ctx, round, doc, store, first, quietLogger(), reporter); err == nil {
		t.Fatal("expected a promotion failure after archive")
	}
	if err := first.CloseDocument(doc); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if len(reporter.events) != 1 || reporter.events[0].Status != "success" {
		t.Fatalf("confirmed archive diagnostics = %+v", reporter.events)
	}
	store.onSave = nil
	resumed := stagedTestRuntime(t, DefaultCandidateLimits())
	doc = newTestDoc(1)
	defer resumed.CloseDocument(doc)
	round.Handler = stagedTestHandler(t, resumed, main)
	if _, err := RunStagedRound(ctx, round, doc, store, resumed, quietLogger(), reporter); err != nil {
		t.Fatal(err)
	}
	if len(reporter.events) != 1 || main.calls.Load() != 1 || doc.Segments[0].Target != "一" {
		t.Fatalf("resume diagnostics=%d model calls=%d target=%q", len(reporter.events), main.calls.Load(), doc.Segments[0].Target)
	}
}

func TestStagedReviseBatchDiagnosticKeepsSuccessForMissingRevision(t *testing.T) {
	ctx := t.Context()
	runtime := stagedTestRuntime(t, DefaultCandidateLimits())
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	doc := reviseDoc()
	defer runtime.CloseDocument(doc)
	doc.Segments = doc.Segments[:2]
	doc.Segments[0].ID = "revision-A"
	doc.Segments[1].ID = "revision-B"
	doc.Segments[1].Issues[0].Disposition = qa.DispositionPending
	main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"revisions":[{"id":"revision-A","target":"改好",},]}`}, nil
	}}
	handler := &ReviseHandler{
		Backend: BindRequestBackend(main, 1, 0, runtime), Renderer: newReviseRenderer(t), BatchSize: 2,
		IssueCodes: []string{"calque", "naturalness"}, Repair: repair.Options{JSONStructural: true}, Logger: quietLogger(),
	}
	reporter := &recordingBatchObserver{}
	if _, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: handler}, doc, store, runtime, quietLogger(), reporter); err != nil {
		t.Fatal(err)
	}
	if len(reporter.events) != 1 {
		t.Fatalf("revise diagnostics=%d", len(reporter.events))
	}
	event := reporter.events[0]
	if event.Stage != RoundModeRevise || event.Status != "success" || !slices.Equal(event.SegmentIDs, []string{"revision-A", "revision-B"}) ||
		!slices.Contains(event.Repaired, "json.trailing-comma") || event.JSONSchema == nil {
		t.Fatalf("incorrect revise diagnostic: %+v", event)
	}
}

func TestStagedProviderFailureWithOversizedDiagnosticGlossary(t *testing.T) {
	ctx := t.Context()
	runtime := stagedTestRuntime(t, DefaultCandidateLimits())
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	doc := newTestDoc(1)
	defer runtime.CloseDocument(doc)
	doc.Segments[0].Source = "known term in the source"
	main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return nil, &backend.StatusError{StatusCode: 400, Err: errors.New("request refused")}
	}}
	handler := stagedTestHandler(t, runtime, main)
	renderer, err := prompt.NewRenderer(testSystemTmpl + "\n{{range .Glossary}}{{.Notes}}{{end}}")
	if err != nil {
		t.Fatal(err)
	}
	handler.Renderer = renderer
	gloss := glossary.NewMemory()
	notes := strings.Repeat("n", progress.MaxSSEBatchContentBytes+64)
	if _, err := gloss.Add(ctx, glossary.Entry{Source: "known term", Target: "术语", Notes: notes}); err != nil {
		t.Fatal(err)
	}
	handler.Glossary = gloss
	reporter := &recordingBatchObserver{}
	if _, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: handler}, doc, store, runtime, quietLogger(), reporter); err != nil {
		t.Fatal(err)
	}
	if len(reporter.events) != 1 || main.calls.Load() != 1 {
		t.Fatalf("diagnostics=%d calls=%d", len(reporter.events), main.calls.Load())
	}
	event := reporter.events[0]
	if event.Status != "failed" || event.ErrorType != "backend_error" || event.HTTPStatus != 400 ||
		!strings.Contains(event.ErrorMessage, "request refused") || !slices.Equal(event.SegmentIDs, []string{"seg-0"}) ||
		event.BackendName != main.Name() || event.PoolIndex == nil || *event.PoolIndex != 0 || event.ParentRequestID == "" {
		t.Fatalf("failure identity or cause lost: status=%s backend=%s codes=%s/%d IDs=%v cause=%q",
			event.Status, event.BackendName, event.ErrorType, event.HTTPStatus, event.SegmentIDs, event.ErrorMessage)
	}
	if len(event.UsedGlossary) != 0 || event.SystemPromptLength < len(notes) || len(event.SystemPrompt) != progress.MaxSSEBatchContentBytes ||
		event.SentLength != len(event.SentContent) {
		t.Fatalf("supplemental diagnostic bounds: glossary=%d system=%d/%d user=%d/%d",
			len(event.UsedGlossary), len(event.SystemPrompt), event.SystemPromptLength, len(event.SentContent), event.SentLength)
	}
}
