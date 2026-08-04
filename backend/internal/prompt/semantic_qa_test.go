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

func TestNormalizeSemanticQAIssues_OK(t *testing.T) {
	in := []SemanticQAIssue{{ID: "1", Code: "calque", Message: "和制汉语安堵感", Snippet: "安堵感"}}
	got := NormalizeSemanticQAIssues(in)
	if len(got) != 1 || got[0].Code != "calque" || got[0].Snippet != "安堵感" {
		t.Fatalf("got=%#v", got)
	}
}

func TestNormalizeSemanticQAIssues_FiltersIllegalCode(t *testing.T) {
	in := []SemanticQAIssue{{ID: "1", Code: "source_residual"}, {ID: "1", Code: "calque", Message: "ok"}}
	got := NormalizeSemanticQAIssues(in)
	if len(got) != 1 || got[0].Code != "calque" {
		t.Fatalf("want only calque, got=%#v", got)
	}
}

func TestNormalizeSemanticQAIssues_DropsMissingID(t *testing.T) {
	in := []SemanticQAIssue{{Code: "calque"}, {ID: "1", Code: "calque"}}
	got := NormalizeSemanticQAIssues(in)
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseSemanticQATextIssues_TextIssues(t *testing.T) {
	resp := "[issues]\n1 | calque | 安堵感 | 安堵感借译\n1 | term_fidelity | 术语A | 术语不一致\n2 | naturalness | 整句 | 不自然"
	got, recognized := ParseSemanticQATextIssues(resp)
	if !recognized {
		t.Fatalf("not recognized")
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

func TestParseSemanticQATextIssues_Fenced(t *testing.T) {
	resp := "```\n[issues]\n1 | calque | 借译\n```"
	got, recognized := ParseSemanticQATextIssues(resp)
	if !recognized {
		t.Fatalf("not recognized")
	}
	if len(got) != 1 || got[0].Code != "calque" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseSemanticQATextIssues_Empty(t *testing.T) {
	got, recognized := ParseSemanticQATextIssues("[issues]")
	if !recognized {
		t.Fatalf("empty [issues] header should be recognized")
	}
	if len(got) != 0 {
		t.Fatalf("got=%#v want empty issues", got)
	}
}

func TestParseSemanticQATextIssues_Malformed(t *testing.T) {
	got, recognized := ParseSemanticQATextIssues("[issues]\nmalformed")
	// "malformed" 含非空内容行（无 | 故 parse 失败），hasIssueContent=true → recognized=false，
	// 由上层 fallback JSON 路径处理。
	if recognized {
		t.Fatalf("malformed content line should NOT be recognized as empty-list")
	}
	if len(got) != 0 {
		t.Fatalf("malformed line should yield no issues, got=%#v", got)
	}
}

func TestParseSemanticQATextIssues_FiltersIllegalCode(t *testing.T) {
	resp := "[issues]\n1 | source_residual | residual\n2 | calque | ok"
	got, _ := ParseSemanticQATextIssues(resp)
	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("want only legal code, got=%#v", got)
	}
}

func TestParseSemanticQATextIssues_MessageWithPipe(t *testing.T) {
	resp := "[issues]\n1 | calque | snip | a | b | c"
	got, _ := ParseSemanticQATextIssues(resp)
	if len(got) != 1 || got[0].Snippet != "snip" || got[0].Message != "a | b | c" {
		t.Fatalf("message with pipes got=%#v", got)
	}
}

func TestParseSemanticQATextIssues_LegacyThreeFields(t *testing.T) {
	resp := "[issues]\n1 | calque | only message"
	got, _ := ParseSemanticQATextIssues(resp)
	if len(got) != 1 || got[0].Snippet != "" || got[0].Message != "only message" {
		t.Fatalf("legacy three-field got=%#v", got)
	}
}

func TestParseSemanticQATextIssues_NotRecognizedWhenNoHeader(t *testing.T) {
	resp := "just some prose without issues header"
	_, recognized := ParseSemanticQATextIssues(resp)
	if recognized {
		t.Fatal("should not be recognized without [issues] header")
	}
}

func TestSemanticQAIssueSchema_IncludesNewCodes(t *testing.T) {
	s := SemanticQAIssueSchema()
	props := s["properties"].(map[string]any)
	arr := props["issues"].(map[string]any)
	item := arr["items"].(map[string]any)
	itemProps := item["properties"].(map[string]any)
	code := itemProps["code"].(map[string]any)
	enum, _ := code["enum"].([]string)
	want := map[string]bool{
		"calque": false, "term_fidelity": false, "naturalness": false,
		"mistranslation": false, "omission": false, "addition": false,
		"grammar": false, "register": false,
	}
	for _, c := range enum {
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for c, found := range want {
		if !found {
			t.Errorf("schema enum missing %q: %#v", c, enum)
		}
	}
}

func TestNormalizeSemanticQAIssues_NewCodes(t *testing.T) {
	in := []SemanticQAIssue{
		{ID: "1", Code: "mistranslation", Message: "误译", Snippet: "x"},
		{ID: "2", Code: "omission", Message: "漏译", Snippet: "y"},
		{ID: "3", Code: "addition", Message: "添译", Snippet: "z"},
		{ID: "4", Code: "grammar", Message: "语法", Snippet: "w"},
		{ID: "5", Code: "register", Message: "语体", Snippet: "v"},
	}
	got := NormalizeSemanticQAIssues(in)
	if len(got) != 5 {
		t.Fatalf("got len=%d want 5: %#v", len(got), got)
	}
	want := []string{"mistranslation", "omission", "addition", "grammar", "register"}
	for i, w := range want {
		if got[i].Code != w {
			t.Errorf("got[%d].Code=%q want %q", i, got[i].Code, w)
		}
	}
}

func TestNormalizeSemanticQAIssues_MixedWithRuleCodes(t *testing.T) {
	in := []SemanticQAIssue{
		{ID: "1", Code: "source_residual", Message: "规则不报"},
		{ID: "1", Code: "length_ratio", Message: "规则不报"},
		{ID: "1", Code: "mistranslation", Message: "语义误译", Snippet: "x"},
		{ID: "2", Code: "untranslated", Message: "规则不报"},
	}
	got := NormalizeSemanticQAIssues(in)
	if len(got) != 1 || got[0].Code != "mistranslation" || got[0].Snippet != "x" {
		t.Fatalf("want only mistranslation, got=%#v", got)
	}
}

func TestParseSemanticQATextIssues_NewCodes(t *testing.T) {
	resp := "[issues]\n" +
		"1 | mistranslation | 误译段 | 一般语义误译\n" +
		"1 | omission | 漏译段 | 丢失分句\n" +
		"2 | grammar | 语法段 | 主谓不一致\n" +
		"3 | source_residual | 残留 | 规则不报"
	got, _ := ParseSemanticQATextIssues(resp)
	if len(got) != 3 {
		t.Fatalf("got len=%d want 3: %#v", len(got), got)
	}
	want := []string{"mistranslation", "omission", "grammar"}
	for i, w := range want {
		if got[i].Code != w {
			t.Errorf("got[%d].Code=%q want %q", i, got[i].Code, w)
		}
	}
	if got[0].ID != "1" || got[0].Snippet != "误译段" || got[0].Message != "一般语义误译" {
		t.Errorf("first=%#v", got[0])
	}
}

func TestParseSemanticQATextIssues_SameSegmentMultipleDefects(t *testing.T) {
	resp := "[issues]\n" +
		"1 | calque | 结构借译 | 逐结构借译\n" +
		"1 | mistranslation | 主动误译 | 语义取错\n" +
		"1 | grammar | 主谓不一致 | 语法错"
	got, _ := ParseSemanticQATextIssues(resp)
	if len(got) != 3 {
		t.Fatalf("same segment distinct defects should each report: got=%#v", got)
	}
	seen := map[string]string{}
	for _, iss := range got {
		seen[iss.Code] = iss.Snippet
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 distinct codes: %#v", seen)
	}
}
