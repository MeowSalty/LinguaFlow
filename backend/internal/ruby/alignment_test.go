package ruby

import (
	"encoding/json"
	"strings"
	"testing"
)

func alignmentState(t *testing.T, text, format string, items ...Item) *AlignmentState {
	t.Helper()
	s, err := NewAlignmentState(text, format, items, nil, ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAlignmentRequestSubsetAndDuplicateRejection(t *testing.T) {
	s := alignmentState(t, "alpha beta gamma", "text", Item{ID: "1"}, Item{ID: "2"}, Item{ID: "3"})
	progress := s.ApplyOutput([]string{"2", "3"}, []OutputEntry{
		{ID: "1", Base: "alpha", Text: "a", Kind: "phonetic", Occurrence: 1},
		{Base: "beta", Text: "b", Kind: "phonetic", Occurrence: 1},
		{ID: "2", Base: "beta", Text: "b", Kind: "phonetic", Occurrence: 1},
		{ID: "2", Invalid: true},
		{ID: "3", Base: "gamma", Text: "g", Kind: "phonetic", Occurrence: 1},
	}, true)
	if progress.Added != 1 || len(s.Missing()) != 2 || s.Verified[0].ID != "3" {
		t.Fatalf("bad partial acceptance: %+v %+v", progress, s)
	}
	if progress.Rejected["2"] != "duplicate_id" {
		t.Fatal("malformed duplicate did not reject both rows")
	}
	before := s.Verified[0]
	s.ApplyOutput([]string{"3"}, []OutputEntry{{ID: "3", Base: "alpha", Text: "changed", Kind: "creative", Occurrence: 1}}, true)
	if s.Verified[0] != before {
		t.Fatal("verified mapping overwritten")
	}
}

func TestAlignmentRegionsEntitiesRubyAndOccurrence(t *testing.T) {
	s := alignmentState(t, `<span title="行">行&amp;行</span><ruby class="old">行<rt>old</rt></ruby><b>行</b>`, "epub", Item{ID: "1"}, Item{ID: "2"})
	if len(s.Regions) != 2 || s.Regions[0].Text != "行&行" || s.Regions[1].Text != "行" {
		t.Fatalf("unsafe view: %+v", s.Regions)
	}
	progress := s.ApplyOutput([]string{"1", "2"}, []OutputEntry{
		{ID: "1", Base: "行", Text: "xíng<&", Kind: "phonetic", Occurrence: 3},
		{ID: "2", Base: "&", Text: "amp", Kind: "semantic", Occurrence: 1},
	}, true)
	if progress.Added != 2 {
		t.Fatalf("rejected safe positions: %+v", progress)
	}
	out, result, err := s.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsFull() || !strings.Contains(out, `<ruby>&amp;<rt>amp</rt></ruby>`) || !strings.Contains(out, `<b><ruby>行<rt>xíng&lt;&amp;</rt></ruby></b>`) || !strings.Contains(out, `title="行"`) {
		t.Fatalf("unsafe render: %s %+v", out, result)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored AlignmentState
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if err = restored.Validate(); err != nil {
		t.Fatalf("valid draft did not recover: %v", err)
	}
	restored.Translation += "changed"
	if restored.Validate() == nil {
		t.Fatal("changed candidate accepted")
	}
}

func TestAlignmentConflictRejectsBothIndependentOfResponseOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		s := alignmentState(t, "abc next", "text", Item{ID: "a"}, Item{ID: "b"}, Item{ID: "c"})
		entries := []OutputEntry{{ID: "a", Base: "abc", Text: "x", Kind: "phonetic", Occurrence: 1}, {ID: "b", Base: "bc", Text: "y", Kind: "phonetic", Occurrence: 1}}
		if reverse {
			entries[0], entries[1] = entries[1], entries[0]
		}
		progress := s.ApplyOutput([]string{"a", "b"}, entries, true)
		if progress.Added != 0 || len(s.Missing()) != 3 {
			t.Fatalf("overlap accepted: %+v", progress)
		}
		s.ApplyOutput([]string{"a"}, entries[:0], true)
		progress = s.ApplyOutput([]string{"a"}, []OutputEntry{{ID: "a", Base: "abc", Text: "x", Kind: "phonetic", Occurrence: 1}}, true)
		if progress.Added != 1 {
			t.Fatal(progress)
		}
		progress = s.ApplyOutput([]string{"b"}, []OutputEntry{{ID: "b", Base: "bc", Text: "y", Kind: "phonetic", Occurrence: 1}}, true)
		if progress.Added != 0 || len(s.Verified) != 1 {
			t.Fatal("new mapping damaged old fact")
		}
	}
}

func TestAlignmentUniqueFallbackAndMissingOccurrence(t *testing.T) {
	for _, tt := range []struct {
		name, text, base, source string
		occurrence               int
		require                  bool
		want                     bool
		fallback                 bool
	}{
		{"unique main", "one", "one", "", 0, false, true, false},
		{"ambiguous main", "one one", "one", "", 0, false, false, false},
		{"strict missing occurrence", "one", "one", "", 0, true, false, false},
		{"unique fallback", "source", "absent", "source", 7, true, true, true},
		{"ambiguous fallback", "source source", "absent", "source", 1, true, false, false},
		{"invalid occurrence no fallback", "one source", "one", "source", 2, true, false, false},
		{"nonexistent base", "one", "absent", "", 1, true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := alignmentState(t, tt.text, "text", Item{ID: "1", SourceBase: tt.source})
			p := s.ApplyOutput([]string{"1"}, []OutputEntry{{ID: "1", Base: tt.base, Text: "read", Kind: "phonetic", Occurrence: tt.occurrence}}, tt.require)
			if (p.Added == 1) != tt.want {
				t.Fatalf("progress=%+v", p)
			}
			if tt.want && s.Verified[0].SourceFallback != tt.fallback {
				t.Fatal("fallback fact missing")
			}
		})
	}
}

func TestAlignmentRegionWhitespaceAndNoCrossNodeMatch(t *testing.T) {
	s := alignmentState(t, "<i>A\r\nB</i><b>C</b>", "epub", Item{ID: "1"}, Item{ID: "2"})
	if s.Regions[0].Text != "A\nB" {
		t.Fatalf("XML newline view=%q", s.Regions[0].Text)
	}
	p := s.ApplyOutput([]string{"1", "2"}, []OutputEntry{{ID: "1", Base: "A\nB", Text: " x ", Kind: "creative", Occurrence: 1}, {ID: "2", Base: "BC", Text: "x", Kind: "phonetic", Occurrence: 1}}, true)
	if p.Added != 1 || len(s.Missing()) != 1 {
		t.Fatalf("cross node match or newline error: %+v", p)
	}
	out, _, err := s.Render()
	if err != nil || !strings.Contains(out, "<ruby>A\r\nB<rt> x </rt></ruby>") {
		t.Fatalf("raw content changed: %q %v", out, err)
	}
	plain := alignmentState(t, " A\r\nB &amp; ", "text", Item{ID: "1"})
	if plain.Regions[0].Text != plain.Translation {
		t.Fatal("plain text normalized")
	}
}

func TestAlignmentPreservationCannotChangeAfterConstruction(t *testing.T) {
	s, err := NewAlignmentState("one two", "text", []Item{{ID: "1", Kind: "creative"}, {ID: "2", Kind: "phonetic"}}, map[string]bool{"creative": true}, ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	p := s.ApplyOutput([]string{"1", "2"}, []OutputEntry{{ID: "1", Base: "one", Text: "x", Kind: "phonetic", Occurrence: 1}, {ID: "2", Base: "two", Text: "y", Kind: "creative", Occurrence: 1}}, true)
	if p.Added != 1 || len(s.Missing()) != 0 {
		t.Fatalf("kind changed preserve decision: %+v", p)
	}
	_, stats, err := s.Render()
	if err != nil || stats.Total != 1 {
		t.Fatalf("bad conservation: %+v %v", stats, err)
	}
}

func TestAlignmentJSONPartialRowsPreserveWhitespaceAndDuplicates(t *testing.T) {
	entries, err := ParseAlignmentJSONV2(`{"ruby_output":[{"id":"1","base":" a ","text":" x ","kind":"creative","occurrence":1},{"id":"2","base":"b","text":"x","kind":"phonetic","occurrence":"wrong"},{"id":"2","base":"b","text":"x","kind":"phonetic","occurrence":1}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Base != " a " || entries[0].Text != " x " || !entries[1].Invalid || entries[1].ID != "2" {
		t.Fatalf("partial parse=%+v", entries)
	}
	s := alignmentState(t, " a b", "text", Item{ID: "1"}, Item{ID: "2"})
	p := s.ApplyOutput([]string{"1", "2"}, entries, true)
	if p.Added != 1 || p.Rejected["2"] != "duplicate_id" {
		t.Fatal(p)
	}
}

func TestAlignmentTextV2QuotedRoundTrip(t *testing.T) {
	base := " A|B \"quoted\"\\\n"
	text := " read | \\ \n "
	b, _ := json.Marshal(base)
	r, _ := json.Marshal(text)
	line := string(b) + " | " + string(r) + ` | "creative" | "6" | 2`
	entries := ParseAlignmentTextV2(line)
	if len(entries) != 1 || entries[0].Invalid || entries[0].Base != base || entries[0].Text != text || entries[0].Occurrence != 2 {
		t.Fatalf("roundtrip=%+v", entries)
	}
	for _, invalid := range []string{`a | b | phonetic | 6 | 1`, `"a" | "b" | "phonetic" | "6" | 0`, `"a" | "b" | "phonetic" | "6" | 1.5`, `"a\q" | "b" | "phonetic" | "6" | 1`, `"a" | "b" | "phonetic" | "6"`} {
		got := ParseAlignmentTextV2(invalid)
		if len(got) != 1 || !got[0].Invalid {
			t.Fatalf("accepted invalid line: %s", invalid)
		}
	}
}
