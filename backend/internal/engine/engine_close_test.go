package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

// closeTrackingBackend 记录 Close 是否被调用。
type closeTrackingBackend struct {
	name       string
	closed     bool
	closeCount int
}

func (b *closeTrackingBackend) Name() string { return b.name }

func (b *closeTrackingBackend) Translate(context.Context, backend.Request) (*backend.Response, error) {
	return nil, nil
}

func (b *closeTrackingBackend) Close() error {
	b.closed = true
	b.closeCount++
	return nil
}

func TestEngineCloseOwnsRubyRetryBackends(t *testing.T) {
	shared := &closeTrackingBackend{name: "shared"}
	ruby := &closeTrackingBackend{name: "ruby"}
	e := &Engine{
		rounds:            []pipeline.Round{{Handler: &pipeline.TranslateHandler{Backend: shared}}},
		rubyRetryBackends: []backend.Backend{ruby, shared, nil, ruby},
	}
	for range 2 {
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if shared.closeCount != 1 || ruby.closeCount != 1 {
		t.Fatalf("close counts: shared=%d ruby=%d", shared.closeCount, ruby.closeCount)
	}
}

// TestEngineCloseClosesReviseHandlerBackend 防止回归：Engine.Close 的类型分支
// 曾遗漏 ReviseHandler，导致 revise 轮后端不被关闭。
func TestEngineCloseClosesReviseHandlerBackend(t *testing.T) {
	b := &closeTrackingBackend{name: "revise-backend"}
	e := &Engine{
		rounds: []pipeline.Round{
			{Handler: &pipeline.ReviseHandler{Backend: b}},
		},
	}
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !b.closed {
		t.Fatal("revise handler backend was not closed")
	}
}

type memberCloseBackend struct{ closeTrackingBackend }

func (*memberCloseBackend) Translate(context.Context, backend.Request) (*backend.Response, error) {
	return &backend.Response{Text: `{"translations":{"1":"saved"}}`}, nil
}

type memberCloseFailure struct{ error }

func (memberCloseFailure) Permanent() bool { return true }
func (e memberCloseFailure) Unwrap() error { return e.error }

type memberCloseBorrowingStore struct {
	*pipeline.MemoryRoundStore
	borrower   []byte
	allocation *backend.OwnedAllocation
	bounds     *backend.BuildBounds
	cause      error
}

func (s *memberCloseBorrowingStore) PromoteMember(ctx context.Context, _ pipeline.MemberKey, _ *pipeline.Candidate, _ pipeline.WorkCursor, _ string) error {
	var err error
	s.bounds, err = backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		return err
	}
	s.allocation, err = s.bounds.OwnAllocation(1, 1)
	if err != nil {
		return err
	}
	s.borrower = make([]byte, 1)
	return s.cause
}

func TestEngineCloseRetriesOwnedRuntimeAfterBorrowerJoins(t *testing.T) {
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, AlignmentConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	runtime := pipeline.NewExecutionRuntime(admission, pipeline.DefaultCandidateLimits(), nil)
	root, err := resourcebudget.New(resourcebudget.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err = admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "engine-close-retry"}); err != nil {
		t.Fatal(err)
	}
	renderer, err := prompt.NewRenderer(templates.EmbeddedPromptTemplate())
	if err != nil {
		t.Fatal(err)
	}
	b := &memberCloseBackend{closeTrackingBackend: closeTrackingBackend{name: "member-close"}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &pipeline.TranslateHandler{Backend: pipeline.BindRequestBackend(b, 1, 0, runtime), Renderer: renderer, BatchSize: 1, Logger: logger}
	round := pipeline.Round{Concurrency: 1, Handler: handler}
	e := &Engine{runtime: runtime, ownRuntime: true, rounds: []pipeline.Round{round}}
	store := &memberCloseBorrowingStore{MemoryRoundStore: pipeline.NewMemoryRoundStore(nil), cause: memberCloseFailure{errors.New("save unavailable")}}
	doc := &pipeline.Document{Format: "txt", SourceLang: "en", TargetLang: "zh", Segments: []pipeline.Segment{{ID: "1", Source: "source", Translate: true, Status: "pending", TargetIsNull: true}}}
	t.Cleanup(func() {
		store.borrower = nil
		if store.allocation != nil {
			store.allocation.Release()
		}
		if store.bounds != nil {
			store.bounds.CloseRetired()
		}
		if err := runtime.CloseDocument(doc); err != nil {
			t.Error(err)
		}
		if err := e.Close(); err != nil {
			t.Error(err)
		}
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := pipeline.RunStagedRound(t.Context(), round, doc, store, runtime, logger, nil); !errors.Is(err, store.cause) {
		t.Fatalf("save failure lost: %v", err)
	}
	var lifecycle *backend.ProviderAllocationLifecycleError
	if err := e.Close(); !errors.As(err, &lifecycle) || e.runtimeClosed || root.Snapshot(1).Instance.MainResponseOwners != 1 || b.closeCount != 1 {
		t.Fatalf("first Close must retain owner and close backend once: err=%v settled=%t root=%+v closes=%d", err, e.runtimeClosed, root.Snapshot(1).Instance, b.closeCount)
	}
	store.borrower = nil
	store.allocation.Release()
	store.bounds.CloseRetired()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if !e.runtimeClosed || root.Snapshot(1).Instance.MainResponseOwners != 0 || b.closeCount != 1 {
		t.Fatalf("Close retry failed to settle runtime once: root=%+v closes=%d", root.Snapshot(1).Instance, b.closeCount)
	}
	if headers, _, err := store.ResponseHeaders(t.Context(), 0, 16); err != nil || len(headers) != 1 {
		t.Fatalf("Close erased independent reliable response: %v", err)
	}
	if err := runtime.CloseDocument(doc); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if got := root.Snapshot(1).Instance; got.PayloadBytes != 0 || got.UnconfirmedMembers != 0 || got.MainResponseOwners != 0 {
		t.Fatalf("last reference retained inventory: %+v", got)
	}
}
