package ruby

import (
	"strings"
	"testing"
)

func TestAlignmentTextV2MalformedRowsCannotHideDuplicateID(t *testing.T) {
	for name, malformed := range map[string]string{
		"base_escape":    `"alpha\q" | "a" | "creative" | "6" | 1`,
		"text_escape":    `"alpha" | "a\u000Z" | "creative" | "6" | 1`,
		"kind_escape":    `"alpha" | "a" | "creat\qive" | "6" | 1`,
		"unquoted_base":  `alpha | "a" | "creative" | "6" | 1`,
		"invalid_utf8":   "\"alpha\xff\" | \"a\" | \"creative\" | \"6\" | 1",
		"missing_number": `"alpha" | "a" | "creative" | "6"`,
	} {
		t.Run(name, func(t *testing.T) {
			entries := ParseAlignmentTextV2(strings.Join([]string{
				`"alpha" | "a" | "creative" | "6" | 1`,
				malformed,
				`"beta" | "b" | "creative" | "9" | 1`,
			}, "\n"))
			if len(entries) != 3 || !entries[1].Invalid || entries[1].ID != "6" {
				t.Fatalf("identifiable invalid row was lost: %+v", entries)
			}
			state := alignmentState(t, "alpha beta", "text", Item{ID: "6"}, Item{ID: "9"})
			progress := state.ApplyOutput([]string{"6", "9"}, entries, true)
			if progress.Added != 1 || progress.Rejected["6"] != "duplicate_id" || len(state.Missing()) != 1 || state.Missing()[0].ID != "6" {
				t.Fatalf("invalid duplicate manufactured an alignment fact: %+v %+v", entries, progress)
			}
		})
	}
}

func TestPrimaryTextMalformedRowsCannotHideDuplicateID(t *testing.T) {
	entries := ParseSectionRubyOutput([]string{
		`1: "alpha" | "a" | "creative" | "6"`,
		`1: "alpha\q" | "a" | "creative" | "6"`,
		`1: "beta" | "b" | "creative" | "9"`,
	})["1"]
	state := alignmentState(t, "alpha beta", "text", Item{ID: "6"}, Item{ID: "9"})
	progress := state.ApplyOutput([]string{"6", "9"}, entries, false)
	if progress.Added != 1 || progress.Rejected["6"] != "duplicate_id" || len(state.Missing()) != 1 || state.Missing()[0].ID != "6" {
		t.Fatalf("optional main mapping hid an invalid duplicate: %+v %+v", entries, progress)
	}
}
