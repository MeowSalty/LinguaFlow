package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type boundedStoredProbe struct {
	runtime *ExecutionRuntime
	text    string
	calls   int
	held    bool
}

func (*boundedStoredProbe) Name() string { return "bounded-stored" }
func (*boundedStoredProbe) Close() error { return nil }
func (p *boundedStoredProbe) Translate(context.Context, backend.Request) (*backend.Response, error) {
	p.calls++
	p.held = p.held || len(p.runtime.parse) != 0
	return &backend.Response{Text: p.text}, nil
}

type boundedStoredGlossary struct {
	runtime   *ExecutionRuntime
	added     []glossary.Entry
	held      bool
	lookupErr error
	addErr    error
}

func (*boundedStoredGlossary) Lookup(context.Context, string, string, string) ([]glossary.Entry, error) {
	return nil, nil
}
func (g *boundedStoredGlossary) LookupSourcesBounded(context.Context, string, string, string, int64, func(context.Context, func() error) error) ([]string, error) {
	return nil, g.lookupErr
}
func (g *boundedStoredGlossary) Add(_ context.Context, entries ...glossary.Entry) (glossary.AddResult, error) {
	g.held = g.held || len(g.runtime.parse) != 0
	if g.addErr != nil {
		return glossary.AddResult{}, g.addErr
	}
	g.added = append(g.added, entries...)
	return glossary.AddResult{Added: entries}, nil
}

func TestBoundedStoredHandlersKeepNetworkAndAddOutsideCPU(t *testing.T) {
	for _, mode := range []string{"extract", "semantic_qa", "adjudicate"} {
		t.Run(mode, func(t *testing.T) {
			r := &ExecutionRuntime{parse: make(chan struct{}, 2), ResponseBytes: 4096, HandoffBytes: 64 << 10}
			ctx := WithExecutionRuntime(context.Background(), r)
			p := &boundedStoredProbe{runtime: r}
			doc := &Document{SourceLang: "en", TargetLang: "zh", Segments: []Segment{{ID: "0", Source: "source", Target: "target", Status: "translated", Issues: []qa.QualityIssue{{Code: "calque", Message: "issue"}}}}}
			var handler RoundHandler
			var g *boundedStoredGlossary
			switch mode {
			case "extract":
				p.text = `{"glossary":[{"source":"term","target":"value","notes":"verbatim notes"}]}`
				renderer, e := prompt.NewBootstrapRenderer("extract")
				if e != nil {
					t.Fatal(e)
				}
				g = &boundedStoredGlossary{runtime: r}
				handler = &ExtractHandler{Renderer: renderer, Backends: []backend.Backend{p}, Glossary: g}
			case "semantic_qa":
				p.text = `{"issues":[{"id":"0","code":"calque","message":"warning","snippet":"target"}]}`
				renderer, e := prompt.NewSemanticQARenderer("quality")
				if e != nil {
					t.Fatal(e)
				}
				handler = &SemanticQAHandler{Renderer: renderer, Backend: p}
			case "adjudicate":
				p.text = `{"verdicts":[{"id":"0","issue_code":"calque","verdict":"false_positive","reason":"reason"}]}`
				renderer, e := prompt.NewAdjudicationRenderer("adjudicate")
				if e != nil {
					t.Fatal(e)
				}
				handler = &AdjudicateHandler{Renderer: renderer, Backend: p, AdjudicateCodes: []string{"calque"}}
			}
			result := handler.ProcessBatch(ctx, doc, []int{0}, 0, quietLogger())
			if result.err != nil || len(result.unresolved) != 0 || len(result.failedSegments) != 0 || p.calls != 1 || p.held || len(r.parse) != 0 {
				t.Fatalf("result=%+v calls=%d held=%v", result, p.calls, p.held)
			}
			if g != nil && (g.held || len(g.added) != 1 || g.added[0].Notes != "verbatim notes") {
				t.Fatalf("add=%+v held=%v", g.added, g.held)
			}
			if mode == "semantic_qa" && (result.callbackResult == nil || len(result.callbackResult.Segments[0].Issues) != 1) {
				t.Fatal("semantic result lost")
			}
			if mode == "adjudicate" && (result.callbackResult == nil || !result.callbackResult.Segments[0].Issues[0].Dismissed()) {
				t.Fatal("verdict lost")
			}
		})
	}
}

func TestBoundedStoredHandlersRejectInputBeforeNetwork(t *testing.T) {
	for _, mode := range []string{"extract", "semantic_qa", "adjudicate"} {
		t.Run(mode, func(t *testing.T) {
			r := &ExecutionRuntime{parse: make(chan struct{}, 2), ResponseBytes: 4096, HandoffBytes: 64 << 10}
			ctx := WithExecutionRuntime(context.Background(), r)
			p := &boundedStoredProbe{runtime: r, text: `{"issues":[]}`}
			doc := &Document{SourceLang: "en", TargetLang: "zh", Segments: []Segment{{ID: "0", Source: strings.Repeat("x", 80<<10), Target: "target", Status: "translated"}}}
			var h RoundHandler
			switch mode {
			case "extract":
				renderer, _ := prompt.NewBootstrapRenderer("extract")
				h = &ExtractHandler{Renderer: renderer, Backends: []backend.Backend{p}, Glossary: glossary.Nop{}}
			case "semantic_qa":
				renderer, _ := prompt.NewSemanticQARenderer("quality")
				h = &SemanticQAHandler{Renderer: renderer, Backend: p}
			case "adjudicate":
				renderer, _ := prompt.NewAdjudicationRenderer("adjudicate")
				h = &AdjudicateHandler{Renderer: renderer, Backend: p}
			}
			h.ProcessBatch(ctx, doc, []int{0}, 0, quietLogger())
			if p.calls != 0 || len(r.parse) != 0 {
				t.Fatalf("oversize input dispatched %d calls, CPU=%d", p.calls, len(r.parse))
			}
		})
	}
}

func TestBoundedStoredSemanticCapacityDoesNotRetryModel(t *testing.T) {
	r := &ExecutionRuntime{parse: make(chan struct{}, 2), ResponseBytes: 256, HandoffBytes: 64 << 10}
	p := &boundedStoredProbe{runtime: r, text: `{"issues":[{"id":"0","code":"calque","message":"` + strings.Repeat("x", 300) + `"}]}`}
	renderer, _ := prompt.NewSemanticQARenderer("quality")
	h := &SemanticQAHandler{Renderer: renderer, Backend: p, Retry: backend.RetryPolicy{MaxAttempts: 3}}
	doc := &Document{Segments: []Segment{{ID: "0", Source: "source", Target: "target", Status: "translated"}}}
	result := h.ProcessBatch(WithExecutionRuntime(context.Background(), r), doc, []int{0}, 0, quietLogger())
	if p.calls != 1 || result.retry != nil || len(result.failedSegments) != 1 || len(r.parse) != 0 {
		t.Fatalf("capacity result=%+v calls=%d", result, p.calls)
	}
}

func TestBoundedStoredExtractContextCapacityRefusesDispatch(t *testing.T) {
	r := &ExecutionRuntime{parse: make(chan struct{}, 2), ResponseBytes: 4096, HandoffBytes: 64 << 10}
	p := &boundedStoredProbe{runtime: r}
	g := &boundedStoredGlossary{runtime: r, lookupErr: backend.ErrBuildCapacity}
	renderer, _ := prompt.NewBootstrapRenderer("extract")
	h := &ExtractHandler{Renderer: renderer, Backends: []backend.Backend{p}, Glossary: g}
	doc := &Document{Segments: []Segment{{ID: "0", Source: "source"}}}
	result := h.ProcessBatch(WithExecutionRuntime(context.Background(), r), doc, []int{0}, 0, quietLogger())
	if p.calls != 0 || len(result.failedSegments) != 1 || len(result.unresolved) != 0 || len(r.parse) != 0 {
		t.Fatalf("context was silently omitted or retried: %+v calls=%d", result, p.calls)
	}
}

func TestBoundedStoredRecordSizes(t *testing.T) {
	for _, row := range []struct {
		name string
		size uintptr
	}{
		{"BootstrapEntry", unsafe.Sizeof(prompt.BootstrapEntry{})},
		{"SemanticQASegment", unsafe.Sizeof(prompt.SemanticQASegment{})},
		{"SemanticQAIssue", unsafe.Sizeof(prompt.SemanticQAIssue{})},
		{"AdjudicationSegment", unsafe.Sizeof(prompt.AdjudicationSegment{})},
		{"AdjudicationVerdict", unsafe.Sizeof(prompt.AdjudicationVerdict{})},
		{"GlossaryEntry", unsafe.Sizeof(glossary.Entry{})},
		{"QualityIssue", unsafe.Sizeof(qa.QualityIssue{})},
		{"Span", unsafe.Sizeof(qa.Span{})},
		{"TranslatedSegment", unsafe.Sizeof(TranslatedSegment{})},
		{"BatchResult", unsafe.Sizeof(BatchResult{})},
	} {
		t.Logf("%s=%d", row.name, row.size)
		if row.size > 1024 {
			t.Fatalf("%s exceeds fixed E", row.name)
		}
	}
}

func TestBoundedStoredPlannerPreservesBatchOrder(t *testing.T) {
	doc := &Document{Segments: make([]Segment, 40)}
	for i := range doc.Segments {
		doc.Segments[i].Source = strings.Repeat("word ", 1+i%5)
		doc.Segments[i].Target = "target"
	}
	pending := []int{0, 1, 2, 3, 5, 6, 8, 9, 10, 11, 12, 20, 22, 23, 24, 28, 29, 30}
	for _, continuous := range []bool{false, true} {
		for _, constraint := range []BatchConstraint{{}, {MaxSegments: 2}, {MaxWords: 5}, {MaxSegments: 3, MaxWords: 8}} {
			for _, span := range []int{0, 4} {
				if continuous && span != 0 {
					continue
				}
				got, count, err := buildStoredScopeBounded(context.Background(), doc, pending, nil, func(*Segment, map[string]struct{}) bool { return true }, constraint, span, continuous)
				if err != nil || count != len(pending) {
					t.Fatalf("planner err=%v count=%d", err, count)
				}
				var want [][]int
				if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 && span <= 0 {
					want = [][]int{pending}
				} else if continuous {
					want = BuildContinuousPendingBatches(doc, pending, constraint)
				} else {
					want = BuildPackedPendingBatches(doc, pending, constraint, span)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("continuous=%v constraint=%+v span=%d got=%v want=%v", continuous, constraint, span, got, want)
				}
				for _, batch := range got {
					if len(batch) != cap(batch) {
						t.Fatal("batch aliases append capacity")
					}
					batch[0] = -1
				}
				if pending[0] != 0 {
					t.Fatal("planner modified borrowed pending")
				}
			}
		}
	}
}

func TestBoundedStoredPlannerCapacityBeforeBacking(t *testing.T) {
	r := &ExecutionRuntime{parse: make(chan struct{}, 2), ResponseBytes: 4096, HandoffBytes: 4096}
	got, _, err := buildStoredScopeBounded(WithExecutionRuntime(context.Background(), r), &Document{}, make([]int, 2000), nil, func(*Segment, map[string]struct{}) bool { return true }, BatchConstraint{}, 0, false)
	if !errors.Is(err, backend.ErrBuildCapacity) || got != nil || len(r.parse) != 0 {
		t.Fatalf("planner=%v err=%v CPU=%d", got, err, len(r.parse))
	}
}

func TestBoundedStoredExtractStorageFailureDoesNotResend(t *testing.T) {
	r := &ExecutionRuntime{parse: make(chan struct{}, 2), ResponseBytes: 4096, HandoffBytes: 64 << 10}
	p := &boundedStoredProbe{runtime: r, text: `{"glossary":[{"source":"term","target":"value"}]}`}
	g := &boundedStoredGlossary{runtime: r, addErr: &StorageError{Err: errors.New("storage failed")}}
	renderer, _ := prompt.NewBootstrapRenderer("extract")
	h := &ExtractHandler{Renderer: renderer, Backends: []backend.Backend{p}, Glossary: g}
	result := h.ProcessBatch(WithExecutionRuntime(context.Background(), r), &Document{Segments: []Segment{{ID: "0", Source: "source"}}}, []int{0}, 0, quietLogger())
	if !storageFailure(result.err) || p.calls != 1 || g.held || len(r.parse) != 0 {
		t.Fatalf("result=%+v calls=%d CPU=%d", result, p.calls, len(r.parse))
	}
}
