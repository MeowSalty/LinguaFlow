package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

const defaultTestAdjudicationTmpl = `You are LinguaFlow adjudication assistant.
Source={{.SourceLang}} Target={{.TargetLang}}.
Protocol={{.Protocol}}.
Reply as JSON: {"verdicts":[...]}.`

func TestAdjudicationRenderer_RendersEnvelope(t *testing.T) {
	r, err := NewAdjudicationRenderer(defaultTestAdjudicationTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	sys, usr, err := r.Render(AdjudicationData{
		SourceLang: "en",
		TargetLang: "zh",
		Protocol:   ProtocolJSONStrict,
		Segments: []AdjudicationSegment{
			{
				ID:     "1",
				Source: "Hello",
				Target: "你好 Hello",
				Issues: []AdjudicationIssue{{Code: "source_residual", Message: "residual"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sys, "en") || !strings.Contains(sys, "zh") {
		t.Errorf("system missing langs:\n%s", sys)
	}
	var env struct {
		Task       string `json:"task"`
		SourceLang string `json:"source_lang"`
		TargetLang string `json:"target_lang"`
		Segments   []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
			Target string `json:"target"`
			Issues []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"issues"`
		} `json:"segments"`
	}
	if err := json.Unmarshal([]byte(usr), &env); err != nil {
		t.Fatalf("user not json: %v\n%s", err, usr)
	}
	if env.Task != "adjudicate_quality_issues" {
		t.Errorf("task=%q", env.Task)
	}
	if len(env.Segments) != 1 || env.Segments[0].ID != "1" {
		t.Errorf("segments=%#v", env.Segments)
	}
	if len(env.Segments[0].Issues) != 1 || env.Segments[0].Issues[0].Code != "source_residual" {
		t.Errorf("issues=%#v", env.Segments[0].Issues)
	}
}

func TestAdjudicationRenderer_TextUser(t *testing.T) {
	r, err := NewAdjudicationRenderer(defaultTestAdjudicationTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	_, usr, err := r.Render(AdjudicationData{
		SourceLang: "en",
		TargetLang: "zh",
		Protocol:   ProtocolText,
		Segments: []AdjudicationSegment{
			{
				ID:     "1",
				Source: "Hello",
				Target: "你好 Hello",
				Issues: []AdjudicationIssue{{Code: "source_residual", Message: "residual text"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(usr), "{") {
		t.Fatalf("text mode user should not be JSON:\n%s", usr)
	}
	if !strings.Contains(usr, "source_lang: en") {
		t.Errorf("missing source_lang:\n%s", usr)
	}
	if !strings.Contains(usr, "target_lang: zh") {
		t.Errorf("missing target_lang:\n%s", usr)
	}
	if !strings.Contains(usr, "[segment] id=1") {
		t.Errorf("missing segment header:\n%s", usr)
	}
	if !strings.Contains(usr, "source: Hello") {
		t.Errorf("missing source:\n%s", usr)
	}
	if !strings.Contains(usr, "target: 你好 Hello") {
		t.Errorf("missing target:\n%s", usr)
	}
	if !strings.Contains(usr, "- source_residual: residual text") {
		t.Errorf("missing issue line:\n%s", usr)
	}
}

func TestAdjudicationVerdictSchema_Strict(t *testing.T) {
	s := AdjudicationVerdictSchema()
	if s["additionalProperties"] != false {
		t.Error("outer additionalProperties should be false")
	}
	outerReq, _ := s["required"].([]string)
	if len(outerReq) != 1 || outerReq[0] != "verdicts" {
		t.Errorf("outer required mismatch: %#v", outerReq)
	}
	props := s["properties"].(map[string]any)
	arr := props["verdicts"].(map[string]any)
	item := arr["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Error("item additionalProperties should be false")
	}
	req, _ := item["required"].([]string)
	if len(req) != 4 {
		t.Errorf("item required should list 4 props, got %#v", req)
	}
}

func TestParseAdjudicationResponse_OK(t *testing.T) {
	resp := `{"verdicts":[{"id":"1","issue_code":"source_residual","verdict":"false_positive","reason":"proper noun"}]}`
	got, err := ParseAdjudicationResponse(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Verdict != "false_positive" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseAdjudicationResponse_Fenced(t *testing.T) {
	resp := "Here you go:\n```json\n{\"verdicts\":[{\"id\":\"2\",\"issue_code\":\"length_ratio\",\"verdict\":\"real\",\"reason\":\"too short\"}]}\n```"
	got, err := ParseAdjudicationResponse(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].IssueCode != "length_ratio" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseAdjudicationResponse_NoJSON(t *testing.T) {
	_, err := ParseAdjudicationResponse("sorry I cannot")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseAdjudicationByMode_TextVerdicts(t *testing.T) {
	resp := "[verdicts]\n1 | source_residual | false_positive | proper noun\n2 | length_ratio | real | too short"
	got, err := ParseAdjudicationByMode(resp, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got len=%d want 2: %#v", len(got), got)
	}
	if got[0].ID != "1" || got[0].Verdict != "false_positive" || got[0].Reason != "proper noun" {
		t.Errorf("first=%#v", got[0])
	}
	if got[1].ID != "2" || got[1].IssueCode != "length_ratio" || got[1].Verdict != "real" {
		t.Errorf("second=%#v", got[1])
	}
}

func TestParseAdjudicationByMode_TextFenced(t *testing.T) {
	resp := "```\n[verdicts]\n1 | source_residual | real | missed\n```"
	got, err := ParseAdjudicationByMode(resp, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Verdict != "real" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseAdjudicationByMode_TextEmptyFallsBackJSON(t *testing.T) {
	resp := `{"verdicts":[{"id":"1","issue_code":"source_residual","verdict":"false_positive","reason":"ok"}]}`
	got, err := ParseAdjudicationByMode(resp, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Verdict != "false_positive" {
		t.Fatalf("JSON fallback got=%#v", got)
	}
}

func TestParseAdjudicationByMode_TextFiltersIllegalVerdict(t *testing.T) {
	resp := "[verdicts]\n1 | source_residual | maybe | unclear\n2 | length_ratio | real | ok"
	got, err := ParseAdjudicationByMode(resp, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("want only legal verdict, got=%#v", got)
	}
}

func TestParseAdjudicationByMode_TextReasonWithPipe(t *testing.T) {
	resp := "[verdicts]\n1 | source_residual | false_positive | a | b | c"
	got, err := ParseAdjudicationByMode(resp, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Reason != "a | b | c" {
		t.Fatalf("reason with pipes got=%#v", got)
	}
}

func TestParseAdjudicationByMode_NonTextUsesJSON(t *testing.T) {
	resp := "[verdicts]\n1 | source_residual | real | x"
	_, err := ParseAdjudicationByMode(resp, false)
	if err == nil {
		t.Fatal("non-text mode should require JSON")
	}
}
