package prompt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// defaultTestSystemTmpl 是测试用的最小系统模板。
// 内容与 templates/default/prompts/default.tmpl 的关键子集保持一致。
const defaultTestSystemTmpl = `你是 LinguaFlow，一个专业的翻译引擎。
将用户的文本从 {{.SourceLang}} 翻译为 {{.TargetLang}}。
协议：
- 用户消息是一个 JSON 对象，包含 source_lang、target_lang、segments 字段。
- segments 中每个条目包含 "source"（原文）和 "translate"（是否需要翻译）。
- 你的回复必须是一个 JSON 对象，其中 "translations" 仅包含 translate=true 的段落。
- 仅输出 JSON，无 markdown 围栏、无额外文字。`

type userMsg struct {
	SourceLang string                   `json:"source_lang"`
	TargetLang string                   `json:"target_lang"`
	Segments   map[string]SegmentDetail `json:"segments"`
}

func mustUnmarshalUser(t *testing.T, s string) userMsg {
	t.Helper()
	var m userMsg
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("user message is not valid JSON: %v\n%s", err, s)
	}
	return m
}

func TestRenderer_BatchMode(t *testing.T) {
	r, err := NewRenderer(config.PromptConfig{
		SystemTemplateContent: defaultTestSystemTmpl,
	})
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "en", TargetLang: "zh",
		Segments: []SegmentInput{
			{ID: "1", Source: "hello", Translate: true},
			{ID: "2", Source: "world", Translate: true},
		},
	}
	sys, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sys, `"translations"`) {
		t.Errorf("system prompt missing JSON protocol instructions:\n%s", sys)
	}
	m := mustUnmarshalUser(t, usr)
	if m.Segments["1"].Source != "hello" || m.Segments["2"].Source != "world" {
		t.Errorf("segments mismatch: %#v", m.Segments)
	}
	if !m.Segments["1"].Translate || !m.Segments["2"].Translate {
		t.Errorf("batch segments should have translate=true")
	}
	if _, ok := m.Segments[SingleID]; ok {
		t.Errorf("batch mode should not carry single-id %q", SingleID)
	}
}

func TestRenderer_BatchModeWithContext(t *testing.T) {
	r, err := NewRenderer(config.PromptConfig{
		SystemTemplateContent: defaultTestSystemTmpl,
	})
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "en", TargetLang: "zh",
		Segments: []SegmentInput{
			{ID: "0", Source: "context before", Translate: false},
			{ID: "1", Source: "hello", Translate: true},
			{ID: "2", Source: "world", Translate: true},
			{ID: "3", Source: "context after", Translate: false},
		},
	}
	_, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	m := mustUnmarshalUser(t, usr)
	if m.Segments["0"].Translate {
		t.Errorf("context segment 0 should have translate=false")
	}
	if !m.Segments["1"].Translate {
		t.Errorf("segment 1 should have translate=true")
	}
	if m.Segments["3"].Translate {
		t.Errorf("context segment 3 should have translate=false")
	}
}

func TestRenderer_SingleMode(t *testing.T) {
	r, err := NewRenderer(config.PromptConfig{
		SystemTemplateContent: defaultTestSystemTmpl,
	})
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "en", TargetLang: "zh",
		Source: "hello",
	}
	_, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	m := mustUnmarshalUser(t, usr)
	if len(m.Segments) != 1 {
		t.Fatalf("single mode should have 1 segment, got %d: %#v", len(m.Segments), m.Segments)
	}
	if m.Segments[SingleID].Source != "hello" {
		t.Errorf("single mode payload mismatch: %#v", m.Segments)
	}
	if !m.Segments[SingleID].Translate {
		t.Errorf("single mode should have translate=true")
	}
}

func TestRenderer_RejectsUserTemplate(t *testing.T) {
	_, err := NewRenderer(config.PromptConfig{UserTemplate: "anything.tmpl"})
	if err == nil {
		t.Fatal("expected error when user_template is set")
	}
}
