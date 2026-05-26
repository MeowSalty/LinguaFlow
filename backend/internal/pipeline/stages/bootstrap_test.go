package stages

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

// fakeBackend 按 calls 序号依次返回预设响应。
type fakeBackend struct {
	name      string
	responses []string
	errs      []error
	idx       atomic.Int32
}

func (f *fakeBackend) Name() string { return f.name }

func (f *fakeBackend) Translate(_ context.Context, _ backend.Request) (*backend.Response, error) {
	i := int(f.idx.Add(1)) - 1
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

type fakeSelector struct{ b backend.Backend }

func (s *fakeSelector) Pick(context.Context, string) (backend.Backend, error) {
	return s.b, nil
}
func (s *fakeSelector) Plan(context.Context, string, []string) ([]backend.Backend, error) {
	return []backend.Backend{s.b}, nil
}
func (s *fakeSelector) All() []backend.Backend { return []backend.Backend{s.b} }
func (s *fakeSelector) Close() error           { return nil }

func newBootstrapRenderer(t *testing.T) *prompt.BootstrapRenderer {
	t.Helper()
	r, err := prompt.NewBootstrapRenderer()
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	return r
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBootstrap_AddsExtractedTermsToGlossary(t *testing.T) {
	doc := &pipeline.Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []pipeline.Segment{
			{OriginalSource: "Call the Gemini API to translate text."},
		},
	}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"glossary":[{"source":"Gemini","target":"哈基米","notes":"company"},{"source":"API","target":"接口","notes":""}]}`,
		},
	}
	g := glossary.NewMemory()

	s := &Bootstrap{
		Selector:         &fakeSelector{b: fb},
		Renderer:         newBootstrapRenderer(t),
		Glossary:         g,
		BatchSize:        10,
		Concurrency:      1,
		MaxTermsPerBatch: 20,
		MinSourceLen:     2,
		Logger:           discardLogger(),
	}
	if err := s.Run(context.Background(), doc); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := g.Len(); got != 2 {
		t.Fatalf("want 2 entries added, got %d (entries=%v)", got, g.SnapshotSources())
	}
}

func TestBootstrap_FiltersTooShortTerms(t *testing.T) {
	doc := &pipeline.Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []pipeline.Segment{{OriginalSource: "x"}},
	}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"glossary":[{"source":"A","target":"甲","notes":""},{"source":"AI","target":"人工智能","notes":""}]}`,
		},
	}
	g := glossary.NewMemory()

	s := &Bootstrap{
		Selector:         &fakeSelector{b: fb},
		Renderer:         newBootstrapRenderer(t),
		Glossary:         g,
		BatchSize:        10,
		Concurrency:      1,
		MaxTermsPerBatch: 20,
		MinSourceLen:     2,
		Logger:           discardLogger(),
	}
	if err := s.Run(context.Background(), doc); err != nil {
		t.Fatalf("run: %v", err)
	}
	// "A" 长度 1 被过滤，只剩 "AI"。
	if g.Len() != 1 {
		t.Fatalf("want 1 entry, got %d (entries=%v)", g.Len(), g.SnapshotSources())
	}
}

func TestBootstrap_BatchFailureDoesNotAbortStage(t *testing.T) {
	doc := &pipeline.Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []pipeline.Segment{
			{OriginalSource: "first batch text"},
			{OriginalSource: "second batch text"},
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

	s := &Bootstrap{
		Selector:         &fakeSelector{b: fb},
		Renderer:         newBootstrapRenderer(t),
		Glossary:         g,
		BatchSize:        1,
		Concurrency:      1, // 顺序，保证第一批先跑
		MaxTermsPerBatch: 20,
		MinSourceLen:     2,
		Retry:            backend.RetryPolicy{MaxAttempts: 1},
		Logger:           discardLogger(),
	}
	if err := s.Run(context.Background(), doc); err != nil {
		t.Fatalf("run should not fail on single batch error, got: %v", err)
	}
	if g.Len() != 1 {
		t.Errorf("want 1 entry from second batch, got %d (entries=%v)", g.Len(), g.SnapshotSources())
	}
}

func TestBootstrap_NoSegments(t *testing.T) {
	doc := &pipeline.Document{
		SourceLang: "en", TargetLang: "zh",
		Segments: []pipeline.Segment{{Skip: true, Source: "skipped"}, {Source: "   "}},
	}
	fb := &fakeBackend{name: "fake"}
	g := glossary.NewMemory()

	s := &Bootstrap{
		Selector:    &fakeSelector{b: fb},
		Renderer:    newBootstrapRenderer(t),
		Glossary:    g,
		BatchSize:   10,
		Concurrency: 1,
		Logger:      discardLogger(),
	}
	if err := s.Run(context.Background(), doc); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fb.idx.Load() != 0 {
		t.Errorf("backend should not be called for empty pending; calls=%d", fb.idx.Load())
	}
}
