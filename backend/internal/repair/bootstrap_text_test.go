package repair

import (
	"errors"
	"strings"
	"testing"
)

func TestTryRepairBootstrapText_WithHeader(t *testing.T) {
	in := `[glossary]
Gemini | 哈基米 | company
API | 接口
`
	entries, _, err := TryRepairBootstrapText(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2, got %d: %#v", len(entries), entries)
	}
	if entries[0].Source != "Gemini" || entries[0].Target != "哈基米" || entries[0].Notes != "company" {
		t.Errorf("entry0: %#v", entries[0])
	}
	if entries[1].Source != "API" || entries[1].Target != "接口" {
		t.Errorf("entry1: %#v", entries[1])
	}
}

func TestTryRepairBootstrapText_NoHeader(t *testing.T) {
	in := "Gemini | 哈基米 | company\nAPI | 接口\n"
	entries, _, err := TryRepairBootstrapText(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("want 2, got %d", len(entries))
	}
}

func TestTryRepairBootstrapText_EmptyList(t *testing.T) {
	in := "[glossary]\n"
	entries, _, err := TryRepairBootstrapText(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want empty, got %#v", entries)
	}
}

func TestTryRepairBootstrapText_EmptyString(t *testing.T) {
	entries, _, err := TryRepairBootstrapText("", allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want empty, got %#v", entries)
	}
}

func TestTryRepairBootstrapText_CodeFence(t *testing.T) {
	in := "```\n[glossary]\nA | B | note\n```"
	entries, repaired, err := TryRepairBootstrapText(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("entries: %#v", entries)
	}
	joined := strings.Join(repaired, ",")
	if !strings.Contains(joined, "text.strip-code-fence") {
		t.Errorf("repaired ops: %v", repaired)
	}
}

func TestTryRepairBootstrapText_Dedup(t *testing.T) {
	in := `[glossary]
A | B | note1
A | C | note2
`
	entries, _, err := TryRepairBootstrapText(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 1 || entries[0].Target != "B" {
		t.Errorf("dedupe keep first: %#v", entries)
	}
}

func TestTryRepairBootstrapText_IgnoresOtherSections(t *testing.T) {
	in := `[glossary]
A | B | note
[other]
C | D | note
`
	entries, _, err := TryRepairBootstrapText(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 1 || entries[0].Source != "A" {
		t.Errorf("entries: %#v", entries)
	}
}

func TestParseBootstrapByMode_PruneStrictEmpty(t *testing.T) {
	_, _, err := ParseBootstrapByMode("not a glossary at all", true, allOpts, true)
	if !errors.Is(err, ErrBootstrapTextEmpty) {
		t.Fatalf("want ErrBootstrapTextEmpty, got %v", err)
	}
}

func TestParseBootstrapByMode_ExtractLooseEmpty(t *testing.T) {
	entries, _, err := ParseBootstrapByMode("not a glossary at all", true, allOpts, false)
	if err != nil {
		t.Fatalf("extract allows empty: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want empty, got %#v", entries)
	}
}

func TestParseBootstrapByMode_ExplicitEmptyOK(t *testing.T) {
	entries, _, err := ParseBootstrapByMode("[glossary]\n", true, allOpts, true)
	if err != nil {
		t.Fatalf("explicit empty: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want empty, got %#v", entries)
	}
}

func TestParseBootstrapByMode_JSONFallback(t *testing.T) {
	in := `{"glossary":[{"source":"x","target":"y","notes":""}]}`
	entries, ops, err := ParseBootstrapByMode(in, true, allOpts, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 1 || entries[0].Source != "x" {
		t.Errorf("entries: %#v", entries)
	}
	found := false
	for _, op := range ops {
		if op == "text.fallback-json" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ops: %v", ops)
	}
}
