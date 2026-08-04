package preview

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

// mockGlossary is a simple in-memory Glossary that satisfies the interface
// for testing OverlayGlossary base layer.
type mockGlossary struct {
	entries []glossary.Entry
}

func (m *mockGlossary) Lookup(_ context.Context, text, _, _ string) ([]glossary.Entry, error) {
	var matches []glossary.Entry
	lowerText := strings.ToLower(text)
	for _, e := range m.entries {
		source := e.Source
		matched := false
		if e.CaseSensitive {
			matched = strings.Contains(text, source)
		} else {
			matched = strings.Contains(lowerText, strings.ToLower(source))
		}
		if matched {
			matches = append(matches, e)
		}
	}
	return matches, nil
}

func (m *mockGlossary) Add(_ context.Context, _ ...glossary.Entry) (glossary.AddResult, error) {
	return glossary.AddResult{}, nil
}

func TestOverlayGlossary_NilBase(t *testing.T) {
	g := NewOverlayGlossary(nil)

	res, err := g.Add(context.Background(), glossary.Entry{Source: "hello", Target: "你好"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(res.Added))
	}

	entries, err := g.Lookup(context.Background(), "hello world", "en", "zh")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry from Lookup, got %d", len(entries))
	}
	if entries[0].Target != "你好" {
		t.Fatalf("expected target '你好', got %q", entries[0].Target)
	}

	entries, err = g.Lookup(context.Background(), "nope", "en", "zh")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestOverlayGlossary_BaseAndOverlayDedup(t *testing.T) {
	base := &mockGlossary{
		entries: []glossary.Entry{
			{Source: "Hello", Target: "你好"},
			{Source: "World", Target: "世界"},
		},
	}
	g := NewOverlayGlossary(base)

	res, err := g.Add(context.Background(),
		glossary.Entry{Source: "hello", Target: "HOLA"},
		glossary.Entry{Source: "foo", Target: "bar"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 2 {
		t.Fatalf("expected 2 added (overlay is empty, both add), got %d", len(res.Added))
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("expected 0 skipped (overlay is empty), got %d", len(res.Skipped))
	}

	entries, err := g.Lookup(context.Background(), "Hello World foo", "en", "zh")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (hello, world from base; foo from overlay), got %d", len(entries))
	}

	baseWins := false
	fooFound := false
	for _, e := range entries {
		if e.Source == "Hello" || e.Source == "hello" {
			if e.Target == "你好" {
				baseWins = true
			}
		}
		if e.Source == "foo" {
			fooFound = true
		}
	}
	if !baseWins {
		t.Fatal("expected base entry '你好' to win over overlay entry")
	}
	if !fooFound {
		t.Fatal("expected overlay entry 'foo' to be present")
	}
}

func TestOverlayGlossary_AddDuplicateSameTarget(t *testing.T) {
	g := NewOverlayGlossary(nil)

	res, err := g.Add(context.Background(), glossary.Entry{Source: "hi", Target: "嗨"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(res.Added))
	}

	res, err = g.Add(context.Background(), glossary.Entry{Source: "hi", Target: "嗨"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 0 {
		t.Fatalf("expected 0 added (same target is noop), got %d", len(res.Added))
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("expected 0 skipped (same target is noop), got %d", len(res.Skipped))
	}
}

func TestOverlayGlossary_AddEmptySourceOrTarget(t *testing.T) {
	g := NewOverlayGlossary(nil)

	res, err := g.Add(context.Background(),
		glossary.Entry{Source: "", Target: "x"},
		glossary.Entry{Source: "x", Target: ""},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 0 {
		t.Fatalf("expected 0 added, got %d", len(res.Added))
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("expected 2 skipped, got %d", len(res.Skipped))
	}
	for _, s := range res.Skipped {
		if s.Reason != glossary.SkipReasonEmpty {
			t.Fatalf("expected SkipReasonEmpty, got %v", s.Reason)
		}
	}
}

func TestOverlayGlossary_Concurrency(t *testing.T) {
	g := NewOverlayGlossary(nil)

	var wg sync.WaitGroup
	n := 20

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = g.Add(context.Background(), glossary.Entry{
				Source: "key",
				Target: "val",
			})
		}()
	}

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = g.Lookup(context.Background(), "key", "en", "zh")
		}()
	}

	wg.Wait()

	entries, err := g.Lookup(context.Background(), "key", "en", "zh")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one entry after concurrent Add+Lookup")
	}
}

func TestOverlayGlossary_ForbiddenFlagDistinguishes(t *testing.T) {
	g := NewOverlayGlossary(nil)

	res, err := g.Add(context.Background(),
		glossary.Entry{Source: "foo", Target: "禁止", Forbidden: true},
		glossary.Entry{Source: "foo", Target: "bar", Forbidden: false},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 2 {
		t.Fatalf("expected 2 added (different Forbidden flags), got %d", len(res.Added))
	}
}

func TestOverlayGlossary_ExistingSameSourceDifferentTargetSkipped(t *testing.T) {
	g := NewOverlayGlossary(nil)

	res, err := g.Add(context.Background(), glossary.Entry{Source: "hi", Target: "嗨"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(res.Added))
	}

	res, err = g.Add(context.Background(), glossary.Entry{Source: "hi", Target: "你好"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 0 {
		t.Fatalf("expected 0 added, got %d", len(res.Added))
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("expected 1 skipped, got %d", len(res.Skipped))
	}
	if res.Skipped[0].Reason != glossary.SkipReasonExists {
		t.Fatalf("expected SkipReasonExists, got %v", res.Skipped[0].Reason)
	}
	if res.Skipped[0].Existing.Target != "嗨" {
		t.Fatalf("expected existing target '嗨', got %q", res.Skipped[0].Existing.Target)
	}
}

func TestNoopTM_Search(t *testing.T) {
	var tm NoopTM
	matches, err := tm.Search(context.Background(), "hello", "en", "zh")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected nil/empty matches, got %d", len(matches))
	}
}

func TestNoopTM_Add(t *testing.T) {
	var tm NoopTM
	if err := tm.Add(context.Background(), "hello", "你好", "en", "zh"); err != nil {
		t.Fatal(err)
	}
}
