package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

func TestCLIInlineTermExtractionDefaultsAndPresence(t *testing.T) {
	disabled := execution.DefaultInlineTermExtraction()
	enabled := disabled
	enabled.Enabled = true
	custom := execution.InlineTermExtractionConfig{Enabled: false, MaxTermsPer1000Words: 7.5, MinSourceLen: 4, ConflictStrategy: "off"}
	for _, tc := range []struct {
		name, input string
		want        *execution.InlineTermExtractionConfig
	}{
		{name: "omitted"},
		{name: "empty", input: "{}", want: &disabled},
		{name: "enabled only", input: "{enabled: true}", want: &enabled},
		{name: "disabled custom", input: "{enabled: false, max_terms_per_1000_words: 7.5, min_source_len: 4, conflict_strategy: off}", want: &custom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := minimalTranslation
			if tc.input != "" {
				doc = strings.Replace(doc, "translate: {}", "translate:\n        inline_term_extraction: "+tc.input, 1)
			}
			cfg, err := ResolveCLIConfig(translationInput(t, doc))
			if err != nil {
				t.Fatal(err)
			}
			got := cfg.Execution.Rounds[0].Translate.InlineTermExtraction
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("inline extraction=%+v want=%+v", got, tc.want)
			}
		})
	}
}

func TestCLIInlineTermExtractionRejectsInvalidFields(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"zero density", "{enabled: true, max_terms_per_1000_words: 0}", "max_terms_per_1000_words"},
		{"disabled zero density", "{enabled: false, max_terms_per_1000_words: 0}", "max_terms_per_1000_words"},
		{"negative density", "{max_terms_per_1000_words: -1}", "max_terms_per_1000_words"},
		{"nan density", "{max_terms_per_1000_words: .nan}", "max_terms_per_1000_words"},
		{"infinite density", "{max_terms_per_1000_words: .inf}", "max_terms_per_1000_words"},
		{"zero length", "{enabled: true, min_source_len: 0}", "min_source_len"},
		{"negative length", "{min_source_len: -1}", "min_source_len"},
		{"invalid strategy", "{conflict_strategy: last-wins}", "conflict_strategy"},
		{"old density name", "{max_terms_per_1000_chars: 3}", "unknown"},
		{"old strategy name", "{inline_conflict_strategy: off}", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := strings.Replace(minimalTranslation, "translate: {}", "translate:\n        inline_term_extraction: "+tc.input, 1)
			_, err := ResolveCLIConfig(translationInput(t, doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v want=%s", err, tc.want)
			}
		})
	}
}

func TestCLIInlineTermExtractionDoesNotLeakAcrossRounds(t *testing.T) {
	doc := strings.Replace(minimalTranslation, "translate: {}", "translate:\n        inline_term_extraction: {enabled: true, max_terms_per_1000_words: 6, conflict_strategy: off}", 1) + `    - mode: translate
      backend: test
      translate: {}
    - mode: translate
      backend: test
      translate:
        inline_term_extraction: {enabled: false, min_source_len: 5}
`
	cfg, err := ResolveCLIConfig(translationInput(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	first := cfg.Execution.Rounds[0].Translate.InlineTermExtraction
	second := cfg.Execution.Rounds[1].Translate.InlineTermExtraction
	third := cfg.Execution.Rounds[2].Translate.InlineTermExtraction
	if first == nil || !first.Enabled || first.MaxTermsPer1000Words != 6 || first.MinSourceLen != 2 || first.ConflictStrategy != "off" {
		t.Fatalf("first round lost its configuration: %+v", first)
	}
	if second != nil {
		t.Fatalf("omitted second round inherited extraction: %+v", second)
	}
	if third == nil || third.Enabled || third.MaxTermsPer1000Words != 3 || third.MinSourceLen != 5 || third.ConflictStrategy != "rewrite-local" {
		t.Fatalf("third round inherited another round's configuration: %+v", third)
	}
}

func TestCLIRejectsLegacyProfileGlossary(t *testing.T) {
	const profile = "schema_version: 1\nglossary:\n  bootstrap:\n    enabled: true\n"
	for _, external := range []bool{false, true} {
		name := "inline"
		if external {
			name = "external"
		}
		t.Run(name, func(t *testing.T) {
			doc := minimalTranslation + "translation_profiles:\n  legacy:\n"
			if external {
				doc += "    file: legacy.yaml\n"
			} else {
				doc += "    " + strings.ReplaceAll(strings.TrimSuffix(profile, "\n"), "\n", "\n    ") + "\n"
			}
			in := translationInput(t, doc)
			if external {
				if err := os.WriteFile(filepath.Join(in.WorkingDirectory, "legacy.yaml"), []byte(profile), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ResolveCLIConfig(in); err == nil || !strings.Contains(err.Error(), "unknown field glossary") {
				t.Fatalf("legacy profile was accepted: %v", err)
			}
		})
	}
}
