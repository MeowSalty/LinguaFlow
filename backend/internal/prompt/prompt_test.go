package prompt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// defaultTestSystemTmpl 是测试用的最小系统模板。
// 内容与 templates/default/prompts/default.tmpl 的关键子集保持一致。
const defaultTestSystemTmpl = `你是 LinguaFlow，一个专业的翻译引擎。
将用户的文本从 {{.SourceLang}} 翻译为 {{.TargetLang}}。
协议：
- 你的回复必须是一个 JSON 对象：{"translations":{"<id>":"<翻译>", ...}}
- 仅输出 JSON，无 markdown 围栏、无额外文字。`

func TestBuildContext_PrefersOriginalSource(t *testing.T) {
	doc := &pipeline.Document{
		Segments: []pipeline.Segment{
			{OriginalSource: "first paragraph", Source: "__LF_000001__"},
			{OriginalSource: "middle paragraph", Source: "__LF_000002__"},
			{OriginalSource: "last paragraph", Source: "__LF_000003__"},
		},
	}
	prev, next := BuildContext(doc, 1)
	if prev != "first paragraph" {
		t.Errorf("prev want %q, got %q", "first paragraph", prev)
	}
	if next != "last paragraph" {
		t.Errorf("next want %q, got %q", "last paragraph", next)
	}
}

func TestBuildContext_FallbackToSource(t *testing.T) {
	doc := &pipeline.Document{
		Segments: []pipeline.Segment{
			{Source: "a"},
			{Source: "b"},
		},
	}
	prev, next := BuildContext(doc, 1)
	if prev != "a" {
		t.Errorf("prev want %q, got %q", "a", prev)
	}
	if next != "" {
		t.Errorf("next want empty, got %q", next)
	}
}

func TestBuildContextRange(t *testing.T) {
	doc := &pipeline.Document{
		Segments: []pipeline.Segment{
			{OriginalSource: "s0"},
			{OriginalSource: "s1"},
			{OriginalSource: "s2"},
			{OriginalSource: "s3"},
			{OriginalSource: "s4"},
		},
	}
	prev, next := BuildContextRange(doc, 1, 3)
	if prev != "s0" || next != "s4" {
		t.Errorf("got prev=%q next=%q", prev, next)
	}
}

type userMsg struct {
	SourceLang    string            `json:"source_lang"`
	TargetLang    string            `json:"target_lang"`
	ContextBefore string            `json:"context_before"`
	ContextAfter  string            `json:"context_after"`
	Segments      map[string]string `json:"segments"`
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
			{ID: "1", Source: "hello"},
			{ID: "2", Source: "world"},
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
	if m.Segments["1"] != "hello" || m.Segments["2"] != "world" {
		t.Errorf("segments mismatch: %#v", m.Segments)
	}
	if _, ok := m.Segments[SingleID]; ok {
		t.Errorf("batch mode should not carry single-id %q", SingleID)
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
	if m.Segments[SingleID] != "hello" {
		t.Errorf("single mode payload mismatch: %#v", m.Segments)
	}
}

func TestRenderer_EmbedsContext(t *testing.T) {
	r, err := NewRenderer(config.PromptConfig{
		SystemTemplateContent: defaultTestSystemTmpl,
	})
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	_, usr, err := r.Render(Data{
		SourceLang: "en", TargetLang: "zh",
		Source:      "middle",
		PrevContext: "before-paragraph",
		NextContext: "after-paragraph",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	m := mustUnmarshalUser(t, usr)
	if m.ContextBefore != "before-paragraph" || m.ContextAfter != "after-paragraph" {
		t.Errorf("context not embedded: %#v", m)
	}
}

func TestRenderer_RejectsUserTemplate(t *testing.T) {
	_, err := NewRenderer(config.PromptConfig{UserTemplate: "anything.tmpl"})
	if err == nil {
		t.Fatal("expected error when user_template is set")
	}
}
