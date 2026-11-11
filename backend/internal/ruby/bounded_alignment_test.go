package ruby

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestBoundedAlignmentPreservesRegionDigestAndValidation(t *testing.T) {
	for _, format := range []string{"text", "html", "epub"} {
		text := "海風"
		if format != "text" {
			text = "<p>海&amp;風</p>"
		}
		items := []Item{{ID: "1", SourceBase: "海", SourceText: "うみ"}}
		old, e := NewAlignmentState(text, format, items, nil, ProtocolV2)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := backend.NewBuildBounds(1<<20, 1<<20)
		s, e := NewAlignmentStateBounded(b, text, format, items, nil, ProtocolV2)
		if e != nil || !reflect.DeepEqual(old, s) {
			t.Fatalf("state %s old=%v bounded=%v err=%v", format, old, s, e)
		}
		entry := []OutputEntry{{ID: "1", Base: "海", Text: "うみ", Kind: "phonetic", Occurrence: 1}}
		a := old.ApplyOutput([]string{"1"}, entry, true)
		got, e := s.ApplyOutputBounded(b, []string{"1"}, entry, true)
		if e != nil || !reflect.DeepEqual(a, got) {
			t.Fatalf("apply: %v %v %v", a, got, e)
		}
		expected, _, e := old.Render()
		if e != nil {
			t.Fatal(e)
		}
		rendered, _, e := s.RenderBounded(b, 1<<20)
		if e != nil || rendered != expected {
			t.Fatalf("render %s %q %q %v", format, expected, rendered, e)
		}
	}
}

func TestBoundedAlignmentControlEscapesKeepHistoricalDigest(t *testing.T) {
	translation := " A|B \"quoted\"\\\n\t\r\b\f  / suffix"
	items := []Item{{ID: "6", SourceBase: "A|B", SourceText: "quoted\\\n\t\r\b\f"}}
	old, err := NewAlignmentState(translation, "text", items, nil, ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	if err := old.ValidateBounded(b); err != nil {
		t.Fatalf("historical digest failed bounded validation before persistence: %v", err)
	}
	b, _ = backend.NewBuildBounds(1<<20, 1<<20)
	got, err := NewAlignmentStateBounded(b, translation, "text", items, nil, ProtocolV2)
	if err != nil || !reflect.DeepEqual(got, old) {
		t.Fatalf("control escapes changed historical digest/state: err=%v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("bounded digest failed historical validation: %v", err)
	}
}
func TestBoundedAlignmentRenderRejectsOutputBeforeCopy(t *testing.T) {
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	s, e := NewAlignmentStateBounded(b, "海", "text", []Item{{ID: "1", SourceBase: "海"}}, nil, 2)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.ApplyOutputBounded(b, []string{"1"}, []OutputEntry{{ID: "1", Base: "海", Text: strings.Repeat("&", 100), Kind: "phonetic", Occurrence: 1}}, true)
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = s.RenderBounded(b, 100)
	if !errors.Is(e, backend.ErrBuildCapacity) {
		t.Fatalf("expanded ruby target accepted %v", e)
	}
}
