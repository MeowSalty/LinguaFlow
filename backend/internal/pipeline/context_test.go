package pipeline

import "testing"

func TestBuildContext_PrefersOriginalSource(t *testing.T) {
	doc := &Document{
		Segments: []Segment{
			{OriginalSource: "first paragraph", Source: "__LF_000001__"},
			{OriginalSource: "middle paragraph", Source: "__LF_000002__"},
			{OriginalSource: "last paragraph", Source: "__LF_000003__"},
		},
	}
	prev, next := BuildContext(doc, 1)
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
	prev, next := BuildContext(doc, 1)
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
	prev, next := BuildContextRange(doc, 1, 3)
	if prev != "s0" || next != "s4" {
		t.Errorf("got prev=%q next=%q", prev, next)
	}
}
