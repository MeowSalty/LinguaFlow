package pipeline

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

type boundedHintGlossary struct{ entries []glossary.Entry }

func (g boundedHintGlossary) Lookup(context.Context, string, string, string) ([]glossary.Entry, error) {
	return g.entries, nil
}
func (g boundedHintGlossary) Add(context.Context, ...glossary.Entry) (glossary.AddResult, error) {
	return glossary.AddResult{}, nil
}
func (g boundedHintGlossary) VisitMatchesBounded(_ context.Context, _, _, _ string, _ *backend.BuildBounds, _ func(context.Context, func() error) error, yield func(glossary.Entry) error) error {
	for _, entry := range g.entries {
		if e := yield(entry); e != nil {
			return e
		}
	}
	return nil
}

type boundedHintTM struct{ matches []tm.Match }

type unboundedHintTM struct{ searches int }

func (m *unboundedHintTM) Search(context.Context, string, string, string) ([]tm.Match, error) {
	m.searches++
	return []tm.Match{{Source: "source", Target: "legacy"}}, nil
}
func (*unboundedHintTM) Add(context.Context, string, string, string, string) error { return nil }

func (m boundedHintTM) Search(context.Context, string, string, string) ([]tm.Match, error) {
	return m.matches, nil
}
func (m boundedHintTM) Add(context.Context, string, string, string, string) error { return nil }
func (m boundedHintTM) VisitMatchesBounded(_ context.Context, _, _, _ string, _ *backend.BuildBounds, _ func(context.Context, func() error) error, yield func(tm.Match) error) error {
	for _, match := range m.matches {
		if e := yield(match); e != nil {
			return e
		}
	}
	return nil
}

func TestBoundedMainContextParity(t *testing.T) {
	doc := &Document{Segments: []Segment{{Source: "zero"}, {Source: "\xffabcd"}, {Source: "target"}, {Source: "海風真夏text"}, {Source: "last", Skip: true}, {Source: "tail", StructuralOnly: true}}}
	for _, idxs := range [][]int{{2}, {1, 3}, {}, {2, 2}} {
		for _, window := range []int{0, 1, 3} {
			for _, chars := range []int{0, 1, 3} {
				b, _ := backend.NewBuildBounds(1<<20, 1<<20)
				got, e := ExpandMainContextBounded(b, doc, idxs, len(doc.Segments), window, chars, 1<<20)
				want := ExpandBatchWithContext(doc, idxs, len(doc.Segments), window, chars)
				if e != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("idxs=%v window=%d chars=%d got=%+v want=%+v err=%v", idxs, window, chars, got, want, e)
				}
			}
		}
	}
}

func TestBoundedMainContextRejectsBeforeTextBacking(t *testing.T) {
	doc := &Document{Segments: []Segment{{Source: strings.Repeat("海", 64<<10)}, {Source: "target"}}}
	b, _ := backend.NewBuildBounds(4096, 4096)
	_, e := ExpandMainContextBounded(b, doc, []int{1}, 2, math.MaxInt, 32<<10, 4096)
	if !errors.Is(e, backend.ErrBuildCapacity) || b.Snapshot().TextBytes != 0 {
		t.Fatalf("err=%v snapshot=%+v", e, b.Snapshot())
	}
}

func TestBoundedMainHintOrderDedupeAndScoreParity(t *testing.T) {
	h := &TranslateHandler{Glossary: boundedHintGlossary{entries: []glossary.Entry{{Source: "a", Target: "x", Notes: "first"}, {Source: "b", Target: "y"}, {Source: "a", Target: "x", Notes: "last"}, {Source: "blocked", Target: "z", Forbidden: true}}}, TM: boundedHintTM{matches: []tm.Match{{Source: "source", Target: "one", Score: 0.3}, {Source: "source", Target: "two", Score: 0.7}, {Source: "source", Target: "one", Score: 0.9}}}}
	doc := &Document{Segments: []Segment{{Source: "source"}, {Source: "source"}}}
	wantG, wantT := h.lookupHints(context.Background(), doc, []int{0, 1}, quietLogger())
	p, e := LookupMainHintsBounded(context.Background(), h, doc, []int{0, 1}, 1<<20, nil, quietLogger())
	if e != nil {
		t.Fatal(e)
	}
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	gotG, gotT, e := DecodeMainHintsBounded(b, p)
	if e != nil || !reflect.DeepEqual(gotG, wantG) || !reflect.DeepEqual(gotT, wantT) {
		t.Fatalf("g=%v/%v tm=%v/%v err=%v", gotG, wantG, gotT, wantT, e)
	}
}

func TestBoundedMainHintClaimBeforeEncodedBacking(t *testing.T) {
	h := &TranslateHandler{Glossary: boundedHintGlossary{entries: []glossary.Entry{{Source: "a", Target: "x", Notes: "notes"}}}}
	doc := &Document{Segments: []Segment{{Source: "source"}}}
	denied := errors.New("claim denied")
	calls := 0
	p, e := LookupMainHintsWithCreditsBounded(context.Background(), h, doc, []int{0}, 4096, nil, nil, func(n int64) error {
		calls++
		if n <= 0 {
			t.Fatal("unmeasured claim")
		}
		return denied
	})
	if !errors.Is(e, denied) || p.JSON != nil || calls != 1 {
		t.Fatalf("err=%v payload=%d claims=%d", e, len(p.JSON), calls)
	}
}

func TestBoundedMainHintCapacityDoesNotOmitNotes(t *testing.T) {
	h := &TranslateHandler{Glossary: boundedHintGlossary{entries: []glossary.Entry{{Source: "a", Target: "x", Notes: strings.Repeat("n", 64<<10)}}}}
	p, e := LookupMainHintsBounded(context.Background(), h, &Document{Segments: []Segment{{Source: "source"}}}, []int{0}, 4096, nil, nil)
	if !errors.Is(e, backend.ErrBuildCapacity) || p.JSON != nil {
		t.Fatalf("err=%v payload=%d", e, len(p.JSON))
	}
}

func TestBoundedMainHintUnsupportedTMRefusesCompleteCollection(t *testing.T) {
	legacy := &unboundedHintTM{}
	h := &TranslateHandler{Glossary: boundedHintGlossary{entries: []glossary.Entry{{Source: "a", Target: "x"}}}, TM: legacy}
	p, err := LookupMainHintsBounded(context.Background(), h, &Document{Segments: []Segment{{Source: "source"}}}, []int{0}, 1<<20, nil, quietLogger())
	if !errors.Is(err, backend.ErrBuildCapacity) || p.JSON != nil || legacy.searches != 0 {
		t.Fatalf("err=%v payload=%d legacySearches=%d", err, len(p.JSON), legacy.searches)
	}
}

func TestBoundedMainFirstPlannerParity(t *testing.T) {
	doc := &Document{Segments: make([]Segment, 40)}
	for i := range doc.Segments {
		doc.Segments[i] = Segment{Source: strings.Repeat("word ", 1+i%5), Target: "target", Translate: true, Status: "translated"}
	}
	eligible := []int{0, 1, 3, 8, 9, 10, 11, 12, 20, 22, 23}
	for _, window := range []int{0, 1, 3} {
		for _, pool := range []int{0, 1, 3} {
			for _, segments := range []int{0, 2, 4} {
				h := &TranslateHandler{BatchSize: segments, MaxWordsPerBatch: 8, FallbackShrink: 0.5, Context: ContextConfig{Enabled: window > 0, Before: window, After: window}}
				want, e := h.BuildBatches(context.Background(), doc, eligible, pool)
				if e != nil {
					t.Fatal(e)
				}
				b, _ := backend.NewBuildBounds(1<<20, 1<<20)
				got, e := PlanFirstMainBatchBounded(b, h, doc, eligible, pool)
				if e != nil || !reflect.DeepEqual(got, want[0]) {
					t.Fatalf("window=%d pool=%d batch=%d got=%v want=%v err=%v", window, pool, segments, got, want, e)
				}
			}
		}
	}
	for _, span := range []int{0, 4} {
		for _, segments := range []int{0, 3} {
			h := &ReviseHandler{BatchSize: segments, MaxWordsPerBatch: 8, MaxBatchIndexSpan: span}
			want := BuildPackedPendingBatches(doc, eligible, BatchConstraint{MaxSegments: segments, MaxWords: 8, WordCount: func(seg Segment) int { return CountWords(seg.Source) + CountWords(seg.Target) }}, span)
			b, _ := backend.NewBuildBounds(1<<20, 1<<20)
			got, e := PlanFirstMainBatchBounded(b, h, doc, eligible, 0)
			if e != nil || !reflect.DeepEqual(got, want[0]) {
				t.Fatalf("revise span=%d batch=%d got=%v want=%v err=%v", span, segments, got, want, e)
			}
		}
	}
}
