package pipeline

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

const extractProbeResponse = `{"glossary":[{"source":"term","target":"value","notes":"verbatim notes"}]}`

type extractCaptureBackend struct {
	*fakeBackend
	runtime *ExecutionRuntime
	held    atomic.Bool
}

func (b *extractCaptureBackend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	if len(b.runtime.parse) != 0 {
		b.held.Store(true)
	}
	return b.fakeBackend.Translate(ctx, req)
}

type extractCaptureGlossary struct {
	*glossary.FileGlossary
	runtime               *ExecutionRuntime
	lookupErr, publishErr error
	published, added      atomic.Int32
	held                  atomic.Bool
}

func (g *extractCaptureGlossary) LookupSourcesBounded(ctx context.Context, text, src, tgt string, limit int64, build func(context.Context, func() error) error) ([]string, error) {
	if g.lookupErr != nil {
		return nil, g.lookupErr
	}
	return g.FileGlossary.LookupSourcesBounded(ctx, text, src, tgt, limit, build)
}

func (g *extractCaptureGlossary) ProcessResponse(ctx context.Context, in glossary.ResponseProcess) (glossary.ResponseBusiness, error) {
	g.published.Add(1)
	if len(g.runtime.parse) != 0 {
		g.held.Store(true)
	}
	if g.publishErr != nil {
		return glossary.ResponseBusiness{}, g.publishErr
	}
	return g.FileGlossary.ProcessResponse(ctx, in)
}

func (g *extractCaptureGlossary) Add(context.Context, ...glossary.Entry) (glossary.AddResult, error) {
	g.added.Add(1)
	return glossary.AddResult{}, errors.New("unexpected direct glossary add")
}

func TestBoundedStoredExtractUsesCaptureForPublication(t *testing.T) {
	for _, name := range []string{"success", "lookup_capacity", "oversize_input", "storage_failure", "provider_failure"} {
		t.Run(name, func(t *testing.T) {
			r := memoryTestRuntime(t, 1, nil)
			const handoffLimit = 2 << 20
			if err := r.SetResponseLimits(4096, handoffLimit); err != nil {
				t.Fatal(err)
			}
			provider := &extractCaptureBackend{fakeBackend: &fakeBackend{name: "extract-probe", responses: []string{extractProbeResponse}}, runtime: r}
			g := &extractCaptureGlossary{FileGlossary: glossary.NewMemory(), runtime: r}
			t.Cleanup(func() {
				if err := g.Close(); err != nil {
					t.Error(err)
				}
			})
			source := "source"
			fault := errors.New("glossary publication storage failure")
			var calls, publications int32 = 1, 1
			switch name {
			case "lookup_capacity":
				g.lookupErr = backend.ErrBuildCapacity
				calls, publications = 0, 0
			case "oversize_input":
				source = strings.Repeat("x", handoffLimit+1)
				calls, publications = 0, 0
			case "storage_failure":
				g.publishErr = &StorageError{Err: fault}
			case "provider_failure":
				provider.errs = []error{&backend.StatusError{StatusCode: http.StatusBadRequest, Err: errors.New("provider rejected extract request")}}
				publications = 0
			}
			doc := &Document{Segments: []Segment{{ID: "0", Source: source}}}
			h := &ExtractHandler{Renderer: newBootstrapRenderer(t), Backend: BindRequestBackend(provider, 1, 0, r), Glossary: g}
			result, err := RunRound(t.Context(), Round{Handler: h, Runtime: r, Concurrency: 1}, doc, nil, quietLogger(), nil)
			if provider.idx.Load() != calls || g.published.Load() != publications || g.added.Load() != 0 || provider.held.Load() || g.held.Load() || len(r.parse) != 0 {
				t.Fatalf("provider calls=%d publications=%d direct adds=%d CPU held=%t/%t/%d", provider.idx.Load(), g.published.Load(), g.added.Load(), provider.held.Load(), g.held.Load(), len(r.parse))
			}
			switch name {
			case "success":
				hits, lookupErr := g.Lookup(t.Context(), "term", "en", "zh")
				if err != nil || len(result.Resolved) != 1 || lookupErr != nil || len(hits) != 1 || hits[0].Notes != "verbatim notes" {
					t.Fatalf("result=%+v err=%v glossary=%+v, %v", result, err, hits, lookupErr)
				}
			case "lookup_capacity", "oversize_input":
				if err != nil || len(result.FailedSegments) != 1 || len(result.Unresolved) != 0 {
					t.Fatalf("capacity result=%+v err=%v", result, err)
				}
			case "storage_failure":
				if !errors.Is(err, fault) || len(result.Resolved) != 0 {
					t.Fatalf("storage failure result=%+v err=%v", result, err)
				}
			case "provider_failure":
				if err == nil || len(result.Resolved) != 0 || len(result.Unresolved) != 1 {
					t.Fatalf("provider failure result=%+v err=%v", result, err)
				}
			}
		})
	}
}

func TestUncapturedExtractResponseDoesNotPublishGlossary(t *testing.T) {
	provider := &fakeBackend{name: "unbound", responses: []string{extractProbeResponse}}
	g := glossary.NewMemory()
	h := &ExtractHandler{Renderer: newBootstrapRenderer(t), Backend: provider, Glossary: g}
	result := h.ProcessBatch(t.Context(), &Document{Segments: []Segment{{ID: "0", Source: "source"}}}, []int{0}, 0, quietLogger())
	hits, err := g.Lookup(t.Context(), "term", "en", "zh")
	if result.err == nil || provider.idx.Load() != 1 || err != nil || len(hits) != 0 {
		t.Fatalf("uncaptured response result=%+v calls=%d glossary=%+v err=%v", result, provider.idx.Load(), hits, err)
	}
}
