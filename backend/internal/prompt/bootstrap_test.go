package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

// defaultTestBootstrapTmpl 是测试用的最小 bootstrap 模板。
const defaultTestBootstrapTmpl = `You are LinguaFlow, a glossary-bootstrap assistant.
Task: extract domain-specific terms from {{.SourceLang}} to {{.TargetLang}}.
Return AT MOST {{.MaxTerms}} entries. Reply as JSON: {"glossary":[...]}.`

func TestBootstrapRenderer_RendersTaskAndTexts(t *testing.T) {
	r, err := NewBootstrapRenderer(defaultTestBootstrapTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	sys, usr, err := r.Render(BootstrapData{
		SourceLang: "en", TargetLang: "zh",
		Texts:    []string{"call the OpenAI API"},
		Existing: []string{"already-have"},
		MaxTerms: 5,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sys, "extract domain-specific terms") {
		t.Errorf("system prompt missing core instruction:\n%s", sys)
	}
	if !strings.Contains(sys, "5") {
		t.Errorf("system prompt missing max terms cap:\n%s", sys)
	}

	var env struct {
		Task       string   `json:"task"`
		SourceLang string   `json:"source_lang"`
		TargetLang string   `json:"target_lang"`
		Existing   []string `json:"existing"`
		Texts      []string `json:"texts"`
	}
	if err := json.Unmarshal([]byte(usr), &env); err != nil {
		t.Fatalf("user not json: %v\n%s", err, usr)
	}
	if env.Task != "extract_terms" {
		t.Errorf("task want extract_terms, got %q", env.Task)
	}
	if len(env.Texts) != 1 || env.Texts[0] != "call the OpenAI API" {
		t.Errorf("texts mismatch: %#v", env.Texts)
	}
	if len(env.Existing) != 1 || env.Existing[0] != "already-have" {
		t.Errorf("existing mismatch: %#v", env.Existing)
	}
}

func TestBootstrapSchema_Strict(t *testing.T) {
	s := BootstrapSchema()
	if s["additionalProperties"] != false {
		t.Error("outer additionalProperties should be false")
	}
	outerReq, _ := s["required"].([]string)
	if len(outerReq) != 1 || outerReq[0] != "glossary" {
		t.Errorf("outer required mismatch: %#v", outerReq)
	}
	props := s["properties"].(map[string]any)
	glos := props["glossary"].(map[string]any)
	if glos["type"] != "array" {
		t.Error("glossary should be array")
	}
	item := glos["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Error("item additionalProperties should be false")
	}
	req, _ := item["required"].([]string)
	if len(req) != 3 {
		t.Errorf("item required should list all 3 props, got %#v", req)
	}
}

func TestParseBootstrapResponse_OK(t *testing.T) {
	resp := `{"glossary":[{"source":"Gemini","target":"哈基米","notes":"company"},{"source":"API","target":"接口","notes":""}]}`
	got, err := ParseBootstrapResponse(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
}

func TestParseBootstrapResponse_DropsEmptyAndDupes(t *testing.T) {
	resp := `{"glossary":[
		{"source":"Gemini","target":"哈基米","notes":""},
		{"source":"","target":"x","notes":""},
		{"source":"Gemini","target":"哈基米重复","notes":""},
		{"source":"Foo","target":"","notes":""}
	]}`
	got, err := ParseBootstrapResponse(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Source != "Gemini" || got[0].Target != "哈基米" {
		t.Errorf("unexpected entries: %#v", got)
	}
}

func TestParseBootstrapResponse_FenceTolerant(t *testing.T) {
	resp := "Sure!\n```json\n{\"glossary\":[]}\n```\nDone."
	got, err := ParseBootstrapResponse(resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %#v", got)
	}
}

func TestParseBootstrapResponse_NoJSON(t *testing.T) {
	if _, err := ParseBootstrapResponse("totally not json"); err == nil {
		t.Fatal("expected error")
	}
}

func TestBootstrapRenderer_TextModeUserFormat(t *testing.T) {
	r, err := NewBootstrapRenderer(defaultTestBootstrapTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	sys, usr, err := r.Render(BootstrapData{
		SourceLang: "en", TargetLang: "zh",
		Texts:    []string{"Call Gemini API", "Use OAuth2"},
		Existing: []string{"already-have"},
		MaxTerms: 5,
		Protocol: ProtocolText,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sys, "extract domain-specific terms") {
		t.Errorf("system prompt missing core instruction:\n%s", sys)
	}
	// user 应为纯文本，非 JSON
	if strings.HasPrefix(strings.TrimSpace(usr), "{") {
		t.Fatalf("text mode user should not be JSON:\n%s", usr)
	}
	if !strings.Contains(usr, "source_lang: en") {
		t.Errorf("missing source_lang:\n%s", usr)
	}
	if !strings.Contains(usr, "target_lang: zh") {
		t.Errorf("missing target_lang:\n%s", usr)
	}
	if !strings.Contains(usr, "max_terms: 5") {
		t.Errorf("missing max_terms:\n%s", usr)
	}
	if !strings.Contains(usr, "[existing]\nalready-have") {
		t.Errorf("missing existing section:\n%s", usr)
	}
	if !strings.Contains(usr, "[texts]\n---\nCall Gemini API\n---\nUse OAuth2") {
		t.Errorf("missing texts section:\n%s", usr)
	}
}

func TestBootstrapRenderer_StrictSchemaOmitsShape(t *testing.T) {
	const tmpl = `协议
{{- if eq .Protocol "json_loose"}}
SHAPE:{"glossary":[]}
{{- end}}
`
	r, err := NewBootstrapRenderer(tmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	sysStrict, _, err := r.Render(BootstrapData{
		SourceLang: "en", TargetLang: "zh", Texts: []string{"x"}, MaxTerms: 3, Protocol: ProtocolJSONStrict,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(sysStrict, "SHAPE:") {
		t.Errorf("strict should omit shape:\n%s", sysStrict)
	}
	sysLoose, _, err := r.Render(BootstrapData{
		SourceLang: "en", TargetLang: "zh", Texts: []string{"x"}, MaxTerms: 3, Protocol: ProtocolJSONLoose,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sysLoose, "SHAPE:") {
		t.Errorf("loose should include shape:\n%s", sysLoose)
	}
}

func TestBootstrapRenderer_JSONPathUnchanged(t *testing.T) {
	r, err := NewBootstrapRenderer(defaultTestBootstrapTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	_, usr, err := r.Render(BootstrapData{
		SourceLang: "en", TargetLang: "zh",
		Texts: []string{"hello"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(usr), &env); err != nil {
		t.Fatalf("json path should produce JSON: %v\n%s", err, usr)
	}
	if env["task"] != "extract_terms" {
		t.Errorf("task = %v", env["task"])
	}
}
