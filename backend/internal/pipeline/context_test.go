package pipeline

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestBuildContext_PrefersOriginalSource(t *testing.T) {
	doc := &Document{
		Segments: []Segment{
			{OriginalSource: "first paragraph", Source: "__LF_000001__"},
			{OriginalSource: "middle paragraph", Source: "__LF_000002__"},
			{OriginalSource: "last paragraph", Source: "__LF_000003__"},
		},
	}
	prev, next := BuildContext(doc, 1, config.DefaultContextConfig())
	if prev != "first paragraph" {
		t.Errorf("prev want %q, got %q", "first paragraph", prev)
	}
	if next != "last paragraph" {
		t.Errorf("next want %q, got %q", "last paragraph", next)
	}
}

func TestBuildContext_FallbackToSource(t *testing.T) {
	doc := &Document{
		Segments: []Segment{
			{Source: "a"},
			{Source: "b"},
		},
	}
	prev, next := BuildContext(doc, 1, config.DefaultContextConfig())
	if prev != "a" {
		t.Errorf("prev want %q, got %q", "a", prev)
	}
	if next != "" {
		t.Errorf("next want empty, got %q", next)
	}
}

func TestBuildContextRange(t *testing.T) {
	doc := &Document{
		Segments: []Segment{
			{OriginalSource: "s0"},
			{OriginalSource: "s1"},
			{OriginalSource: "s2"},
			{OriginalSource: "s3"},
			{OriginalSource: "s4"},
		},
	}
	prev, next := BuildContextRange(doc, 1, 3, config.DefaultContextConfig())
	if prev != "s0" || next != "s4" {
		t.Errorf("got prev=%q next=%q", prev, next)
	}
}

func TestBuildContextRange_MultiSegment(t *testing.T) {
	doc := &Document{
		Segments: []Segment{
			{OriginalSource: "s0"},
			{OriginalSource: "s1"},
			{OriginalSource: "s2"},
			{OriginalSource: "s3"},
			{OriginalSource: "s4"},
			{OriginalSource: "s5"},
			{OriginalSource: "s6"},
		},
	}
	cfg := config.ContextConfig{Enabled: true, Before: 2, After: 2, MaxChars: 0}
	prev, next := BuildContextRange(doc, 3, 3, cfg)
	if prev != "s1\n\ns2" {
		t.Errorf("prev want %q, got %q", "s1\n\ns2", prev)
	}
	if next != "s4\n\ns5" {
		t.Errorf("next want %q, got %q", "s4\n\ns5", next)
	}
}

func TestBuildContextRange_Disabled(t *testing.T) {
	doc := &Document{
		Segments: []Segment{
			{OriginalSource: "s0"},
			{OriginalSource: "s1"},
			{OriginalSource: "s2"},
		},
	}
	cfg := config.ContextConfig{Enabled: false, Before: 1, After: 1}
	prev, next := BuildContextRange(doc, 1, 1, cfg)
	if prev != "" || next != "" {
		t.Errorf("disabled context should return empty, got prev=%q next=%q", prev, next)
	}
}

func TestContextText_MaxChars(t *testing.T) {
	seg := &Segment{Source: "This is a test. Another sentence here."}
	got := contextText(seg, 20)
	if got != "This is a test...." {
		t.Errorf("want %q, got %q", "This is a test....", got)
	}
}

func TestContextText_MaxCharsNoSentenceBoundary(t *testing.T) {
	seg := &Segment{Source: "abcdefghijklmnopqr"}
	got := contextText(seg, 10)
	if got != "abcdefghij..." {
		t.Errorf("want %q, got %q", "abcdefghij...", got)
	}
}

func TestContextText_MaxCharsZero(t *testing.T) {
	seg := &Segment{Source: "full text here"}
	got := contextText(seg, 0)
	if got != "full text here" {
		t.Errorf("want %q, got %q", "full text here", got)
	}
}
