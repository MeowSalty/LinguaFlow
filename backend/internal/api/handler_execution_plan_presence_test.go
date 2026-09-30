package api

import (
	"encoding/json"
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
