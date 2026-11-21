package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

type confirmedEngineBackend struct{ calls int }

func (*confirmedEngineBackend) Name() string { return "confirmed-engine" }
func (*confirmedEngineBackend) Close() error { return nil }
func (b *confirmedEngineBackend) Translate(context.Context, backend.Request) (*backend.Response, error) {
	b.calls++
	return &backend.Response{Text: `{"translations":{"1":"confirmed"}}`}, nil
}

func TestEngineTemporaryMemoryClosePreservesPendingConfirmedCallback(t *testing.T) {
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, AlignmentConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	r := pipeline.NewExecutionRuntime(a, pipeline.DefaultCandidateLimits(), nil)
	defer r.Close()
	renderer, err := prompt.NewRenderer(templates.EmbeddedPromptTemplate())
	if err != nil {
		t.Fatal(err)
	}
	b := &confirmedEngineBackend{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &pipeline.TranslateHandler{Backend: pipeline.BindRequestBackend(b, 1, 0, r), Renderer: renderer, BatchSize: 1, Logger: logger}
	e := &Engine{cfg: &Config{}, runtime: r, rounds: []pipeline.Round{{Concurrency: 1, Handler: h, Runtime: r}}, logger: logger, reporter: progress.Nop{}}
	defer e.Close()
	doc := &pipeline.Document{Format: "text", SourceLang: "en", TargetLang: "zh", Segments: []pipeline.Segment{{ID: "1", Source: "source", Translate: true, TargetIsNull: true}}}
	defer r.CloseDocument(doc)
	cause, calls := errors.New("after-confirm callback failed"), 0
	_, err = e.ExecuteRound(t.Context(), 0, doc, WithBatchHandler(func(context.Context, pipeline.BatchResult) error {
		calls++
		if calls == 1 {
			return cause
		}
		return nil
	}))
	var pending *pipeline.ConfirmationProjectionError
	if !errors.As(err, &pending) || !errors.Is(err, cause) || b.calls != 1 || calls != 1 || doc.Segments[0].Target != "confirmed" {
		t.Fatalf("engine pending=%v callback=%d HTTP=%d doc=%+v", err, calls, b.calls, doc.Segments[0])
	}
	if err := r.RetryConfirmedDocument(t.Context(), doc); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || b.calls != 1 || !doc.Segments[0].RoundWork.Completed || doc.Segments[0].Target != "confirmed" {
		t.Fatalf("retry repeated request or lost body: callbacks=%d HTTP=%d doc=%+v", calls, b.calls, doc.Segments[0])
	}
}
