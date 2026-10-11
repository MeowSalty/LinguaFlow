package repair

import (
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func TestRubyRepairPreservesAmbiguousRowsAcrossJSONPaths(t *testing.T) {
	const rows = `[{"id":"other","id":"6","base":"alpha","text":"a","kind":"creative"},{"id":"9","base":"beta","text":"b","kind":"creative"}]`
	main := `{"translations":{"1":"alpha beta"},"ruby_output":{"1":` + rows + `}}`
	revision := `{"revisions":[{"id":"1","target":"alpha beta"}],"ruby_output":{"1":` + rows + `}}`
	legacy := `{"ruby_output":` + rows + `}`
	for name, input := range map[string]string{
		"main":              main,
		"main_trailing":     strings.TrimSuffix(main, "}") + ",}",
		"main_robust":       `{"` + main,
		"main_merge":        main + `{"translations":{"2":"gamma"}}`,
		"main_nested":       `{"translations":{"1":{"translation":"alpha beta","ruby_output":` + rows + `}}}`,
		"revision":          revision,
		"revision_robust":   `{"` + revision,
		"revision_trailing": strings.TrimSuffix(revision, "}") + ",}",
		"legacy":            legacy,
		"legacy_robust":     `{"` + legacy,
		"legacy_bare":       rows,
		"legacy_truncated":  strings.TrimSuffix(legacy, "}"),
	} {
		t.Run(name, func(t *testing.T) {
			var entries []ruby.OutputEntry
			switch {
			case strings.HasPrefix(name, "main"):
				result := TryRepair(input, []string{"1"}, allOpts)
				if result.Fatal || result.Trans["1"] != "alpha beta" {
					t.Fatalf("valid main result discarded: %+v", result)
				}
				entries = result.RubyOutput["1"]
			case strings.HasPrefix(name, "revision"):
				revisions, output, _, err := ParseReviseByMode(input, false, allOpts)
				if err != nil || len(revisions) != 1 || revisions[0].Target != "alpha beta" {
					t.Fatalf("valid revision discarded: %+v %v", revisions, err)
				}
				entries = output["1"]
			default:
				var err error
				entries, _, err = TryRepairRubyAlignmentForState(input, allOpts)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(entries) != 2 || !entries[0].Invalid || entries[0].ID != "6" || entries[1].Invalid {
				t.Fatalf("repair erased an ambiguous row or its valid sibling: %+v", entries)
			}
			state, err := ruby.NewAlignmentState("alpha beta", "text", []ruby.Item{{ID: "6"}, {ID: "9"}}, nil, ruby.ProtocolV2)
			if err != nil {
				t.Fatal(err)
			}
			progress := state.ApplyOutput([]string{"6", "9"}, entries, false)
			if progress.Added != 1 || len(state.Missing()) != 1 || state.Missing()[0].ID != "6" {
				t.Fatalf("ambiguous row established an alignment fact: %+v", progress)
			}
		})
	}
}

func TestOptionalRubyDuplicateFieldsKeepMainUnaligned(t *testing.T) {
	const row = `[{"id":"6","base":"alpha","text":"a","kind":"creative"}]`
	for name, annotations := range map[string]string{
		"envelope": `"ruby_output":{"1":` + row + `},"ruby_output":{"1":` + row + `}`,
		"segment":  `"ruby_output":{"1":` + row + `,"1":` + row + `,"2":` + row + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			result := TryRepair(`{"translations":{"1":"alpha","2":"alpha"},`+annotations+`}`, []string{"1", "2"}, allOpts)
			if result.Fatal || len(result.Trans) != 2 || len(result.RubyOutput["1"]) != 0 {
				t.Fatalf("ambiguous optional fields affected main translation or were trusted: %+v", result)
			}
			if name == "segment" && len(result.RubyOutput["2"]) != 1 {
				t.Fatal("unrelated segment mapping was discarded")
			}
		})
	}
}
