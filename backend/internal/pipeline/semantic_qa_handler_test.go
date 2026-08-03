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
	h := &SemanticQAHandler{
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
	h := &SemanticQAHandler{
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

	t.Run("unknown scope falls back to all", func(t *testing.T) {
		batches, err := base("weird", nil).BuildBatches(context.Background(), doc, nil, 0)
		if err != nil {
			t.Fatalf("BuildBatches: %v", err)
		}
		if len(batches) != 1 || !reflect.DeepEqual(batches[0], []int{0, 1, 2}) {
			t.Fatalf("batches=%v want [[0 1 2]]", batches)
		}
	})

	t.Run("empty scope defaults to all", func(t *testing.T) {
		batches, err := base("", nil).BuildBatches(context.Background(), doc, nil, 0)
		if err != nil {
			t.Fatalf("BuildBatches: %v", err)
		}
		if len(batches) != 1 || !reflect.DeepEqual(batches[0], []int{0, 1, 2}) {
			t.Fatalf("batches=%v want [[0 1 2]]", batches)
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

func TestSemanticQAHandler_ProcessBatch_ProducesIssues(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	// 预置一条规则 issue，确认 handler 回调只带新产出
	doc.Segments[0].Issues = []qa.QualityIssue{
		{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"},
	}
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"issues":[{"id":"0","code":"calque","message":"借译","snippet":"hello world"}]}`},
	}
	h := &SemanticQAHandler{
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	cb := result.callbackResult.Segments
	if len(cb) != 1 || len(cb[0].Issues) != 1 || cb[0].Issues[0].Code != "calque" {
		t.Fatalf("callback issues=%v want only new calque", cb)
	}
	if cb[0].Issues[0].Severity != qa.SeverityWarning {
		t.Fatalf("severity=%v want warning", cb[0].Issues[0].Severity)
	}
	// 内存 doc 应追加
	if len(doc.Segments[0].Issues) != 2 {
		t.Fatalf("doc issues len=%d want 2 (existing + new)", len(doc.Segments[0].Issues))
	}
}

func TestSemanticQAHandler_ProcessBatch_EmptyIssuesMarksSegmentProcessed(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{name: "fake", responses: []string{`{"issues":[]}`}}
	h := &SemanticQAHandler{
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	if result.callbackResult.Segments[0].Issues == nil {
		t.Fatal("successful empty scan must return a non-nil issue slice")
	}
}

func TestSemanticQAHandler_ProcessBatch_ParseFailureProducesNone(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	doc.Segments[0].Issues = []qa.QualityIssue{
		{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"},
	}
	fb := &fakeBackend{name: "fake", responses: []string{`not json at all`}}
	h := &SemanticQAHandler{
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
		// MaxAttempts=0 → canRetry=false → 终态
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("doc issues len=%d want 1 preserved", len(doc.Segments[0].Issues))
	}
	if result.callbackResult == nil || result.callbackResult.Segments[0].Issues != nil {
		t.Fatal("parse failure must return nil issues so persistence is skipped")
	}
	if result.retry != nil {
		t.Fatal("parse failure with MaxAttempts=0 must not retry")
	}
	if !reflect.DeepEqual(result.failedSegments, []int{0}) {
		t.Fatalf("failedSegments=%v want [0]", result.failedSegments)
	}
}

func TestSemanticQAHandler_ProcessBatch_ParseFailureRetries(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{name: "fake", responses: []string{`not json at all`}}
	h := &SemanticQAHandler{
		Backend:  fb,
		Renderer: newSemanticQARenderer(t),
		Retry:    backend.RetryPolicy{MaxAttempts: 2},
		Logger:   quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
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

func TestSemanticQAHandler_ProcessBatch_BackendErrorProducesNone(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	doc.Segments[0].Issues = []qa.QualityIssue{
		{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"},
	}
	fb := &fakeBackend{name: "fake", errs: []error{errors.New("network down")}}
	h := &SemanticQAHandler{
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
		// MaxAttempts=0 + 裸网络错误 → 终态
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if len(doc.Segments[0].Issues) != 1 {
		t.Fatalf("doc issues len=%d want 1 preserved", len(doc.Segments[0].Issues))
	}
	if result.callbackResult == nil || result.callbackResult.Segments[0].Issues != nil {
		t.Fatal("callback should carry nil issues on backend error")
	}
	if result.retry != nil {
		t.Fatal("MaxAttempts=0 must not retry")
	}
	if !reflect.DeepEqual(result.failedSegments, []int{0}) {
		t.Fatalf("failedSegments=%v want [0]", result.failedSegments)
	}
}

func TestSemanticQAHandler_ProcessBatch_NetworkErrorRetries(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{name: "fake", errs: []error{errors.New("network down")}}
	h := &SemanticQAHandler{
		Backend:  fb,
		Renderer: newSemanticQARenderer(t),
		Retry: backend.RetryPolicy{
			MaxAttempts: 2,
			Backoff:     time.Millisecond, // 缩短测试时间；minRateLimitBackoff 仍会生效
		},
		Logger: quietLogger(),
	}
	// 覆盖 minRateLimitBackoff 的等待：用短 backoff 仍会至少等 5s；测试里接受。
	// 为避免 5s 等待，改用 StatusError 500 测重试路径（同 IsRetryable）。
	// 本用例验证裸网络错误 + canRetry → retry。
	start := time.Now()
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.retry == nil || result.retry.attempt != 1 {
		t.Fatalf("retry=%v want attempt=1 (elapsed=%s)", result.retry, time.Since(start))
	}
	if result.callbackResult != nil {
		t.Fatal("retry path must not produce callbackResult")
	}
	if result.failedSegments != nil {
		t.Fatalf("retry path must not set failedSegments, got %v", result.failedSegments)
	}
}

func TestSemanticQAHandler_ProcessBatch_5xxRetriesAndExhausts(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	err500 := &backend.StatusError{StatusCode: 500, Err: errors.New("internal")}
	fb := &fakeBackend{name: "fake", errs: []error{err500}}
	h := &SemanticQAHandler{
		Backend:  fb,
		Renderer: newSemanticQARenderer(t),
		Retry: backend.RetryPolicy{
			MaxAttempts: 1,
			Backoff:     time.Millisecond,
		},
		Logger: quietLogger(),
	}

	// attempt 0 → retry
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.retry == nil || result.retry.attempt != 1 {
		t.Fatalf("attempt0 retry=%v want attempt=1", result.retry)
	}

	// attempt == MaxAttempts → 终态
	fb2 := &fakeBackend{name: "fake", errs: []error{err500}}
	h.Backend = fb2
	result = h.ProcessBatch(context.Background(), doc, []int{0}, 1, quietLogger())
	if result.retry != nil {
		t.Fatalf("exhausted must not retry, got %v", result.retry)
	}
	if !reflect.DeepEqual(result.failedSegments, []int{0}) {
		t.Fatalf("failedSegments=%v want [0]", result.failedSegments)
	}
	if result.callbackResult == nil || result.callbackResult.Segments[0].Issues != nil {
		t.Fatal("exhausted must preserve with nil issues")
	}
}

func TestSemanticQAHandler_ProcessBatch_401Terminal(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	err401 := &backend.StatusError{StatusCode: 401, Err: errors.New("unauthorized")}
	fb := &fakeBackend{name: "fake", errs: []error{err401}}
	h := &SemanticQAHandler{
		Backend:  fb,
		Renderer: newSemanticQARenderer(t),
		Retry:    backend.RetryPolicy{MaxAttempts: 5},
		Logger:   quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.retry != nil {
		t.Fatalf("401 must not retry, got %v", result.retry)
	}
	if !reflect.DeepEqual(result.failedSegments, []int{0}) {
		t.Fatalf("failedSegments=%v want [0]", result.failedSegments)
	}
}

func TestSemanticQAHandler_ProcessBatch_CtxCancelNotCounted(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fb := &fakeBackend{name: "fake", errs: []error{context.Canceled}}
	h := &SemanticQAHandler{
		Backend:  fb,
		Renderer: newSemanticQARenderer(t),
		Retry:    backend.RetryPolicy{MaxAttempts: 2},
		Logger:   quietLogger(),
	}
	result := h.ProcessBatch(ctx, doc, []int{0}, 0, quietLogger())
	if result.retry != nil {
		t.Fatalf("ctx cancel must not retry, got %v", result.retry)
	}
	if result.failedSegments != nil {
		t.Fatalf("ctx cancel must not count as scan failure, failedSegments=%v", result.failedSegments)
	}
	if result.callbackResult == nil || result.callbackResult.Segments[0].Issues != nil {
		t.Fatal("ctx cancel must preserve with nil issues")
	}
	_ = cancel
}

func TestSemanticQAHandler_ProcessBatch_LocalTimeoutRetries(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{name: "fake", errs: []error{context.DeadlineExceeded}}
	h := &SemanticQAHandler{
		Backend:  fb,
		Renderer: newSemanticQARenderer(t),
		Retry: backend.RetryPolicy{
			MaxAttempts: 2,
			Backoff:     time.Millisecond, // minRateLimitBackoff 仍生效，接受 ~5s 等待
		},
		Logger: quietLogger(),
	}
	// 父 ctx 仍活：本地 backend timeout 应按可重试处理。
	start := time.Now()
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.retry == nil || result.retry.attempt != 1 {
		t.Fatalf("local timeout retry=%v want attempt=1 (elapsed=%s)", result.retry, time.Since(start))
	}
	if result.callbackResult != nil {
		t.Fatal("retry path must not produce callbackResult")
	}
	if result.failedSegments != nil {
		t.Fatalf("retry path must not set failedSegments, got %v", result.failedSegments)
	}
}

func TestSemanticQAHandler_ProcessBatch_LocalTimeoutExhausts(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{name: "fake", errs: []error{context.DeadlineExceeded}}
	h := &SemanticQAHandler{
		Backend:  fb,
		Renderer: newSemanticQARenderer(t),
		Retry: backend.RetryPolicy{
			MaxAttempts: 1,
			Backoff:     time.Millisecond,
		},
		Logger: quietLogger(),
	}

	// attempt 0 → retry（会等 minRateLimitBackoff ~5s）
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.retry == nil || result.retry.attempt != 1 {
		t.Fatalf("attempt0 retry=%v want attempt=1", result.retry)
	}

	// attempt == MaxAttempts → 终态，计入扫描失败（不触发第二次 5s 等待）
	fb2 := &fakeBackend{name: "fake", errs: []error{context.DeadlineExceeded}}
	h.Backend = fb2
	result = h.ProcessBatch(context.Background(), doc, []int{0}, 1, quietLogger())
	if result.retry != nil {
		t.Fatalf("exhausted must not retry, got %v", result.retry)
	}
	if !reflect.DeepEqual(result.failedSegments, []int{0}) {
		t.Fatalf("failedSegments=%v want [0]", result.failedSegments)
	}
	if result.callbackResult == nil || result.callbackResult.Segments[0].Issues != nil {
		t.Fatal("exhausted must preserve with nil issues")
	}
}

func TestSemanticQAHandler_ProcessBatch_ParentDeadlineNotCounted(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	// Windows 定时器分辨率可能 >1ms，轮询直至父 deadline 过期。
	deadline := time.Now().Add(200 * time.Millisecond)
	for ctx.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("parent ctx should already be expired")
		}
		time.Sleep(time.Millisecond)
	}
	fb := &fakeBackend{name: "fake", errs: []error{context.DeadlineExceeded}}
	h := &SemanticQAHandler{
		Backend:  fb,
		Renderer: newSemanticQARenderer(t),
		Retry:    backend.RetryPolicy{MaxAttempts: 2},
		Logger:   quietLogger(),
	}
	result := h.ProcessBatch(ctx, doc, []int{0}, 0, quietLogger())
	if result.retry != nil {
		t.Fatalf("parent deadline must not retry, got %v", result.retry)
	}
	if result.failedSegments != nil {
		t.Fatalf("parent deadline must not count as scan failure, failedSegments=%v", result.failedSegments)
	}
	if result.callbackResult == nil || result.callbackResult.Segments[0].Issues != nil {
		t.Fatal("parent deadline must preserve with nil issues")
	}
}

func TestSemanticQAHandler_ProcessBatch_NonTextAttachesSchema(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"issues":[]}`},
	}
	h := &SemanticQAHandler{
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	_ = h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
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

func TestSemanticQAHandler_ProcessBatch_TextMode(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{"[issues]\n0 | calque | 借译"},
	}
	h := &SemanticQAHandler{
		Backend:      fb,
		Renderer:     newSemanticQARenderer(t),
		BatchSize:    10,
		ResponseMode: "text",
		Logger:       quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
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
	if len(result.callbackResult.Segments[0].Issues) != 1 ||
		result.callbackResult.Segments[0].Issues[0].Code != "calque" {
		t.Fatalf("callback issues=%v", result.callbackResult.Segments[0].Issues)
	}
}

func TestSemanticQAHandler_ProcessBatch_TextModeJSONFallback(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{
		name:      "fake",
		responses: []string{`{"issues":[{"id":"0","code":"naturalness","message":"生硬"}]}`},
	}
	h := &SemanticQAHandler{
		Backend:      fb,
		Renderer:     newSemanticQARenderer(t),
		BatchSize:    10,
		ResponseMode: "text",
		Logger:       quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil || len(result.callbackResult.Segments[0].Issues) != 1 {
		t.Fatalf("want 1 issue via JSON fallback, got %#v", result.callbackResult)
	}
	if result.callbackResult.Segments[0].Issues[0].Code != "naturalness" {
		t.Fatalf("code=%q", result.callbackResult.Segments[0].Issues[0].Code)
	}
}

func TestSemanticQAHandler_BuildBatches_PackedDiscontinuous(t *testing.T) {
	doc := semanticQADoc(
		[]string{"translated", "approved", "translated", "pending", "edited"},
		nil,
	)
	h := &SemanticQAHandler{
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
	h := &SemanticQAHandler{
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
	h := &SemanticQAHandler{
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

func TestSemanticQAHandler_ProcessBatch_MultipleIssuesPerSegment(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	fb := &fakeBackend{
		name: "fake",
		responses: []string{`{"issues":[
			{"id":"0","code":"calque","message":"借译","snippet":"hello"},
			{"id":"0","code":"term_fidelity","message":"术语","snippet":"world"},
			{"id":"0","code":"calque","message":"重复","snippet":"hello"}
		]}`},
	}
	h := &SemanticQAHandler{
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	// 相同 (code, matched_text) 去重后剩 2 条
	if len(result.callbackResult.Segments[0].Issues) != 2 {
		t.Fatalf("want 2 issues after dedup, got %v", result.callbackResult.Segments[0].Issues)
	}
}

func TestSemanticQAHandler_ProcessBatch_SameCodeDifferentSnippets(t *testing.T) {
	doc := semanticQADoc([]string{"translated"}, nil)
	doc.Segments[0].Target = "foo bar baz"
	fb := &fakeBackend{
		name: "fake",
		responses: []string{`{"issues":[
			{"id":"0","code":"calque","message":"a","snippet":"foo"},
			{"id":"0","code":"calque","message":"b","snippet":"bar"}
		]}`},
	}
	h := &SemanticQAHandler{
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 10,
		Logger:    quietLogger(),
	}
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}
	issues := result.callbackResult.Segments[0].Issues
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
