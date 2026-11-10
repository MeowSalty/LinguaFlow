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
		if err := json.Unmarshal(request.response.Receipt, &receipt); err != nil || receipt.Version != 2 || receipt.Skipped != 1 || len(request.response.Receipt) > 32<<10 || len(request.response.Raw) > 0 || len(request.response.CorrectedTargets) > 0 {
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
	frozen := frozenMainBatch{Mode: RoundModeTranslate, Format: "txt", TargetLang: "zh", Members: []frozenMainMember{{Index: 0, PromptID: "1", CandidateID: "nil-meta", Segment: Segment{Source: "source", Status: "pending", TargetIsNull: true}, RubyItems: []ruby.Item{{ID: "1", SourceBase: "source", SourceText: "reading"}}}}}
	payload, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"translations":{"1":"候选"},"glossary":[{"source":"thread pool","target":"候选"}]}`)
	response := MainResponse{MainResponseHeader: MainResponseHeader{ID: "nil-meta", FreezeBytes: int64(len(payload)), ResponseBytes: int64(len(raw)), ResponseDigest: responseDigest(raw), BusinessBound: 1 << 20}, FreezePayload: payload, Raw: raw}
	h := &TranslateHandler{Glossary: g, InlineBootstrap: true, InlineConflictStrategy: InlineConflictRewriteLocal, Repair: defaultRepairOpts()}
	processed, err := processFrozenGlossary(ctx, h, frozen, response, nil, runtime, quietLogger())
	if err != nil || processed.Phase != "glossary_processed" || !strings.Contains(string(processed.CorrectedTargets), "规范") {
		t.Fatalf("separate ruby validation: phase=%s body=%s err=%v", processed.Phase, processed.CorrectedTargets, err)
	}
}
