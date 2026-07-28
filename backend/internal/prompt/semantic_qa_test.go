package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

const defaultTestSemanticQATmpl = `You are LinguaFlow semantic QA assistant.
Source={{.SourceLang}} Target={{.TargetLang}}.
Protocol={{.Protocol}}.
Reply as JSON: {"issues":[...]}.`

func TestSemanticQARenderer_RendersEnvelope(t *testing.T) {
	r, err := NewSemanticQARenderer(defaultTestSemanticQATmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	sys, usr, err := r.Render(SemanticQAData{
		SourceLang: "ja",
		TargetLang: "zh",
		Protocol:   ProtocolJSONStrict,
		Segments: []SemanticQASegment{
			{ID: "1", Source: "安堵感", Target: "安堵感"},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sys, "ja") || !strings.Contains(sys, "zh") {
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
		} `json:"segments"`
	}
	if err := json.Unmarshal([]byte(usr), &env); err != nil {
		t.Fatalf("user not json: %v\n%s", err, usr)
	}
	if env.Task != "semantic_quality_scan" {
		t.Errorf("task=%q", env.Task)
	}
	if len(env.Segments) != 1 || env.Segments[0].ID != "1" {
		t.Errorf("segments=%#v", env.Segments)
	}
}

func TestSemanticQARenderer_TextUser(t *testing.T) {
	r, err := NewSemanticQARenderer(defaultTestSemanticQATmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	_, usr, err := r.Render(SemanticQAData{
		SourceLang: "ja",
		TargetLang: "zh",
		Protocol:   ProtocolText,
		Segments: []SemanticQASegment{
			{ID: "1", Source: "安堵感", Target: "安堵感"},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(usr), "{") {
		t.Fatalf("text mode user should not be JSON:\n%s", usr)
	}
	if !strings.Contains(usr, "source_lang: ja") {
		t.Errorf("missing source_lang:\n%s", usr)
	}
	if !strings.Contains(usr, "target_lang: zh") {
		t.Errorf("missing target_lang:\n%s", usr)
	}
	if !strings.Contains(usr, "[segment] id=1") {
		t.Errorf("missing segment header:\n%s", usr)
	}
	if !strings.Contains(usr, "source: 安堵感") {
		t.Errorf("missing source:\n%s", usr)
	}
	if !strings.Contains(usr, "target: 安堵感") {
		t.Errorf("missing target:\n%s", usr)
	}
}

func TestSemanticQAIssueSchema_Strict(t *testing.T) {
	s := SemanticQAIssueSchema()
	if s["additionalProperties"] != false {
		t.Error("outer additionalProperties should be false")
	}
	outerReq, _ := s["required"].([]string)
	if len(outerReq) != 1 || outerReq[0] != "issues" {
		t.Errorf("outer required mismatch: %#v", outerReq)
	}
	props := s["properties"].(map[string]any)
	arr := props["issues"].(map[string]any)
	item := arr["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Error("item additionalProperties should be false")
	}
	req, _ := item["required"].([]string)
	if len(req) != 4 {
		t.Errorf("item required should list 4 props, got %#v", req)
	}
}

func TestParseSemanticQAResponse_OK(t *testing.T) {
	resp := `{"issues":[{"id":"1","code":"calque","message":"和制汉语安堵感","snippet":"安堵感"}]}`
	got, err := ParseSemanticQAResponse(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Code != "calque" || got[0].Snippet != "安堵感" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseSemanticQAResponse_Fenced(t *testing.T) {
	resp := "Here:\n```json\n{\"issues\":[{\"id\":\"2\",\"code\":\"naturalness\",\"message\":\"生硬\"}]}\n```"
	got, err := ParseSemanticQAResponse(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Code != "naturalness" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseSemanticQAResponse_NoJSON(t *testing.T) {
	_, err := ParseSemanticQAResponse("sorry I cannot")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseSemanticQAResponse_FiltersIllegalCode(t *testing.T) {
	resp := `{"issues":[{"id":"1","code":"source_residual","message":"x"},{"id":"1","code":"calque","message":"ok"}]}`
	got, err := ParseSemanticQAResponse(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Code != "calque" {
		t.Fatalf("want only calque, got=%#v", got)
	}
}

func TestParseSemanticQAByMode_TextIssues(t *testing.T) {
	resp := "[issues]\n1 | calque | 安堵感 | 安堵感借译\n1 | term_fidelity | 术语A | 术语不一致\n2 | naturalness | 整句 | 不自然"
	got, err := ParseSemanticQAByMode(resp, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got len=%d want 3: %#v", len(got), got)
	}
	if got[0].ID != "1" || got[0].Code != "calque" || got[0].Snippet != "安堵感" || got[0].Message != "安堵感借译" {
		t.Errorf("first=%#v", got[0])
	}
	if got[2].ID != "2" || got[2].Code != "naturalness" || got[2].Snippet != "整句" {
		t.Errorf("third=%#v", got[2])
	}
}

func TestParseSemanticQAByMode_TextFenced(t *testing.T) {
	resp := "```\n[issues]\n1 | calque | 借译\n```"
	got, err := ParseSemanticQAByMode(resp, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Code != "calque" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseSemanticQAByMode_TextEmptyFallsBackJSON(t *testing.T) {
	resp := `{"issues":[{"id":"1","code":"calque","message":"ok"}]}`
	got, err := ParseSemanticQAByMode(resp, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Code != "calque" {
		t.Fatalf("JSON fallback got=%#v", got)
	}
}

func TestParseSemanticQAByMode_TextEmptyIssues(t *testing.T) {
	got, err := ParseSemanticQAByMode("[issues]", true)
	if err != nil {
		t.Fatalf("parse empty issues: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got=%#v want empty issues", got)
	}
}

func TestParseSemanticQAByMode_TextMalformedIssues(t *testing.T) {
	_, err := ParseSemanticQAByMode("[issues]\nmalformed", true)
	if err == nil {
		t.Fatal("malformed issues should not be accepted as an empty result")
	}
}

func TestParseSemanticQAByMode_TextFiltersIllegalCode(t *testing.T) {
	resp := "[issues]\n1 | source_residual | residual\n2 | calque | ok"
	got, err := ParseSemanticQAByMode(resp, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("want only legal code, got=%#v", got)
	}
}

func TestParseSemanticQAByMode_TextMessageWithPipe(t *testing.T) {
	resp := "[issues]\n1 | calque | snip | a | b | c"
	got, err := ParseSemanticQAByMode(resp, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Snippet != "snip" || got[0].Message != "a | b | c" {
		t.Fatalf("message with pipes got=%#v", got)
	}
}

func TestParseSemanticQAByMode_TextLegacyThreeFields(t *testing.T) {
	resp := "[issues]\n1 | calque | only message"
	got, err := ParseSemanticQAByMode(resp, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Snippet != "" || got[0].Message != "only message" {
		t.Fatalf("legacy three-field got=%#v", got)
	}
}

func TestParseSemanticQAByMode_NonTextUsesJSON(t *testing.T) {
	resp := "[issues]\n1 | calque | x"
	_, err := ParseSemanticQAByMode(resp, false)
	if err == nil {
		t.Fatal("non-text mode should require JSON")
	}
}
