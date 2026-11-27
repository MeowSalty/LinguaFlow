package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestCapturedSemanticQAProviderRetryPersistsBeforePause(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"local timeout", context.DeadlineExceeded},
		{"network failure", errors.New("network down")},
		{"provider failure", &backend.StatusError{StatusCode: 503, Err: errors.New("upstream unavailable")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := memoryTestRuntime(t, 1, nil)
			memory := NewMemoryRoundStore(nil)
			defer memory.Close()
			store := &nativeStoredRoundTestStore{MemoryRoundStore: memory, onFailure: func(cursor WorkCursor) {
				if cursor.Attempt == 1 {
					runtime.Gate.Pause()
				}
			}}
			remote := &fakeBackend{name: "test", errs: []error{tc.err}}
			rep := &recordingBatchObserver{}
			retry := backend.RetryPolicy{MaxAttempts: 1}
			h := &SemanticQAHandler{Backend: remote, Renderer: newSemanticQARenderer(t), SegmentScope: "all", Retry: retry, Reporter: rep}
			doc := semanticQADoc([]string{"translated"}, nil)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			result, err := runMemoryTestRound(t, ctx, Round{Handler: h, Runtime: runtime, Store: store, Concurrency: 1, Retry: retry}, doc, nil, quietLogger(), rep)
			if err != nil || !reflect.DeepEqual(result.Unresolved, []int{0}) || len(result.FailedSegments) != 0 || remote.idx.Load() != 1 {
				t.Fatalf("unexpected retry result %+v calls=%d err=%v", result, remote.idx.Load(), err)
			}
			state, err := memory.Load(t.Context())
			cursor := state.Cursors[0]
			if err != nil || cursor.Pool != 0 || cursor.Attempt != 1 || cursor.NextAttemptAt.IsZero() || len(rep.events) != 1 || rep.events[0].ErrorType != "backend_error" {
				t.Fatalf("retry cursor or provider event missing: cursor=%+v events=%+v err=%v", cursor, rep.events, err)
			}
		})
	}
}

const testSemanticQATmpl = `Semantic QA for {{.SourceLang}} → {{.TargetLang}}. Reply as JSON: {"issues":[...]}`

func newSemanticQARenderer(t *testing.T) *prompt.SemanticQARenderer {
	t.Helper()
	r, err := prompt.NewSemanticQARenderer(testSemanticQATmpl)
	if err != nil {
		t.Fatalf("semantic_qa renderer: %v", err)
	}
	return r
}

func semanticQADoc(statuses []string, targets []string) *Document {
	segs := make([]Segment, len(statuses))
	for i := range segs {
		target := "你好世界"
		if targets != nil {
			target = targets[i]
		}
		segs[i] = Segment{
			ID:        strconv.Itoa(i),
			Source:    "hello world",
			Target:    target,
			Status:    statuses[i],
			Translate: true,
		}
	}
	return &Document{
		SourceLang: "en",
		TargetLang: "zh",
		Segments:   segs,
		Vars:       map[string]any{},
	}
}

func TestSemanticQAHandler_BuildBatches_SelectsTranslatedEdited(t *testing.T) {
	doc := semanticQADoc(
		[]string{"translated", "approved", "edited", "pending", "rejected"},
		nil,
	)
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:   &fakeBackend{name: "fake"},
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	if len(batches) != 1 || len(batches[0]) != 2 || batches[0][0] != 0 || batches[0][1] != 2 {
		t.Fatalf("batches=%v want [[0 2]]", batches)
	}
}

func TestSemanticQAHandler_BuildBatches_SkipsEmptyTarget(t *testing.T) {
	doc := semanticQADoc(
		[]string{"translated", "translated"},
		[]string{"你好", ""},
	)
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:   &fakeBackend{name: "fake"},
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	if len(batches) != 1 || !reflect.DeepEqual(batches[0], []int{0}) {
		t.Fatalf("batches=%v want [[0]]", batches)
	}
}

func TestSemanticQAHandler_BuildBatches_SegmentScope(t *testing.T) {
	doc := semanticQADoc(
		[]string{"translated", "translated", "translated", "pending", "translated"},
		[]string{"你好", "世界", "测试", "忽略", ""},
	)
	doc.Segments[0].Issues = []qa.QualityIssue{{Code: "source_residual", Message: "residual"}}
	doc.Segments[1].Issues = []qa.QualityIssue{{Code: "calque", Message: "calque"}}
	// seg 2: no issues; seg 3: pending; seg 4: empty target

	base := func(scope string, codes []string) *SemanticQAHandler {
		return &SemanticQAHandler{
			Backend:      &fakeBackend{name: "fake"},
			Renderer:     newSemanticQARenderer(t),
			BatchSize:    10,
			SegmentScope: scope,
			IssueCodes:   codes,
			Logger:       quietLogger(),
		}
	}

	t.Run("scope all includes segments without issues", func(t *testing.T) {
		batches, err := base("all", nil).BuildBatches(context.Background(), doc, nil, 0)
		if err != nil {
			t.Fatalf("BuildBatches: %v", err)
		}
		if len(batches) != 1 || !reflect.DeepEqual(batches[0], []int{0, 1, 2}) {
			t.Fatalf("batches=%v want [[0 1 2]]", batches)
		}
	})

	t.Run("scope with_issues skips clean segments", func(t *testing.T) {
		batches, err := base("with_issues", nil).BuildBatches(context.Background(), doc, nil, 0)
		if err != nil {
			t.Fatalf("BuildBatches: %v", err)
		}
		if len(batches) != 1 || !reflect.DeepEqual(batches[0], []int{0, 1}) {
			t.Fatalf("batches=%v want [[0 1]]", batches)
		}
	})

	t.Run("scope with_issue_codes filters by code", func(t *testing.T) {
		batches, err := base("with_issue_codes", []string{"source_residual"}).BuildBatches(context.Background(), doc, nil, 0)
		if err != nil {
			t.Fatalf("BuildBatches: %v", err)
		}
		if len(batches) != 1 || !reflect.DeepEqual(batches[0], []int{0}) {
			t.Fatalf("batches=%v want [[0]]", batches)
		}
	})

	t.Run("scope with_issue_codes empty codes selects none", func(t *testing.T) {
		batches, err := base("with_issue_codes", nil).BuildBatches(context.Background(), doc, nil, 0)
		if err != nil {
			t.Fatalf("BuildBatches: %v", err)
		}
		if batches != nil {
			t.Fatalf("batches=%v want nil", batches)
		}
	})

	t.Run("unknown scope selects none", func(t *testing.T) {
		batches, err := base("weird", nil).BuildBatches(context.Background(), doc, nil, 0)
		if err != nil {
			t.Fatalf("BuildBatches: %v", err)
		}
		if len(batches) != 0 {
			t.Fatalf("batches=%v want none", batches)
		}
	})

	t.Run("empty scope selects none", func(t *testing.T) {
		batches, err := base("", nil).BuildBatches(context.Background(), doc, nil, 0)
		if err != nil {
			t.Fatalf("BuildBatches: %v", err)
		}
		if len(batches) != 0 {
			t.Fatalf("batches=%v want none", batches)
		}
	})

	t.Run("scope intersects task segment selection", func(t *testing.T) {
		selectedDoc := semanticQADoc(
			[]string{"translated", "translated"},
			[]string{"已选择", "未选择"},
		)
		selectedDoc.Segments[1].Translate = false

		batches, err := base("all", nil).BuildBatches(context.Background(), selectedDoc, nil, 0)
		if err != nil {
			t.Fatalf("BuildBatches: %v", err)
		}
		if len(batches) != 1 || !reflect.DeepEqual(batches[0], []int{0}) {
			t.Fatalf("batches=%v want [[0]]", batches)
		}
	})
}

func TestSemanticQAHandler_CapturedResponse_ProducesIssues(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	// 预置一条规则 issue，确认 handler 回调只带新产出
	doc.Segments[0].Issues = []qa.QualityIssue{
		{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"},
	}
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"issues":[{"id":"0","code":"calque","message":"借译","snippet":"hello world"}]}`},
	}
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.processStoredResponse(t.Context(), doc, []int{0}, &backend.Response{Text: fb.responses[0]}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	cb := decodeCapturedQualityBatch(t, result).Segments
	if len(cb) != 1 || len(cb[0].Issues) != 1 || cb[0].Issues[0].Code != "calque" {
		t.Fatalf("callback issues=%v want only new calque", cb)
	}
	if cb[0].Issues[0].Severity != qa.SeverityWarning {
		t.Fatalf("severity=%v want warning", cb[0].Issues[0].Severity)
	}
	// 解析不修改输入段的原有规则问题。
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("doc issues len=%d want 1 existing", len(doc.Segments[0].Issues))
	}
}

func TestSemanticQAHandler_CapturedResponse_EmptyIssuesMarksSegmentProcessed(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{name: "fake", responses: []string{`{"issues":[]}`}}
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.processStoredResponse(t.Context(), doc, []int{0}, &backend.Response{Text: fb.responses[0]}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	if decodeCapturedQualityBatch(t, result).Segments[0].Issues == nil {
		t.Fatal("successful empty scan must return a non-nil issue slice")
	}
}

func TestSemanticQAHandler_CapturedResponse_ParseFailureProducesNone(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	doc.Segments[0].Issues = []qa.QualityIssue{
		{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"},
	}
	fb := &fakeBackend{name: "fake", responses: []string{`not json at all`}}
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	// MaxAttempts=0 → transientBudget=1 → parse 失败在 attempt 0 即耗尽预算，落 unresolved（交下一池）
	result := h.processStoredResponse(t.Context(), doc, []int{0}, &backend.Response{Text: fb.responses[0]}, 0, quietLogger())
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("doc issues len=%d want 1 preserved", len(doc.Segments[0].Issues))
	}
	if result.callbackResult != nil {
		t.Fatal("parse failure must not produce callbackResult (would write back empty issues)")
	}
	if result.retry != nil {
		t.Fatal("parse failure with MaxAttempts=0 must not retry")
	}
	if !reflect.DeepEqual(result.unresolved, []int{0}) {
		t.Fatalf("unresolved=%v want [0]", result.unresolved)
	}
	if result.failedSegments != nil {
		t.Fatalf("unresolved path must not set failedSegments, got %v", result.failedSegments)
	}
}

func TestSemanticQAHandler_CapturedResponse_ParseFailureRetries(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{name: "fake", responses: []string{`not json at all`}}
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:  fb,
		Renderer: newSemanticQARenderer(t),
		Retry:    backend.RetryPolicy{MaxAttempts: 2},
		Logger:   quietLogger(),
	}
	result := h.processStoredResponse(t.Context(), doc, []int{0}, &backend.Response{Text: fb.responses[0]}, 0, quietLogger())
	if result.retry == nil || result.retry.attempt != 1 {
		t.Fatalf("retry=%v want attempt=1", result.retry)
	}
	if result.callbackResult != nil {
		t.Fatal("retry path must not produce callbackResult")
	}
	if result.failedSegments != nil {
		t.Fatalf("retry path must not set failedSegments, got %v", result.failedSegments)
	}
}

func TestSemanticQAHandler_CapturedResponse_NonTextAttachesSchema(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"issues":[]}`},
	}
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	runCapturedQualityTest(t, h, doc, nil)
	if len(fb.requests) != 1 {
		t.Fatalf("requests=%d want 1", len(fb.requests))
	}
	if fb.requests[0].ResponseFormat != "" {
		t.Fatalf("ResponseFormat should be empty, got %q", fb.requests[0].ResponseFormat)
	}
	if fb.requests[0].JSONSchema == nil {
		t.Fatal("JSONSchema should be set for non-text mode")
	}
}

func TestSemanticQAHandler_CapturedResponse_TextMode(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{"[issues]\n0 | calque | 借译"},
	}
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:      fb,
		Renderer:     newSemanticQARenderer(t),
		BatchSize:    10,
		ResponseMode: "text",
		Logger:       quietLogger(),
	}
	_, batch := runCapturedQualityTest(t, h, doc, nil)
	if batch == nil {
		t.Fatal("expected callbackResult")
	}
	if len(fb.requests) != 1 {
		t.Fatalf("requests=%d want 1", len(fb.requests))
	}
	req := fb.requests[0]
	if req.ResponseFormat != "none" {
		t.Fatalf("ResponseFormat=%q want none", req.ResponseFormat)
	}
	if req.JSONSchema != nil {
		t.Fatal("JSONSchema should be nil in text mode")
	}
	if !strings.Contains(req.User, "source_lang:") {
		t.Fatalf("text user missing source_lang:\n%s", req.User)
	}
	if !strings.Contains(req.User, "[segment]") {
		t.Fatalf("text user missing [segment]:\n%s", req.User)
	}
	if len(batch.Segments[0].Issues) != 1 ||
		batch.Segments[0].Issues[0].Code != "calque" {
		t.Fatalf("callback issues=%v", batch.Segments[0].Issues)
	}
}

func TestSemanticQAHandler_CapturedResponse_TextModeJSONFallback(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"issues":[{"id":"0","code":"naturalness","message":"生硬"}]}`},
	}
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:      fb,
		Renderer:     newSemanticQARenderer(t),
		BatchSize:    10,
		ResponseMode: "text",
		Logger:       quietLogger(),
	}
	result := h.processStoredResponse(t.Context(), doc, []int{0}, &backend.Response{Text: fb.responses[0]}, 0, quietLogger())
	if result.callbackResult == nil || len(decodeCapturedQualityBatch(t, result).Segments[0].Issues) != 1 {
		t.Fatalf("want 1 issue via JSON fallback, got %#v", result.callbackResult)
	}
	if decodeCapturedQualityBatch(t, result).Segments[0].Issues[0].Code != "naturalness" {
		t.Fatalf("code=%q", decodeCapturedQualityBatch(t, result).Segments[0].Issues[0].Code)
	}
}

// TestSemanticQAHandler_CapturedResponse_TextModeTruncatedRefused fail-closed stage
// 对截断响应恒不采纳（text 协议逐行解析无完整性信号，截断的已完成行会被当作
// 完整质检结果形成假阴性）：后端标记 Truncated 时即使 text 协议命中也必须
// 报 parse_error 走重试，事件携带截断信号。
func TestSemanticQAHandler_CapturedResponse_TextModeTruncatedRefused(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	rec := &recordingBatchObserver{}
	fb := &truncatedFakeBackend{
		fakeBackend: fakeBackend{
			name:      "fake",
			responses: []string{"[issues]\n0 | calque | 借译"},
		},
	}
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:      fb,
		Renderer:     newSemanticQARenderer(t),
		BatchSize:    10,
		ResponseMode: "text",
		Reporter:     rec,
		Logger:       quietLogger(),
	}
	_, batch := runCapturedQualityTest(t, h, doc, rec)
	if batch != nil {
		t.Fatalf("truncated text response must not be accepted as complete issues, got %#v",
			batch)
	}
	if len(rec.events) == 0 {
		t.Fatal("expected parse_error event")
	}
	evt := rec.events[0]
	if evt.ErrorType != "parse_error" || !evt.Truncated {
		t.Fatalf("event error_type=%q truncated=%v, want parse_error/true", evt.ErrorType, evt.Truncated)
	}
}

// TestSemanticQAHandler_CapturedResponse_TruncatedCompleteJSONRefused 同一 fail-closed
// 规则封住「截断点恰在完整边界、解析成功」的残余通道：JSON 本身完整可解析
// （对照 TextModeJSONFallback 的成功路径），但后端标记 Truncated 即拒绝——
// 无法排除模型还想输出更多 issue。
func TestSemanticQAHandler_CapturedResponse_TruncatedCompleteJSONRefused(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	rec := &recordingBatchObserver{}
	fb := &truncatedFakeBackend{
		fakeBackend: fakeBackend{
			name:      "fake",
			responses: []string{`{"issues":[{"id":"0","code":"naturalness","message":"生硬"}]}`},
		},
	}
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Reporter:  rec,
		Logger:    quietLogger(),
	}
	_, batch := runCapturedQualityTest(t, h, doc, rec)
	if batch != nil {
		t.Fatalf("truncated response must not be accepted even when JSON parses cleanly, got %#v",
			batch)
	}
	if len(rec.events) == 0 || rec.events[0].ErrorType != "parse_error" || !rec.events[0].Truncated {
		t.Fatalf("want parse_error event with truncated=true, got %+v", rec.events)
	}
}

func TestSemanticQAHandler_BuildBatches_PackedDiscontinuous(t *testing.T) {
	doc := semanticQADoc(
		[]string{"translated", "approved", "translated", "pending", "edited"},
		nil,
	)
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:   &fakeBackend{name: "fake"},
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	if len(batches) != 1 || !reflect.DeepEqual(batches[0], []int{0, 2, 4}) {
		t.Fatalf("batches=%v want [[0 2 4]]", batches)
	}
}

func TestSemanticQAHandler_BuildBatches_MaxBatchIndexSpan(t *testing.T) {
	doc := semanticQADoc(
		[]string{"translated", "translated", "translated", "translated", "translated"},
		nil,
	)
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:           &fakeBackend{name: "fake"},
		Renderer:          newSemanticQARenderer(t),
		BatchSize:         10,
		MaxBatchIndexSpan: 2,
		Logger:            quietLogger(),
	}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	want := [][]int{{0, 1, 2}, {3, 4}}
	if !reflect.DeepEqual(batches, want) {
		t.Fatalf("batches=%v want %v", batches, want)
	}
}

func TestSemanticQAHandler_BuildBatches_CountsSourceAndTargetWords(t *testing.T) {
	doc := semanticQADoc([]string{"translated", "translated"}, nil)
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:          &fakeBackend{name: "fake"},
		Renderer:         newSemanticQARenderer(t),
		BatchSize:        10,
		MaxWordsPerBatch: 6,
		Logger:           quietLogger(),
	}
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}
	want := [][]int{{0}, {1}}
	if !reflect.DeepEqual(batches, want) {
		t.Fatalf("batches=%v want %v", batches, want)
	}
}

func TestSemanticQAHandler_CapturedResponse_MultipleIssuesPerSegment(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{
		name: "fake",
		responses: []string{`{"issues":[
			{"id":"0","code":"calque","message":"借译","snippet":"hello"},
			{"id":"0","code":"term_fidelity","message":"术语","snippet":"world"},
			{"id":"0","code":"calque","message":"重复","snippet":"hello"}
		]}`},
	}
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.processStoredResponse(t.Context(), doc, []int{0}, &backend.Response{Text: fb.responses[0]}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	// 相同 (code, matched_text) 去重后剩 2 条
	if len(decodeCapturedQualityBatch(t, result).Segments[0].Issues) != 2 {
		t.Fatalf("want 2 issues after dedup, got %v", decodeCapturedQualityBatch(t, result).Segments[0].Issues)
	}
}

func TestSemanticQAHandler_CapturedResponse_SameCodeDifferentSnippets(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	doc.Segments[0].Target = "foo bar baz"
	fb := &fakeBackend{
		name: "fake",
		responses: []string{`{"issues":[
			{"id":"0","code":"calque","message":"a","snippet":"foo"},
			{"id":"0","code":"calque","message":"b","snippet":"bar"}
		]}`},
	}
	h := &SemanticQAHandler{SegmentScope: "all",
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.processStoredResponse(t.Context(), doc, []int{0}, &backend.Response{Text: fb.responses[0]}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	issues := decodeCapturedQualityBatch(t, result).Segments[0].Issues
	if len(issues) != 2 {
		t.Fatalf("want 2 distinct calque issues, got %v", issues)
	}
	for _, iss := range issues {
		if iss.Span == nil || iss.Span.MatchedText == "" {
			t.Fatalf("expected span, got %#v", iss)
		}
		if iss.Span.TargetStart == nil {
			t.Fatalf("expected target offsets for %q", iss.Span.MatchedText)
		}
	}
}

var _ backend.Backend = (*fakeBackend)(nil)
