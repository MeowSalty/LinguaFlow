package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
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
	return &backend.Response{Text: p.text, Usage: backend.Usage{PromptTokens: 7, CompletionTokens: 11}}, nil
}

func boundedQualityFixture(t *testing.T, mode string, p backend.Backend, reporter progress.Reporter) (RoundHandler, *Document) {
	t.Helper()
	if mode == RoundModeSemanticQA {
		return &SemanticQAHandler{Renderer: newSemanticQARenderer(t), Backend: p, SegmentScope: "all", Reporter: reporter}, semanticQADoc([]string{"translated"}, nil)
	}
	return &AdjudicateHandler{Renderer: newAdjudicationRenderer(t), Backend: p, AdjudicateCodes: []string{"calque"}, Reporter: reporter},
		adjudicableDoc([]string{"translated"}, [][]qa.QualityIssue{{{Code: "calque", Message: "issue"}}})
}

func TestBoundedStoredQualityNetworkRunsOutsideCPU(t *testing.T) {
	for _, tc := range []struct {
		mode, raw string
	}{
		{RoundModeSemanticQA, `{"issues":[{"id":"0","code":"calque","message":"warning"}]}`},
		{RoundModeAdjudicate, `{"verdicts":[{"id":"0","issue_code":"calque","verdict":"false_positive","reason":"reason"}]}`},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			r := memoryTestRuntime(t, 1, nil)
			p := &boundedStoredProbe{runtime: r, text: tc.raw}
			h, doc := boundedQualityFixture(t, tc.mode, p, nil)
			var batch BatchResult
			result, err := runMemoryTestRound(t, t.Context(), Round{Handler: h, Runtime: r, Concurrency: 1}, doc, func(_ context.Context, sealed BatchResult) error {
				return json.Unmarshal(sealed.PreparedPayload, &batch)
			}, quietLogger(), nil)
			if err != nil || !reflect.DeepEqual(result.Resolved, []int{0}) || p.calls != 1 || p.held || len(r.parse) != 0 {
				t.Fatalf("result=%+v calls=%d held=%v err=%v", result, p.calls, p.held, err)
			}
			if len(batch.Segments) != 1 || len(batch.Segments[0].Issues) != 1 ||
				batch.Segments[0].Issues[0].Dismissed() != (tc.mode == RoundModeAdjudicate) {
				t.Fatalf("quality result lost: %+v", batch.Segments)
			}
			if doc.InputTokens != 7 || doc.OutputTokens != 11 {
				t.Fatalf("captured tokens were not counted once: %d/%d", doc.InputTokens, doc.OutputTokens)
			}
		})
	}
}

func TestBoundedStoredQualityRejectsInputBeforeNetwork(t *testing.T) {
	for _, mode := range []string{RoundModeSemanticQA, RoundModeAdjudicate} {
		t.Run(mode, func(t *testing.T) {
			r := memoryTestRuntime(t, 1, nil)
			const handoff = 2 << 20
			if err := r.SetResponseLimits(4096, handoff); err != nil {
				t.Fatal(err)
			}
			p := &boundedStoredProbe{runtime: r}
			rep := &recordingBatchObserver{}
			h, doc := boundedQualityFixture(t, mode, p, rep)
			doc.Segments[0].Source = strings.Repeat("x", handoff+1)
			result, err := runMemoryTestRound(t, t.Context(), Round{Handler: h, Runtime: r, Concurrency: 1}, doc, nil, quietLogger(), rep)
			if err != nil || p.calls != 0 || len(r.parse) != 0 || len(result.Resolved) != 0 || len(rep.events) != 1 || rep.events[0].ErrorType != "render_error" {
				t.Fatalf("oversize input result=%+v calls=%d CPU=%d events=%+v err=%v", result, p.calls, len(r.parse), rep.events, err)
			}
		})
	}
}

func TestBoundedStoredSemanticCapacityDoesNotRetryModel(t *testing.T) {
	r := &ExecutionRuntime{parse: make(chan struct{}, 2), ResponseBytes: 256, HandoffBytes: 64 << 10}
	h := &SemanticQAHandler{Backend: &fakeBackend{name: "test"}, Retry: backend.RetryPolicy{MaxAttempts: 3}}
	doc := semanticQADoc([]string{"translated"}, nil)
	resp := &backend.Response{Text: `{"issues":[{"id":"0","code":"calque","message":"` + strings.Repeat("x", 300) + `"}]}`}
	result := h.processStoredResponse(WithExecutionRuntime(t.Context(), r), doc, []int{0}, resp, 0, quietLogger())
	if result.retry != nil || len(result.failedSegments) != 1 || len(r.parse) != 0 {
		t.Fatalf("capacity result=%+v", result)
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
	ctx := backend.WithBuildAllocation(WithExecutionRuntime(context.Background(), r), 4096, nil, nil)
	got, _, err := buildStoredScopeBounded(ctx, &Document{}, make([]int, 2000), nil, func(*Segment, map[string]struct{}) bool { return true }, BatchConstraint{}, 0, false)
	if !errors.Is(err, backend.ErrBuildCapacity) || got != nil || len(r.parse) != 0 {
		t.Fatalf("planner=%v err=%v CPU=%d", got, err, len(r.parse))
	}
}
