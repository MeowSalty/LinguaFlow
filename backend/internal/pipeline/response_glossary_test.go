package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func TestRubyHandoffMemoryGlossaryCorrectsLargeTargetOnce(t *testing.T) {
	ctx := context.Background()
	g := glossary.NewMemory()
	large := strings.Repeat("规范", 8000)
	if _, err := g.Add(ctx, glossary.Entry{Source: "thread pool", Target: large}); err != nil {
		t.Fatal(err)
	}
	runtime := testExecutionRuntime(t, 4)
	defer runtime.Close()
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		payload, _ := json.Marshal(map[string]any{"translations": map[string]string{"1": "候选"}, "glossary": []map[string]string{{"source": "thread pool", "target": "候选"}}})
		return &backend.Response{Text: string(payload)}, nil
	}}
	h := &TranslateHandler{Backend: BindRequestBackend(inner, 1, 0, runtime), Renderer: newTestRenderer(t), BatchSize: 1, Glossary: g, InlineBootstrap: true, InlineConflictStrategy: InlineConflictRewriteLocal, Logger: quietLogger()}
	doc := newTestDoc(1)
	doc.TargetLang = "zh"
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	result, err := RunRound(ctx, Round{Concurrency: 1, Handler: h, Runtime: runtime, Store: store}, doc, nil, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 1 || doc.Segments[0].Target != large {
		t.Fatalf("result=%+v error=%v target bytes=%d", result, err, len(doc.Segments[0].Target))
	}
	if inner.calls.Load() != 1 {
		t.Fatal("response promotion repeated model")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, request := range store.requests {
		var receipt glossary.ResponseReceipt
		if err := json.Unmarshal(request.response.Receipt, &receipt); err != nil || receipt.Version != 2 || receipt.Skipped != 1 || len(request.response.Receipt) > 32<<10 || request.capture != nil || len(request.outputs) > 0 || len(request.corrections) > 0 || request.response.InputMemberBytes != 0 || request.response.OutputMemberBytes != 0 {
			t.Fatalf("receipt/cleanup %+v %v", receipt, err)
		}
	}
}

func TestRubyHandoffGlossaryValidationRestoresSeparateRubyIntoNilMetadata(t *testing.T) {
	ctx := context.Background()
	g := glossary.NewMemory()
	if _, err := g.Add(ctx, glossary.Entry{Source: "thread pool", Target: "规范"}); err != nil {
		t.Fatal(err)
	}
	runtime := testExecutionRuntime(t, 1)
	defer runtime.Close()
	ctx = runtime.BuildContext(WithExecutionRuntime(ctx, runtime), "promotion:nil-meta")
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	frozen := frozenMainMember{Index: 0, PromptID: "1", CandidateID: "nil-meta", Segment: Segment{Source: "source", Status: "pending", TargetIsNull: true}, RubyItems: []ruby.Item{{ID: "1", SourceBase: "source", SourceText: "reading"}}}
	header := memberFixtureReserve(t, ctx, store, "nil-meta", 1<<20, []frozenMainMember{frozen})
	header = memberFixtureOutputs(t, ctx, store, header, []archivedOutput{{Target: "候选"}}, []glossary.Entry{{Source: "thread pool", Target: "候选"}})
	if err := runtime.Admission.RestoreResponseOwnership(header.ID, 0, 0, header.Facts(), header.Phase, false); err != nil {
		t.Fatal(err)
	}
	defer runtime.Admission.ReleaseDurableResponse(header.ID)
	h := &TranslateHandler{Glossary: g, InlineBootstrap: true, InlineConflictStrategy: InlineConflictRewriteLocal, Repair: defaultRepairOpts()}
	processed, err := processArchivedGlossary(ctx, h, header, store, runtime, quietLogger())
	if err != nil || processed.Phase != "business_ready" {
		t.Fatalf("separate ruby validation: phase=%s err=%v", processed.Phase, err)
	}
	frame, err := store.LoadOutputMember(ctx, archiveMemberKey(header, 0), true, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer frame.Release()
	selected := memberFixtureDecode(t, frame)
	if selected.Target != "规范" {
		t.Fatalf("corrected target=%q", selected.Target)
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, 1<<20, frame.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	defer b.CloseRetired()
	candidate, err := prepareArchivedCandidate(b, h, processed, frozen, selected, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	items, ok := candidate.Segment.Meta["ruby_items"].([]ruby.Item)
	if !ok || len(items) != 1 || items[0].ID != "1" {
		t.Fatalf("separate frozen ruby was lost: %#v", candidate.Segment.Meta)
	}
	if _, err := store.LoadOutputMember(ctx, archiveMemberKey(header, 1), true, 1<<20); err == nil {
		t.Fatal("wrong ordinal output accepted")
	}
}
