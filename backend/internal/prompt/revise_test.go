package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

const defaultTestReviseTmpl = `You are LinguaFlow revise assistant.
Source={{.SourceLang}} Target={{.TargetLang}}.
Protocol={{.Protocol}}.
Reply as JSON: {"revisions":[...]}.`

func TestReviseRenderer_RendersEnvelope(t *testing.T) {
	r, err := NewReviseRenderer(defaultTestReviseTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	sys, usr, err := r.Render(ReviseData{
		SourceLang: "ja",
		TargetLang: "zh",
		Protocol:   ProtocolJSONStrict,
		Segments: []ReviseSegment{
			{
				ID:     "1",
				Source: "原文",
				Target: "旧译文",
				Issues: []ReviseIssue{{Code: "mistranslation", Message: "误译", Snippet: "旧"}},
			},
			{ID: "2", Source: "第二段", Target: "第二译文"},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sys, "ja") || !strings.Contains(sys, "zh") {
		t.Errorf("system missing langs: %s", sys)
	}
	var env struct {
		Task       string          `json:"task"`
		SourceLang string          `json:"source_lang"`
		TargetLang string          `json:"target_lang"`
		Segments   []ReviseSegment `json:"segments"`
	}
	if err := json.Unmarshal([]byte(usr), &env); err != nil {
		t.Fatalf("user not json: %v\n%s", err, usr)
	}
	if env.Task != "revise_translation" {
		t.Errorf("task=%q", env.Task)
	}
	if env.SourceLang != "ja" || env.TargetLang != "zh" || len(env.Segments) != 2 {
		t.Fatalf("envelope=%#v", env)
	}
	if len(env.Segments[0].Issues) != 1 || env.Segments[0].Issues[0].Snippet != "旧" {
		t.Errorf("issues=%#v", env.Segments[0].Issues)
	}
}

func TestReviseRenderer_TextUser(t *testing.T) {
	r, err := NewReviseRenderer(defaultTestReviseTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	_, usr, err := r.Render(ReviseData{
		SourceLang: "en",
		TargetLang: "zh",
		Protocol:   ProtocolText,
		Segments: []ReviseSegment{{
			ID:     "1",
			Source: "Hello",
			Target: "你好",
			Issues: []ReviseIssue{
				{Code: "naturalness", Message: "表达生硬"},
				{Code: "grammar", Message: "语法错误", Snippet: "你好"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(usr), "{") {
		t.Fatalf("text mode user should not be JSON: %s", usr)
	}
	for _, want := range []string{"source_lang: en", "target_lang: zh", "[segment] id=1", "source: Hello", "target: 你好", "- naturalness | 表达生硬", "- grammar | 语法错误 | snippet: 你好"} {
		if !strings.Contains(usr, want) {
			t.Errorf("missing %q in user:\n%s", want, usr)
		}
	}
}

func TestReviseRevisionSchema_Strict(t *testing.T) {
	s := ReviseRevisionSchema(false, nil)
	if s["additionalProperties"] != false {
		t.Error("outer additionalProperties should be false")
	}
	outerReq, _ := s["required"].([]string)
	if len(outerReq) != 1 || outerReq[0] != "revisions" {
		t.Errorf("outer required mismatch: %#v", s["required"])
	}
	props := s["properties"].(map[string]any)
	arr := props["revisions"].(map[string]any)
	item := arr["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Error("item additionalProperties should be false")
	}
	req, _ := item["required"].([]string)
	if len(req) != 2 || req[0] != "id" || req[1] != "target" {
		t.Errorf("item required mismatch: %#v", item["required"])
	}
}

func TestNormalizeReviseRevisions(t *testing.T) {
	in := []ReviseRevision{
		{ID: " 1 ", Target: " 修订后 "},
		{ID: "", Target: "drop"},
		{ID: "2", Target: ""},
		{ID: "1", Target: "duplicate"},
		{ID: " 3 ", Target: " third "},
	}
	got := NormalizeReviseRevisions(in)
	if len(got) != 2 || got[0].ID != "1" || got[0].Target != "修订后" || got[1].ID != "3" || got[1].Target != "third" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseReviseTextRevisions(t *testing.T) {
	resp := "[revisions]\n1 | 第一版 | 保留竖线\n2 | 第二版\n"
	got, recognized := ParseReviseTextRevisions(resp)
	if !recognized {
		t.Fatal("protocol should be recognized")
	}
	if len(got) != 2 || got[0].ID != "1" || got[0].Target != "第一版 | 保留竖线" || got[1].Target != "第二版" {
		t.Fatalf("got=%#v", got)
	}
}

func TestParseReviseTextRevisions_FencedAndEmpty(t *testing.T) {
	got, recognized := ParseReviseTextRevisions("```text\n[revisions]\n```")
	if !recognized || len(got) != 0 {
		t.Fatalf("got=%#v recognized=%v", got, recognized)
	}
}

func TestParseReviseTextRevisions_MalformedAndUnrecognized(t *testing.T) {
	got, recognized := ParseReviseTextRevisions("[revisions]\nmalformed")
	if recognized || len(got) != 0 {
		t.Fatalf("malformed got=%#v recognized=%v", got, recognized)
	}
	_, recognized = ParseReviseTextRevisions("plain response")
	if recognized {
		t.Fatal("response without header should not be recognized")
	}
}

func TestReviseRenderer_RendersRubyAnnotationsJSON(t *testing.T) {
	r, err := NewReviseRenderer(defaultTestReviseTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	_, usr, err := r.Render(ReviseData{
		SourceLang: "ja",
		TargetLang: "zh",
		Protocol:   ProtocolJSONStrict,
		Segments: []ReviseSegment{
			{ID: "s1", Source: "呪い", Target: "诅咒"},
			{ID: "s2", Source: "第二段", Target: "第二译文"},
		},
		RubyAnnotations: map[string][]RubyAnnotation{
			"s1": {{ID: "7", Base: "呪", Text: "じゅ"}},
		},
		RubyMode: RubyModeJSON,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var env struct {
		Task            string                      `json:"task"`
		RubyAnnotations map[string][]RubyAnnotation `json:"ruby_annotations"`
	}
	if err := json.Unmarshal([]byte(usr), &env); err != nil {
		t.Fatalf("user not json: %v\n%s", err, usr)
	}
	anns := env.RubyAnnotations["s1"]
	if len(anns) != 1 {
		t.Fatalf("ruby_annotations=%#v", env.RubyAnnotations)
	}
	if anns[0].ID != "7" || anns[0].Base != "呪" || anns[0].Text != "じゅ" {
		t.Errorf("annotation mismatch: %#v", anns[0])
	}
}

func TestReviseRenderer_TextUser_RubyLine(t *testing.T) {
	r, err := NewReviseRenderer(defaultTestReviseTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	_, usr, err := r.Render(ReviseData{
		SourceLang: "ja",
		TargetLang: "zh",
		Protocol:   ProtocolText,
		Segments: []ReviseSegment{
			{ID: "1", Source: "微笑", Target: "微笑"},
			{ID: "2", Source: "无注音段", Target: "无注音译文"},
		},
		RubyAnnotations: map[string][]RubyAnnotation{
			"1": {{ID: "3", Base: "微", Text: "ほほ"}, {Base: "笑", Text: "え"}},
		},
		RubyMode: RubyModeSection,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"[segment] id=1",
		"target: 微笑",
		// 含注音的段在 target 行后渲染 ruby 行；带条目 id 的标注以 #id 结尾
		"ruby: 微/ほほ#3, 笑/え",
	} {
		if !strings.Contains(usr, want) {
			t.Errorf("missing %q in user:\n%s", want, usr)
		}
	}
	// 无注音条目的段不应出现 ruby 行
	if strings.Count(usr, "ruby: ") != 1 {
		t.Errorf("expected exactly one ruby line in user:\n%s", usr)
	}
}

func TestReviseRevisionSchema_WithRuby(t *testing.T) {
	s := ReviseRevisionSchema(true, []string{"1", "2"})
	req, _ := s["required"].([]string)
	if len(req) != 2 || req[0] != "revisions" || req[1] != "ruby_output" {
		t.Errorf("outer required mismatch: %#v", s["required"])
	}
	props := s["properties"].(map[string]any)
	rubyOut := props["ruby_output"].(map[string]any)
	if rubyOut["type"] != "object" {
		t.Errorf("ruby_output type=%v", rubyOut["type"])
	}
	rprops := rubyOut["properties"].(map[string]any)
	if len(rprops) != 2 {
		t.Fatalf("ruby_output properties=%#v", rprops)
	}
	for _, id := range []string{"1", "2"} {
		entry, ok := rprops[id].(map[string]any)
		if !ok {
			t.Fatalf("ruby_output.properties[%q]=%#v", id, rprops[id])
		}
		items := entry["items"].(map[string]any)
		itemReq, _ := items["required"].([]string)
		if len(itemReq) != 3 {
			t.Errorf("item required mismatch for %q: %#v", id, items["required"])
		}
	}
}

func TestReviseRevisionSchema_NoRuby(t *testing.T) {
	s := ReviseRevisionSchema(false, nil)
	props := s["properties"].(map[string]any)
	if _, ok := props["ruby_output"]; ok {
		t.Error("ruby_output should be absent when includeRuby=false")
	}
	req, _ := s["required"].([]string)
	for _, r := range req {
		if r == "ruby_output" {
			t.Errorf("ruby_output should not be required when includeRuby=false: %#v", req)
		}
	}
}
