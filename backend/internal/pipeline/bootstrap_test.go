package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

// fakeBackend 按 calls 序号依次返回预设响应。
type fakeBackend struct {
	name      string
	responses []string
	errs      []error
	idx       atomic.Int32
	requests  []backend.Request
}

func (f *fakeBackend) Name() string { return f.name }

func (f *fakeBackend) Translate(_ context.Context, req backend.Request) (*backend.Response, error) {
	i := int(f.idx.Add(1)) - 1
	f.requests = append(f.requests, req)
	var (
		resp string
		err  error
	)
	if i < len(f.responses) {
		resp = f.responses[i]
	}
	if i < len(f.errs) {
		err = f.errs[i]
	}
	if err != nil {
		return nil, err
	}
	return &backend.Response{Text: resp}, nil
}

func (f *fakeBackend) Close() error { return nil }

// testBootstrapTmpl 是测试用的最小 bootstrap 模板。
const testBootstrapTmpl = `You are LinguaFlow, a glossary-bootstrap assistant.
Task: extract domain-specific terms from {{.SourceLang}} to {{.TargetLang}}.
Return AT MOST {{.MaxTerms}} entries. Reply as JSON: {"glossary":[...]}.`

func newBootstrapRenderer(t *testing.T) *prompt.BootstrapRenderer {
	t.Helper()
	r, err := prompt.NewBootstrapRenderer(testBootstrapTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	return r
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestExtractHandler_AddsExtractedTermsToGlossary(t *testing.T) {
	doc := &Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []Segment{
			{ID: "0", Source: "Call the Gemini API to translate text.", Translate: true},
		},
	}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"glossary":[{"source":"Gemini","target":"哈基米","notes":"company"},{"source":"API","target":"接口","notes":""}]}`,
		},
	}
	g := glossary.NewMemory()

	h := &ExtractHandler{
		Backends:             []backend.Backend{fb},
		Renderer:             newBootstrapRenderer(t),
		Glossary:             g,
		BatchSize:            10,
		MaxTermsPer1000Chars: 25.0,
		MinSourceLen:         2,
		Logger:               discardLogger(),
	}

	round := Round{
		Concurrency: 1,
		Handler:     h,
	}

	_, err := RunRound(context.Background(), round, doc, nil, discardLogger(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := g.Len(); got != 2 {
		t.Fatalf("want 2 entries added, got %d (entries=%v)", got, g.SnapshotSources())
	}
}

func TestExtractHandler_FiltersTooShortTerms(t *testing.T) {
	doc := &Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []Segment{{ID: "0", Source: "x", Translate: true}},
	}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"glossary":[{"source":"A","target":"甲","notes":""},{"source":"AI","target":"人工智能","notes":""}]}`,
		},
	}
	g := glossary.NewMemory()

	h := &ExtractHandler{
		Backends:             []backend.Backend{fb},
		Renderer:             newBootstrapRenderer(t),
		Glossary:             g,
		BatchSize:            10,
		MaxTermsPer1000Chars: 25.0,
		MinSourceLen:         2,
		Logger:               discardLogger(),
	}

	round := Round{
		Concurrency: 1,
		Handler:     h,
	}

	_, err := RunRound(context.Background(), round, doc, nil, discardLogger(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// "A" 长度 1 被过滤，只剩 "AI"。
	if g.Len() != 1 {
		t.Fatalf("want 1 entry, got %d (entries=%v)", g.Len(), g.SnapshotSources())
	}
}

func TestExtractHandler_BatchFailureDoesNotAbortStage(t *testing.T) {
	doc := &Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []Segment{
			{ID: "0", Source: "first batch text", Translate: true},
			{ID: "1", Source: "second batch text", Translate: true},
		},
	}
	// BatchSize=1 ⇒ 两批；第一批返回错误，第二批正常。
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			"", // 第一批被 err 覆盖，文本无关
			`{"glossary":[{"source":"second","target":"二","notes":""}]}`,
		},
		errs: []error{errors.New("simulated failure"), nil},
	}
	g := glossary.NewMemory()

	h := &ExtractHandler{
		Backends:             []backend.Backend{fb},
		Renderer:             newBootstrapRenderer(t),
		Glossary:             g,
		BatchSize:            1,
		MaxTermsPer1000Chars: 25.0,
		MinSourceLen:         2,
		Retry:                backend.RetryPolicy{MaxAttempts: 1},
		Logger:               discardLogger(),
	}

	round := Round{
		Concurrency: 1,
		Handler:     h,
	}

	_, err := RunRound(context.Background(), round, doc, nil, discardLogger(), nil)
	if err != nil {
		t.Fatalf("run should not fail on single batch error, got: %v", err)
	}
	if g.Len() != 1 {
		t.Errorf("want 1 entry from second batch, got %d (entries=%v)", g.Len(), g.SnapshotSources())
	}
}

func TestExtractHandler_AllBatchesFailed(t *testing.T) {
	doc := &Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []Segment{
			{ID: "0", Source: "first batch text", Translate: true},
			{ID: "1", Source: "second batch text", Translate: true},
		},
	}
	// BatchSize=1 ⇒ 两批；全部返回错误。
	fb := &fakeBackend{
		name: "fake",
		errs: []error{errors.New("failure 1"), errors.New("failure 2")},
	}
	g := glossary.NewMemory()

	h := &ExtractHandler{
		Backends:             []backend.Backend{fb},
		Renderer:             newBootstrapRenderer(t),
		Glossary:             g,
		BatchSize:            1,
		MaxTermsPer1000Chars: 25.0,
		MinSourceLen:         2,
		Retry:                backend.RetryPolicy{MaxAttempts: 1},
		Logger:               discardLogger(),
	}

	round := Round{
		Concurrency: 1,
		Handler:     h,
	}

	_, err := RunRound(context.Background(), round, doc, nil, discardLogger(), nil)
	if err == nil {
		t.Fatal("expected error when all batches fail, got nil")
	}
}

func TestExtractHandler_NoSegments(t *testing.T) {
	doc := &Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []Segment{{Skip: true, Source: "skipped"}, {Source: "   "}},
	}
	fb := &fakeBackend{name: "fake"}
	g := glossary.NewMemory()

	h := &ExtractHandler{
		Backends:  []backend.Backend{fb},
		Renderer:  newBootstrapRenderer(t),
		Glossary:  g,
		BatchSize: 10,
		Logger:    discardLogger(),
	}

	round := Round{
		Concurrency: 1,
		Handler:     h,
	}

	_, err := RunRound(context.Background(), round, doc, nil, discardLogger(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if fb.idx.Load() != 0 {
		t.Errorf("backend should not be called for empty pending; calls=%d", fb.idx.Load())
	}
}

func TestExtractHandler_SendAll_BothZero(t *testing.T) {
	doc := &Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []Segment{
			{ID: "0", Source: "First segment with Gemini API.", Translate: true},
			{ID: "1", Source: "Second segment with OAuth2 authentication.", Translate: true},
			{ID: "2", Source: "Third segment with JWT tokens.", Translate: true},
		},
	}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"glossary":[{"source":"Gemini","target":"哈基米","notes":""},{"source":"OAuth2","target":"OAuth2","notes":""},{"source":"JWT","target":"JWT","notes":""}]}`,
		},
	}
	g := glossary.NewMemory()

	h := &ExtractHandler{
		Backends:             []backend.Backend{fb},
		Renderer:             newBootstrapRenderer(t),
		Glossary:             g,
		BatchSize:            0,
		MaxWordsPerBatch:     0,
		MaxTermsPer1000Chars: 25.0,
		MinSourceLen:         2,
		Logger:               discardLogger(),
	}

	round := Round{
		Concurrency: 1,
		Handler:     h,
	}

	result, err := RunRound(context.Background(), round, doc, nil, discardLogger(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Unresolved != nil && len(result.Unresolved) > 0 {
		t.Errorf("expected no unresolved, got %v", result.Unresolved)
	}
	if g.Len() != 3 {
		t.Errorf("want 3 entries, got %d (entries=%v)", g.Len(), g.SnapshotSources())
	}
	// Should be exactly 1 LLM call (single batch)
	if got := fb.idx.Load(); got != 1 {
		t.Errorf("expected 1 backend call (send-all mode), got %d", got)
	}
}

func TestExtractHandler_MaxWordsPerBatch(t *testing.T) {
	doc := &Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []Segment{
			{ID: "0", Source: "Use the Gemini API for translation.", Translate: true},       // ~6 words
			{ID: "1", Source: "Configure OAuth2 authentication properly.", Translate: true}, // ~5 words
			{ID: "2", Source: "Implement JWT token validation.", Translate: true},           // ~5 words
		},
	}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"glossary":[{"source":"Gemini","target":"哈基米","notes":""}]}`,
			`{"glossary":[{"source":"OAuth2","target":"OAuth2","notes":""},{"source":"JWT","target":"JWT","notes":""}]}`,
		},
	}
	g := glossary.NewMemory()

	// MaxWordsPerBatch=10 means each batch can have at most 10 words
	// With ~6, ~5, ~5 words per segment, this should produce 2-3 batches
	h := &ExtractHandler{
		Backends:             []backend.Backend{fb},
		Renderer:             newBootstrapRenderer(t),
		Glossary:             g,
		BatchSize:            0,  // no segment count limit
		MaxWordsPerBatch:     10, // word count limit only
		MaxTermsPer1000Chars: 25.0,
		MinSourceLen:         2,
		Logger:               discardLogger(),
	}

	round := Round{
		Concurrency: 1,
		Handler:     h,
	}

	_, err := RunRound(context.Background(), round, doc, nil, discardLogger(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if g.Len() != 3 {
		t.Errorf("want 3 entries, got %d (entries=%v)", g.Len(), g.SnapshotSources())
	}
}
