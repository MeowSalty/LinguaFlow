package v013

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

const ignoredStrategyMarker = "synthetic-private-legacy-strategy-marker"

func sourceStrategyTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal("cannot encode synthetic strategy fixture")
	}
	return raw
}

func sourceStrategyTestObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal("cannot decode synthetic strategy fixture")
	}
	return out
}

func TestSourceRoundStrategyIgnoresAllJSONValueTypes(t *testing.T) {
	const baseline = `{"rounds":[{"mode":"translate","translate":{"prompt":{"content":"saved prompt"},"batch_size":3,"max_words_per_batch":50,"concurrency":2,"fallback_shrink":0.5,"segment_filter":{"status_filter":"all"},"retry":{"max_attempts":0,"backoff_ms":0,"jitter":false}}}]}`
	var want sourceJobExecutionSnapshot
	if err := decodeSource([]byte(baseline), &want); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{
		"null":         nil,
		"boolean":      true,
		"number":       123.5,
		"string":       ignoredStrategyMarker,
		"empty_array":  []any{},
		"array":        []any{ignoredStrategyMarker, false, nil},
		"empty_object": map[string]any{},
		"object":       map[string]any{"protect": map[string]any{"enabled": true}, ignoredStrategyMarker: "unused", "schema_version": 99},
	} {
		t.Run(name, func(t *testing.T) {
			input := sourceStrategyTestObject(t, []byte(baseline))
			input["rounds"].([]any)[0].(map[string]any)["translate"].(map[string]any)["strategy"] = value
			var got sourceJobExecutionSnapshot
			if err := decodeSource(sourceStrategyTestJSON(t, input), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) || !bytes.Equal(sourceStrategyTestJSON(t, got), sourceStrategyTestJSON(t, want)) {
				t.Fatal("ignored strategy changed the frozen source snapshot")
			}
			if bytes.Contains(sourceStrategyTestJSON(t, got), []byte(ignoredStrategyMarker)) {
				t.Fatal("ignored strategy payload survived source decoding")
			}
		})
	}
}

func TestSourceRoundStrategyKeepsOtherFieldsStrict(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown_translate_field":    `{"rounds":[{"translate":{"strategy":{},"` + ignoredStrategyMarker + `":true}}]}`,
		"unknown_prompt_field":       `{"rounds":[{"translate":{"prompt":{"strategy":"` + ignoredStrategyMarker + `"}}}]}`,
		"round_strategy":             `{"rounds":[{"strategy":"` + ignoredStrategyMarker + `","translate":{}}]}`,
		"extract_strategy":           `{"rounds":[{"extract":{"strategy":"` + ignoredStrategyMarker + `"}}]}`,
		"unknown_top_strategy_field": `{"strategy":{"` + ignoredStrategyMarker + `":true},"rounds":[{"translate":{"strategy":{}}}]}`,
		"target_schema_version":      `{"schema_version":1,"rounds":[{"translate":{"strategy":{}}}]}`,
		"target_translate_field":     `{"rounds":[{"translate":{"strategy":{},"schema_version":1}}]}`,
		"target_credential":          `{"rounds":[{"backend":{"credential":{"id":1,"version":1}},"translate":{"strategy":{}}}]}`,
		"invalid_batch_type":         `{"rounds":[{"translate":{"batch_size":"` + ignoredStrategyMarker + `","strategy":{}}}]}`,
		"invalid_top_strategy_type":  `{"strategy":"` + ignoredStrategyMarker + `","rounds":[{"translate":{"strategy":{}}}]}`,
		"invalid_translate_type":     `{"rounds":[{"translate":["` + ignoredStrategyMarker + `"]}]}`,
		"trailing_document":          `{"rounds":[{"translate":{"strategy":{}}}]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			var snapshot sourceJobExecutionSnapshot
			err := decodeSource([]byte(raw), &snapshot)
			if err == nil {
				t.Fatal("unsupported source field or type was accepted")
			}
			if strings.Contains(err.Error(), ignoredStrategyMarker) || strings.Contains(err.Error(), "mixed current") {
				t.Fatal("decode failure disclosed source content or inferred a mixed-version cause")
			}
		})
	}
}

func TestSourceRoundStrategyDecodeFailureDoesNotMutateReceiver(t *testing.T) {
	want := sourceJobTranslateRoundSnapshot{BatchSize: 7}
	got := want
	err := got.UnmarshalJSON([]byte(`{"batch_size":10,"strategy":{},"` + ignoredStrategyMarker + `":true}`))
	if err == nil || !reflect.DeepEqual(got, want) {
		t.Fatal("failed strict decoding changed its receiver")
	}
}

func TestSourceRoundStrategyMigrationMatchesRemovedFieldBaseline(t *testing.T) {
	for _, tc := range []struct {
		name, topStrategy string
	}{
		{"missing", ""},
		{"null", "null"},
		{"empty_object", "{}"},
		{"explicit_zero_false_empty", `{"profile_id":0,"profile_name":"saved empty policy","protect":{"enabled":false,"rules":[]},"postprocess":{"enabled":false,"trim_spaces":false},"repair":{"enabled":false,"json_structural":false,"schema_aliases":false,"placeholder_normalize":false,"prompt_upgrade":false},"context":{"enabled":false,"before":0,"after":0,"max_chars":0},"ruby":{"enabled":false,"preserve_kinds":[]},"qa":{"enabled":false,"auto_reject":false,"checks":[],"length_ratio_min":0,"length_ratio_max":0}}`},
		{"disabled_parent_with_saved_children", `{"protect":{"enabled":false,"rules":["code"]},"postprocess":{"enabled":false,"trim_spaces":true},"repair":{"enabled":false,"json_structural":true,"schema_aliases":true,"placeholder_normalize":true,"prompt_upgrade":true},"context":{"enabled":false,"before":2,"after":3,"max_chars":400},"ruby":{"enabled":false,"preserve_kinds":["creative"]},"qa":{"enabled":false,"auto_reject":true,"checks":["untranslated"],"length_method":"word_count","length_ratio_min":0.2,"length_ratio_max":3}}`},
		{"enabled_top_strategy", `{"protect":{"enabled":true,"rules":["xml"]},"postprocess":{"enabled":true,"trim_spaces":true},"repair":{"enabled":true,"json_structural":true},"context":{"enabled":true,"before":2,"after":1,"max_chars":1200},"ruby":{"enabled":true,"preserve_kinds":["creative"]},"qa":{"enabled":true,"checks":[],"length_method":"word_count","length_ratio_min":0,"length_ratio_max":0}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, client, owner, credentials, backends := credentialTestServices(t)
			backend := credentialTestBackend(t, ctx, backends, owner, "current backend", "current-secret")
			baseline := legacyExecutionFixture(t, ctx, client, owner.ID, backend.ID)
			legacy := legacyExecutionFixture(t, ctx, client, owner.ID, backend.ID)
			for _, job := range []*ent.Job{baseline, legacy} {
				delete(job.ExecutionConfig, "strategy")
				if tc.topStrategy != "" {
					var strategy any
					if err := json.Unmarshal([]byte(tc.topStrategy), &strategy); err != nil {
						t.Fatal("invalid top-level strategy fixture")
					}
					job.ExecutionConfig["strategy"] = strategy
				}
				rounds := job.ExecutionConfig["rounds"].([]any)
				secondTranslate := sourceStrategyTestObject(t, sourceStrategyTestJSON(t, rounds[0]))
				job.ExecutionConfig["rounds"] = append(rounds, secondTranslate)
				if job.ID == legacy.ID {
					rounds[0].(map[string]any)["translate"].(map[string]any)["strategy"] = map[string]any{
						"profile_name": ignoredStrategyMarker, "protect": map[string]any{"enabled": true, "rules": []any{"code"}},
					}
					secondTranslate["translate"].(map[string]any)["strategy"] = map[string]any{
						"profile_name": "different unused policy", "protect": map[string]any{"enabled": false, "rules": []any{}},
					}
				}
				client.Job.UpdateOneID(job.ID).SetExecutionConfig(job.ExecutionConfig).SetUpdatedAt(job.UpdatedAt).ExecX(ctx)
			}
			report := runLegacyExecutionMigration(t, ctx, client, credentials.keys)
			if report.Jobs != 2 || report.Credentials != 1 {
				t.Fatalf("unexpected migration counts: %+v", report)
			}
			baselineAfter := client.Job.GetX(ctx, baseline.ID)
			legacyAfter := client.Job.GetX(ctx, legacy.ID)
			baselineJSON := sourceStrategyTestJSON(t, baselineAfter.ExecutionConfig)
			legacyJSON := sourceStrategyTestJSON(t, legacyAfter.ExecutionConfig)
			if !bytes.Equal(baselineJSON, legacyJSON) {
				t.Fatal("legacy strategies changed the complete migrated snapshot or its provenance")
			}
			if bytes.Contains(legacyJSON, []byte(ignoredStrategyMarker)) || bytes.Contains(legacyJSON, []byte("snapshot-original-secret")) {
				t.Fatal("ignored payload or plaintext credential survived migration")
			}
			snapshot, err := service.GetSnapshot(legacyAfter)
			if err != nil {
				t.Fatal(err)
			}
			var want execution.StrategySnapshot
			if tc.topStrategy != "" {
				if err := json.Unmarshal([]byte(tc.topStrategy), &want); err != nil {
					t.Fatal("invalid expected strategy fixture")
				}
			}
			// These are the source release's runtime fallbacks, not current
			// profile defaults. In particular all saved switches stay unchanged.
			if want.QA.Checks == nil {
				want.QA.Checks = []string{"untranslated", "length_ratio", "duplicate", "source_residual", "punctuation_pairing", "punctuation_missing", "punctuation_surplus", "punctuation_wrap_loss", "whitespace_irregular", "repeated_space", "width_mix", "script_mismatch", "number_mismatch", "url_email_mismatch", "subtitle_line_count", "forbidden_term", "term_inconsistency", "leftover_placeholder", "xml_tag_mismatch", "duplicate_source_divergence"}
			}
			if want.QA.LengthMethod == "" {
				want.QA.LengthMethod = "char_weight"
			}
			if !reflect.DeepEqual(snapshot.Strategy, want) {
				t.Fatal("top-level strategy differs from the source release's effective saved values")
			}
			if legacyAfter.Status != legacy.Status || legacyAfter.ProgressTotal != legacy.ProgressTotal || legacyAfter.ProgressCompleted != legacy.ProgressCompleted || !legacyAfter.CreatedAt.Equal(legacy.CreatedAt) || !legacyAfter.UpdatedAt.Equal(legacy.UpdatedAt) {
				t.Fatal("migration changed historical job state")
			}
			for _, binding := range snapshot.Bindings() {
				secret, err := credentials.Resolve(ctx, binding, "openai", "https://api.openai.com/v1")
				if err != nil || secret != "snapshot-original-secret" {
					t.Fatalf("historical credential did not survive: %v", err)
				}
			}
		})
	}
}
