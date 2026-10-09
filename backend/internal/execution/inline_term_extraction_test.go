package execution

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestResolveFreezesInlineTermExtractionPerRound(t *testing.T) {
	input := validExecutionInput()
	input.GlossaryEnabled = true
	first := DefaultInlineTermExtraction()
	first.Enabled, first.MaxTermsPer1000Words = true, 7
	input.Rounds[0].Translate.InlineTermExtraction = &first
	second := *input.Rounds[0].Translate
	second.InlineTermExtraction = nil
	input.Rounds = append(input.Rounds, JobRoundSnapshot{Mode: "translate", Backend: input.Rounds[0].Backend, Translate: &second})
	spec, err := Resolve(input)
	if err != nil {
		t.Fatal(err)
	}
	first.Enabled, first.MaxTermsPer1000Words = false, 99
	got := spec.Rounds[0].Translate.InlineTermExtraction
	if !spec.GlossaryEnabled || got == nil || !got.Enabled || got.MaxTermsPer1000Words != 7 || spec.Rounds[1].Translate.InlineTermExtraction != nil {
		t.Fatal("round settings were lost, shared, or inherited by the next round")
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var restored JobExecutionSnapshot
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSpec(&restored); err != nil {
		t.Fatal(err)
	}
	if *restored.Rounds[0].Translate.InlineTermExtraction != *got || restored.Rounds[1].Translate.InlineTermExtraction != nil {
		t.Fatal("restored settings differ from the frozen per-round settings")
	}
}

func TestInlineTermExtractionPreservesGlossaryMasterSwitch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		input := validExecutionInput()
		input.GlossaryEnabled = enabled
		extraction := DefaultInlineTermExtraction()
		extraction.Enabled = true
		input.Rounds[0].Translate.InlineTermExtraction = &extraction
		spec, err := Resolve(input)
		if err != nil {
			t.Fatal(err)
		}
		if spec.GlossaryEnabled != enabled {
			t.Fatal("round extraction overrode the glossary master switch")
		}
	}
}

func TestInlineTermExtractionRejectsInvalidParameters(t *testing.T) {
	for name, mutate := range map[string]func(*InlineTermExtractionConfig){
		"zero_density":     func(c *InlineTermExtractionConfig) { c.MaxTermsPer1000Words = 0 },
		"negative_density": func(c *InlineTermExtractionConfig) { c.MaxTermsPer1000Words = -1 },
		"nan_density":      func(c *InlineTermExtractionConfig) { c.MaxTermsPer1000Words = math.NaN() },
		"infinite_density": func(c *InlineTermExtractionConfig) { c.MaxTermsPer1000Words = math.Inf(1) },
		"zero_length":      func(c *InlineTermExtractionConfig) { c.MinSourceLen = 0 },
		"unknown_strategy": func(c *InlineTermExtractionConfig) { c.ConflictStrategy = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			c := DefaultInlineTermExtraction()
			mutate(&c)
			if err := ValidateInlineTermExtraction(&c); err == nil {
				t.Fatal("invalid parameters accepted while the switch is disabled")
			}
		})
	}
}

func TestLegacyProfileGlossaryIsNotInherited(t *testing.T) {
	legacy := `{"glossary":{"bootstrap":{"enabled":true,"max_terms_per_1000_chars":3,"min_source_len":2,"inline_conflict_strategy":"rewrite-local"}}}`
	if _, err := DecodeProfileJSON([]byte(legacy), DefaultProfile()); err == nil {
		t.Fatal("new profile requests still accept removed glossary settings")
	}
	input := validExecutionInput()
	// Persisted JSON is intentionally lossy: old strategy fields are ignored,
	// and no translation round inherits them on an existing job's next run.
	if err := json.Unmarshal([]byte(legacy), &input.Strategy); err != nil {
		t.Fatal(err)
	}
	spec, err := Resolve(input)
	if err != nil {
		t.Fatal(err)
	}
	if spec.GlossaryEnabled || spec.Rounds[0].Translate.InlineTermExtraction != nil {
		t.Fatal("legacy strategy enabled extraction in an unconfigured round")
	}
	raw, err := json.Marshal(spec.Strategy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "glossary") {
		t.Fatal("removed glossary configuration remains in new strategy snapshots")
	}
}
