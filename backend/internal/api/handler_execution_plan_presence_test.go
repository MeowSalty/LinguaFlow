package api

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestExecutionPlanEmptyCodeArraysRoundTrip(t *testing.T) {
	for _, test := range []struct {
		mode, config, field string
	}{
		{"adjudicate", `{"adjudicate_codes":[]}`, "adjudicate_codes"},
		{"semantic_qa", `{"segment_scope":"all","issue_codes":[]}`, "issue_codes"},
		{"revise", `{"segment_scope":"with_issues","issue_codes":[]}`, "issue_codes"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			var request ExecutionRoundConfig
			raw := `{"mode":"` + test.mode + `","concurrency":1,"` + test.mode + `":` + test.config + `}`
			if err := json.Unmarshal([]byte(raw), &request); err != nil {
				t.Fatal(err)
			}
			stored := toExecutionPlanRoundsAPI([]ExecutionRoundConfig{request})
			response, err := json.Marshal(toExecutionRoundConfigAPI(stored[0]))
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(response, &object); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(object[test.mode], &fields); err != nil {
				t.Fatal(err)
			}
			if got := string(fields[test.field]); got != "[]" {
				t.Fatalf("explicit empty array became omitted/null: %s", response)
			}
		})
	}
}

func TestExecutionPlanTemplateIDRoundTrip(t *testing.T) {
	for _, test := range []struct {
		mode, config, field string
		want                int
	}{
		{"translate", `{"prompt_template_id":-1,"fallback_shrink":1}`, "prompt_template_id", -1},
		{"translate", `{"prompt_template_id":42,"fallback_shrink":0.5}`, "prompt_template_id", 42},
		{"extract", `{"template_id":-1}`, "template_id", -1},
		{"extract", `{"template_id":7}`, "template_id", 7},
	} {
		t.Run(fmt.Sprintf("%s/%d", test.mode, test.want), func(t *testing.T) {
			var request ExecutionRoundConfig
			raw := `{"mode":"` + test.mode + `","concurrency":1,"` + test.mode + `":` + test.config + `}`
			if err := json.Unmarshal([]byte(raw), &request); err != nil {
				t.Fatal(err)
			}
			stored := toExecutionPlanRoundsAPI([]ExecutionRoundConfig{request})
			var schemaID int
			if test.mode == "translate" {
				schemaID = stored[0].Translate.PromptTemplateID
			} else {
				schemaID = stored[0].Extract.BootstrapTemplateID
			}
			if schemaID != test.want {
				t.Fatalf("request mapping lost template ID: got %d, want %d", schemaID, test.want)
			}
			response, err := json.Marshal(toExecutionRoundConfigAPI(stored[0]))
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(response, &object); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(object[test.mode], &fields); err != nil {
				t.Fatal(err)
			}
			var got int
			if err := json.Unmarshal(fields[test.field], &got); err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("response mapping lost template ID: got %d, want %d (%s)", got, test.want, response)
			}
		})
	}
}
