package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

const testReviseTmpl = `Revise {{.SourceLang}} to {{.TargetLang}}.`

func newReviseRenderer(t *testing.T) *prompt.ReviseRenderer {
	t.Helper()
	r, err := prompt.NewReviseRenderer(testReviseTmpl)
	if err != nil {
		t.Fatalf("revise renderer: %v", err)
	}
	return r
}

func reviseDoc() *Document {
	return &Document{
		SourceLang: "en", TargetLang: "zh", Vars: map[string]any{},
		Segments: []Segment{
			{ID: "0", Source: "hello", Target: "你好", Status: "translated", Translate: true,
				Issues: []qa.QualityIssue{{Code: "calque", Message: "借译"}}},
			{ID: "1", Source: "world", Target: "世界", Status: "edited", Translate: true,
				Issues: []qa.QualityIssue{{Code: "naturalness", Message: "生硬", Disposition: qa.DispositionDismissed}}},
			{ID: "2", Source: "skip", Target: "跳过", Status: "translated", Translate: true},
			{ID: "3", Source: "pending", Target: "待处理", Status: "pending", Translate: true,
				Issues: []qa.QualityIssue{{Code: "calque", Message: "借译"}}},
		},
	}
}

func TestReviseHandler_BuildBatchesFiltersPendingCodesAndResolved(t *testing.T) {
	doc := reviseDoc()
	doc.ResolvedIndices = map[int]struct{}{0: {}}
	h := &ReviseHandler{Backend: &fakeBackend{name: "fake"}, Renderer: newReviseRenderer(t), BatchSize: 10, Logger: discardLogger()}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if batches != nil {
		t.Fatalf("batches=%v want nil after resolved/code filtering", batches)
	}

	doc.ResolvedIndices = nil
	doc.Segments[1].Issues[0].Disposition = qa.DispositionPending
	h.IssueCodes = []string{"naturalness"}
	batches, err = h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batches, [][]int{{1}}) {
		t.Fatalf("batches=%v want [[1]]", batches)
	}
}

func TestReviseHandler_ProcessBatchReturnsKnownRevisionsAndMarksMissingUnresolved(t *testing.T) {
	doc := reviseDoc()
	fb := &fakeBackend{name: "fake", responses: []string{`{"revisions":[{"id":"0","target":"你好啊"},{"id":"unknown","target":"丢弃"}]}`}}
	h := &ReviseHandler{Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger()}
	result := h.ProcessBatch(context.Background(), doc, []int{0, 1}, 0, discardLogger())
	if result.callbackResult == nil || len(result.callbackResult.Segments) != 1 {
		t.Fatalf("callback=%#v want one known revision", result.callbackResult)
	}
	if got := result.callbackResult.Segments[0]; got.TargetText != "你好啊" || got.Issues != nil {
		t.Fatalf("segment=%+v want revised text and nil issues", got)
	}
	// 段 1 未被 LLM 返回：必须进 unresolved 交由下一池重试。executor 对终态批次
	// 按 idxs−unresolved 计数（handler 不触碰进度计数），resolved 子集={0}、
	// missing={1}，两序列的配对断言在 executor 层测试覆盖。
	if !reflect.DeepEqual(result.unresolved, []int{1}) {
		t.Fatalf("unresolved=%v want [1]", result.unresolved)
	}
	if doc.Segments[0].Target != "你好" {
		t.Fatal("handler must not mutate document target")
	}
}

func TestReviseHandler_ProcessBatchSameTextIsReturned(t *testing.T) {
	doc := reviseDoc()
	fb := &fakeBackend{name: "fake", responses: []string{`{"revisions":[{"id":"0","target":"你好"}]}`}}
	h := &ReviseHandler{Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger()}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if result.callbackResult == nil || result.callbackResult.Segments[0].TargetText != "你好" {
		t.Fatalf("result=%#v want same target returned", result.callbackResult)
	}
}

func TestReviseHandler_ParseFailureRetries(t *testing.T) {
	doc := reviseDoc()
	fb := &fakeBackend{name: "fake", responses: []string{"bad json"}}
	h := &ReviseHandler{Backend: fb, Renderer: newReviseRenderer(t), Retry: backend.RetryPolicy{MaxAttempts: 2}, Logger: discardLogger()}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if result.retry == nil || result.retry.attempt != 1 {
		t.Fatalf("retry=%+v want attempt 1", result.retry)
	}
	if result.callbackResult != nil || result.unresolved != nil {
		t.Fatalf("parse retry result=%+v", result)
	}
}

func TestReviseHandler_TextMode(t *testing.T) {
	doc := reviseDoc()
	fb := &fakeBackend{name: "fake", responses: []string{"[revisions]\n0 | 你好啊"}}
	h := &ReviseHandler{Backend: fb, Renderer: newReviseRenderer(t), ResponseMode: "text", Logger: discardLogger()}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if result.callbackResult == nil || result.callbackResult.Segments[0].TargetText != "你好啊" {
		t.Fatalf("result=%#v", result.callbackResult)
	}
	if fb.requests[0].ResponseFormat != "none" || fb.requests[0].JSONSchema != nil {
		t.Fatalf("request=%+v want text mode", fb.requests[0])
	}
	if !strings.Contains(fb.requests[0].User, "[segment]") {
		t.Fatal("text prompt missing segment")
	}
}

func TestReviseHandler_IssueCodesEmptyFallsBackToSemanticCodes(t *testing.T) {
	doc := reviseDoc()
	h := &ReviseHandler{Backend: &fakeBackend{name: "fake"}, Renderer: newReviseRenderer(t), Logger: discardLogger()}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batches, [][]int{{0}}) {
		t.Fatalf("batches=%v want [[0]]", batches)
	}
}

func TestReviseHandler_BackendErrorUnresolved(t *testing.T) {
	doc := reviseDoc()
	h := &ReviseHandler{Backend: &fakeBackend{name: "fake", errs: []error{errors.New("down")}}, Renderer: newReviseRenderer(t), Logger: discardLogger()}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if !reflect.DeepEqual(result.unresolved, []int{0}) {
		t.Fatalf("unresolved=%v want [0]", result.unresolved)
	}
}

// reviseMarkupDoc 构造含单个修订段的指定格式文档。基线译文带合法标签，
// 守卫判据是「不要把已经合法的译文改坏」，基线必须是改写前的译文。
func reviseMarkupDoc(format string) *Document {
	return &Document{
		Format:     format,
		SourceLang: "en",
		TargetLang: "zh",
		Vars:       map[string]any{},
		Segments: []Segment{{
			ID: "0", Source: "hello", Target: "<p>你好</p>", Status: "translated", Translate: true,
			Issues: []qa.QualityIssue{{Code: "calque", Message: "借译"}},
		}},
	}
}

// epub 文档上修订结果结构退化（多余闭标签）：与占位符守恒分支同一 fail-closed
// 语义——拒绝本次改写，段计入 unresolved 交由下一池重试，doc 保留原译文。
func TestReviseMarkupGuard_BrokenRevisionGoesUnresolved(t *testing.T) {
	doc := reviseMarkupDoc("epub")
	fb := &fakeBackend{name: "fake", responses: []string{`{"revisions":[{"id":"0","target":"<p>你好</rt>"}]}`}}
	h := &ReviseHandler{Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger()}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())

	if result.callbackResult != nil && len(result.callbackResult.Segments) != 0 {
		t.Fatalf("callback=%#v, want no segments（坏译文不得采信）", result.callbackResult)
	}
	if !reflect.DeepEqual(result.unresolved, []int{0}) {
		t.Fatalf("unresolved=%v, want [0]", result.unresolved)
	}
	if doc.Segments[0].Target != "<p>你好</p>" {
		t.Errorf("Target=%q, want original kept", doc.Segments[0].Target)
	}
}

// epub 文档上结构合法的修订照常采信：守卫只拦坏结果，不拦 revise 本身。
func TestReviseMarkupGuard_WellFormedRevisionPasses(t *testing.T) {
	doc := reviseMarkupDoc("epub")
	fb := &fakeBackend{name: "fake", responses: []string{`{"revisions":[{"id":"0","target":"<p>你好啊</p>"}]}`}}
	h := &ReviseHandler{Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger()}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())

	if result.callbackResult == nil || len(result.callbackResult.Segments) != 1 {
		t.Fatalf("callback=%#v, want one revised segment", result.callbackResult)
	}
	if got := result.callbackResult.Segments[0]; got.TargetText != "<p>你好啊</p>" {
		t.Fatalf("TargetText=%q, want revised text", got.TargetText)
	}
	if len(result.unresolved) != 0 {
		t.Fatalf("unresolved=%v, want empty", result.unresolved)
	}
}

// 格式门禁：非 epub 格式的译文不做结构校验，同样的坏修订不受影响。
func TestReviseMarkupGuard_FormatGateSkipsNonEpub(t *testing.T) {
	doc := reviseMarkupDoc("txt")
	fb := &fakeBackend{name: "fake", responses: []string{`{"revisions":[{"id":"0","target":"<p>你好</rt>"}]}`}}
	h := &ReviseHandler{Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger()}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())

	if result.callbackResult == nil || len(result.callbackResult.Segments) != 1 {
		t.Fatalf("callback=%#v, want one revised segment（txt 不判违规）", result.callbackResult)
	}
	if got := result.callbackResult.Segments[0]; got.TargetText != "<p>你好</rt>" {
		t.Fatalf("TargetText=%q, want revised text", got.TargetText)
	}
	if len(result.unresolved) != 0 {
		t.Fatalf("unresolved=%v, want empty", result.unresolved)
	}
}
