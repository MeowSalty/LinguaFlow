package cli

import (
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

func TestResolveCLIExecutionFreezesInlineTermExtractionPerRound(t *testing.T) {
	cfg := newTestCLIConfig()
	cfg.Glossary.Enabled = true
	first := execution.InlineTermExtractionConfig{Enabled: true, MaxTermsPer1000Words: 6, MinSourceLen: 4, ConflictStrategy: "off"}
	third := execution.DefaultInlineTermExtraction()
	cfg.Execution.Rounds = append(cfg.Execution.Rounds, translateRoundCfg(), translateRoundCfg())
	cfg.Execution.Rounds[0].Translate.InlineTermExtraction = &first
	cfg.Execution.Rounds[2].Translate.InlineTermExtraction = &third
	resolved, err := resolveCLIExecution(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer resolved.Close()
	if !resolved.Spec.GlossaryEnabled {
		t.Fatal("CLI glossary setting was not preserved")
	}
	got := resolved.Spec.Rounds
	if !reflect.DeepEqual(got[0].Translate.InlineTermExtraction, &first) || got[1].Translate.InlineTermExtraction != nil || !reflect.DeepEqual(got[2].Translate.InlineTermExtraction, &third) {
		t.Fatal("inline extraction configuration did not remain specific to each translation round")
	}
	first.Enabled, first.MinSourceLen = false, 99
	third.Enabled = true
	if !got[0].Translate.InlineTermExtraction.Enabled || got[0].Translate.InlineTermExtraction.MinSourceLen != 4 || got[2].Translate.InlineTermExtraction.Enabled {
		t.Fatal("changing CLI input changed the resolved extraction settings")
	}
}

func TestResolveCLIExecutionPreservesGlossaryMasterSwitch(t *testing.T) {
	for _, glossaryEnabled := range []bool{false, true} {
		name := "disabled"
		if glossaryEnabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			cfg := newTestCLIConfig()
			cfg.Glossary.Enabled = glossaryEnabled
			inline := execution.DefaultInlineTermExtraction()
			inline.Enabled = true
			cfg.Execution.Rounds[0].Translate.InlineTermExtraction = &inline
			resolved, err := resolveCLIExecution(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer resolved.Close()
			if resolved.Spec.GlossaryEnabled != glossaryEnabled {
				t.Fatal("inline extraction overrode the glossary master switch")
			}
			if !resolved.Spec.Rounds[0].Translate.InlineTermExtraction.Enabled {
				t.Fatal("the glossary master switch discarded the saved round setting")
			}
		})
	}
}

func TestApplyBootstrapFlagPreservesPerRoundParametersAndProfiles(t *testing.T) {
	for _, mode := range []string{"off", "pre", "inline"} {
		t.Run(mode, func(t *testing.T) {
			cfg := newTestCLIConfig()
			profile := config.CLIConfigTranslationProfile{ProfileSpec: execution.DefaultProfile()}
			cfg.Execution.Profile = "shared"
			cfg.TranslationProfiles["shared"] = profile
			first := execution.InlineTermExtractionConfig{Enabled: true, MaxTermsPer1000Words: 7, MinSourceLen: 3, ConflictStrategy: "off"}
			second := execution.InlineTermExtractionConfig{Enabled: false, MaxTermsPer1000Words: 11, MinSourceLen: 5, ConflictStrategy: "rewrite-local"}
			cfg.Execution.Rounds = append(cfg.Execution.Rounds, translateRoundCfg(), translateRoundCfg())
			cfg.Execution.Rounds[0].Translate.InlineTermExtraction = &first
			cfg.Execution.Rounds[1].Translate.InlineTermExtraction = &second
			if err := applyTranslateFlags(cfg, translateOptions{bootstrapMode: mode, changed: map[string]bool{"bootstrap": true}}); err != nil {
				t.Fatal(err)
			}
			if len(cfg.TranslationProfiles) != 1 || !reflect.DeepEqual(cfg.TranslationProfiles["shared"], profile) {
				t.Fatal("bootstrap override changed the shared profile")
			}
			want := []execution.InlineTermExtractionConfig{first, second, execution.DefaultInlineTermExtraction()}
			index := 0
			for _, round := range cfg.Execution.Rounds {
				if round.Mode != "translate" {
					continue
				}
				want[index].Enabled = mode == "inline"
				if !reflect.DeepEqual(round.Translate.InlineTermExtraction, &want[index]) {
					t.Fatalf("round %d lost its extraction parameters: got %+v want %+v", index, round.Translate.InlineTermExtraction, want[index])
				}
				index++
			}
			if index != len(want) {
				t.Fatal("bootstrap override removed translation rounds")
			}
		})
	}
}

func TestAbsentBootstrapFlagPreservesMixedRoundSettings(t *testing.T) {
	cfg := newTestCLIConfig()
	enabled := execution.DefaultInlineTermExtraction()
	enabled.Enabled = true
	cfg.Execution.Rounds[0].Translate.InlineTermExtraction = &enabled
	cfg.Execution.Rounds = append(cfg.Execution.Rounds, translateRoundCfg())
	if err := applyTranslateFlags(cfg, translateOptions{bootstrapMode: "off"}); err != nil {
		t.Fatal(err)
	}
	if !cfg.Execution.Rounds[0].Translate.InlineTermExtraction.Enabled || cfg.Execution.Rounds[1].Translate.InlineTermExtraction != nil {
		t.Fatal("an absent bootstrap flag overwrote round configuration")
	}
}
