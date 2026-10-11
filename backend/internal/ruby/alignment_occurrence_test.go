package ruby

import (
	"encoding/json"
	"testing"
)

func TestAlignmentOccurrenceKeepsUnsafeEntityInOrdinalList(t *testing.T) {
	for _, occurrence := range []int{1, 2} {
		s := alignmentState(t, "<p>&NotEqualTilde; ≂</p>", "html", Item{ID: "1"})
		p := s.ApplyOutput([]string{"1"}, []OutputEntry{{ID: "1", Base: "≂", Text: "read", Kind: "phonetic", Occurrence: occurrence}}, true)
		if occurrence == 1 {
			if p.Added != 0 || p.Rejected["1"] != "unsafe_text_boundary" {
				t.Fatalf("partial entity accepted: %+v", p)
			}
			continue
		}
		if p.Added != 1 || s.Verified[0].Occurrence != 2 {
			t.Fatalf("later occurrence renumbered: %+v %+v", p, s.Verified)
		}
		out, _, err := s.Render()
		if err != nil || out != "<p>&NotEqualTilde; <ruby>≂<rt>read</rt></ruby></p>" {
			t.Fatalf("wrong raw insertion: %q %v", out, err)
		}
	}
}

func TestAlignmentSourceFallbackCountsLegalPositionsWithoutRenumbering(t *testing.T) {
	s := alignmentState(t, "<p>&NotEqualTilde; ≂</p>", "html", Item{ID: "1", SourceBase: "≂"})
	p := s.ApplyOutput([]string{"1"}, []OutputEntry{{ID: "1", Base: "missing", Text: "read", Kind: "creative", Occurrence: 8}}, true)
	if p.Added != 1 || !s.Verified[0].SourceFallback || s.Verified[0].Occurrence != 2 {
		t.Fatalf("unique legal fallback not recorded: %+v %+v", p, s.Verified)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored AlignmentState
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.Validate(); err != nil {
		t.Fatalf("fallback not recoverable: %v", err)
	}
}

func TestAlignmentHTMLReferencesCannotBeSplitWithoutSemicolon(t *testing.T) {
	s := alignmentState(t, "<p>&copycat &amp &#65 &notit;</p>", "html", Item{ID: "1"}, Item{ID: "2"}, Item{ID: "3"})
	if len(s.Regions) != 1 || s.Regions[0].Text != "©cat & A ¬it;" {
		t.Fatalf("HTML references were not decoded: %+v", s.Regions)
	}
	p := s.ApplyOutput([]string{"1", "2", "3"}, []OutputEntry{
		{ID: "1", Base: "copy", Text: "unsafe", Kind: "creative", Occurrence: 1},
		{ID: "2", Base: "cat", Text: "suffix", Kind: "creative", Occurrence: 1},
		{ID: "3", Base: "A", Text: "letter", Kind: "creative", Occurrence: 1},
	}, true)
	if p.Added != 2 || len(s.Missing()) != 1 || s.Missing()[0].ID != "1" {
		t.Fatalf("entity spelling was matched or plain suffix lost: %+v", p)
	}
	out, _, err := s.Render()
	if err != nil || out != "<p>&copy<ruby>cat<rt>suffix</rt></ruby> &amp <ruby>&#65<rt>letter</rt></ruby> &notit;</p>" {
		t.Fatalf("entity raw mapping changed: %q %v", out, err)
	}
}

func TestAlignmentV2RejectsDuplicateEnvelopeFields(t *testing.T) {
	if _, err := ParseAlignmentJSONV2(`{"ruby_output":[{"id":"1","base":"a","text":"x","kind":"creative","occurrence":1}],"ruby_output":[]}`); err == nil {
		t.Fatal("ambiguous duplicate envelope accepted")
	}
}
