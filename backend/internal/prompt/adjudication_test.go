package prompt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
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
	if !strings.Contains(usr, "- source_residual | : residual text") {
		t.Errorf("missing issue line with empty matched_text:\n%s", usr)
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
	if len(req) != 5 {
		t.Errorf("item required should list 5 props, got %#v", req)
	}
	assertIssueCodeEnumMatchesAdjudicableCodes(t, item)
}

// assertIssueCodeEnumMatchesAdjudicableCodes 断言 schema 的 issue_code enum 与
// qa.AdjudicableCodes() 权威清单双向一致（不缺项、不多项）。strict 模式按该枚举
// 硬约束生成：缺项会让对应 code 的 issue 永远无法产出 verdict（静默 no-op）。
func assertIssueCodeEnumMatchesAdjudicableCodes(t *testing.T, item map[string]any) {
	t.Helper()
	itemProps, ok := item["properties"].(map[string]any)
	if !ok {
		t.Fatalf("item properties missing or not a map: %#v", item["properties"])
	}
	issueCodeProp, ok := itemProps["issue_code"].(map[string]any)
	if !ok {
		t.Fatalf("issue_code property missing or not a map: %#v", itemProps["issue_code"])
	}
	var gotSet map[string]bool
	switch enum := issueCodeProp["enum"].(type) {
	case []string:
		gotSet = make(map[string]bool, len(enum))
		for _, c := range enum {
			gotSet[c] = true
		}
	case []any:
		gotSet = make(map[string]bool, len(enum))
		for _, v := range enum {
			c, ok := v.(string)
			if !ok {
				t.Fatalf("issue_code enum has non-string entry: %#v", v)
			}
			gotSet[c] = true
		}
	default:
		t.Fatalf("issue_code enum should be []string or []any, got %T: %#v", issueCodeProp["enum"], issueCodeProp["enum"])
	}
	wantSet := make(map[string]bool)
	for _, c := range qa.AdjudicableCodes() {
		wantSet[c] = true
		if !gotSet[c] {
			t.Errorf("issue_code enum missing adjudicable code %q (full enum: %v)", c, qa.AdjudicableCodes())
		}
	}
	for c := range gotSet {
		if !wantSet[c] {
			t.Errorf("issue_code enum has extra code %q not in qa.AdjudicableCodes()", c)
		}
	}
}

func TestNormalizeAdjudicationVerdicts_OK(t *testing.T) {
	in := []AdjudicationVerdict{{ID: "1", IssueCode: "source_residual", Verdict: "false_positive", Reason: "proper noun"}}
	got := NormalizeAdjudicationVerdicts(in)
	if len(got) != 1 || got[0].Verdict != "false_positive" {
		t.Fatalf("got=%#v", got)
	}
}

func TestNormalizeAdjudicationVerdicts_DropsMissingID(t *testing.T) {
	in := []AdjudicationVerdict{{IssueCode: "source_residual"}, {ID: "1", IssueCode: "source_residual"}}
	got := NormalizeAdjudicationVerdicts(in)
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseAdjudicationTextVerdicts_TextVerdicts(t *testing.T) {
	resp := "[verdicts]\n1 | source_residual | テスト | false_positive | proper noun\n2 | length_ratio |  | real | too short"
	got, recognized := ParseAdjudicationTextVerdicts(resp)
	if !recognized {
		t.Fatalf("expected recognized")
	}
	if len(got) != 2 {
		t.Fatalf("got len=%d want 2: %#v", len(got), got)
	}
	if got[0].ID != "1" || got[0].Verdict != "false_positive" || got[0].MatchedText != "テスト" || got[0].Reason != "proper noun" {
		t.Errorf("first=%#v", got[0])
	}
	if got[1].ID != "2" || got[1].IssueCode != "length_ratio" || got[1].Verdict != "real" {
		t.Errorf("second=%#v", got[1])
	}
}

func TestParseAdjudicationTextVerdicts_LegacyFourFields(t *testing.T) {
	resp := "[verdicts]\n1 | source_residual | false_positive | proper noun"
	got, _ := ParseAdjudicationTextVerdicts(resp)
	if len(got) != 1 || got[0].MatchedText != "" || got[0].Verdict != "false_positive" {
		t.Fatalf("legacy four-field got=%#v", got)
	}
}

func TestParseAdjudicationTextVerdicts_Fenced(t *testing.T) {
	resp := "```\n[verdicts]\n1 | source_residual | real | missed\n```"
	got, _ := ParseAdjudicationTextVerdicts(resp)
	if len(got) != 1 || got[0].Verdict != "real" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseAdjudicationTextVerdicts_FiltersIllegalVerdict(t *testing.T) {
	resp := "[verdicts]\n1 | source_residual | maybe | unclear\n2 | length_ratio | real | ok"
	got, _ := ParseAdjudicationTextVerdicts(resp)
	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("want only legal verdict, got=%#v", got)
	}
}

func TestParseAdjudicationTextVerdicts_ReasonWithPipe(t *testing.T) {
	resp := "[verdicts]\n1 | source_residual | false_positive | a | b | c"
	got, _ := ParseAdjudicationTextVerdicts(resp)
	if len(got) != 1 || got[0].Reason != "a | b | c" {
		t.Fatalf("reason with pipes got=%#v", got)
	}
}

func TestParseAdjudicationTextVerdicts_Empty(t *testing.T) {
	got, recognized := ParseAdjudicationTextVerdicts("[verdicts]")
	if !recognized {
		t.Fatalf("empty [verdicts] header should be recognized")
	}
	if len(got) != 0 {
		t.Fatalf("got=%#v want empty verdicts", got)
	}
}
