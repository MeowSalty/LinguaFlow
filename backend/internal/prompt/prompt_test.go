package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

// defaultTestSystemTmpl 是测试用的最小系统模板。
// 内容与 templates/default/prompts/default_translation.tmpl 的关键子集保持一致。
const defaultTestSystemTmpl = `你是 LinguaFlow，一个专业的翻译引擎。
将用户的文本从 {{.SourceLang}} 翻译为 {{.TargetLang}}。
协议：
- 用户消息是一个 JSON 对象，包含 source_lang、target_lang、segments 字段。
- segments 中每个条目包含 "source"（原文）和 "translate"（是否需要翻译）。
- 你的回复必须是一个 JSON 对象，其中 "translations" 仅包含 translate=true 的段落。
- 仅输出 JSON，无 markdown 围栏、无额外文字。
{{- if not .StrictSchema}}
- 输出 JSON 结构如下：{"translations":{"<id>":"<翻译>"}}。
{{- end}}`

func TestStrictSchemaFromResponseMode(t *testing.T) {
	cases := []struct {
		mode string
		want bool
	}{
		{"", true},
		{"json_schema", true},
		{"json_object", false},
		{"text", false},
		{"none", false},
	}
	for _, tc := range cases {
		if got := StrictSchemaFromResponseMode(tc.mode); got != tc.want {
			t.Errorf("StrictSchemaFromResponseMode(%q)=%v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestRenderer_StrictSchemaOmitsShape(t *testing.T) {
	r, err := NewRenderer(defaultTestSystemTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	sysStrict, _, err := r.Render(Data{
		SourceLang: "en", TargetLang: "zh",
		Segments:     []SegmentInput{{ID: "0", Source: "hi", Translate: true}},
		StrictSchema: true,
	})
	if err != nil {
		t.Fatalf("render strict: %v", err)
	}
	if strings.Contains(sysStrict, "输出 JSON 结构如下") {
		t.Errorf("strict schema should omit shape example:\n%s", sysStrict)
	}

	sysLoose, _, err := r.Render(Data{
		SourceLang: "en", TargetLang: "zh",
		Segments:     []SegmentInput{{ID: "0", Source: "hi", Translate: true}},
		StrictSchema: false,
	})
	if err != nil {
		t.Fatalf("render loose: %v", err)
	}
	if !strings.Contains(sysLoose, "输出 JSON 结构如下") {
		t.Errorf("json_object mode should include shape example:\n%s", sysLoose)
	}
}

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
	r, err := NewRenderer(defaultTestSystemTmpl)
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
	r, err := NewRenderer(defaultTestSystemTmpl)
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
	r, err := NewRenderer(defaultTestSystemTmpl)
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

func TestRenderer_TextMode(t *testing.T) {
	r, err := NewRenderer(`你是 LinguaFlow，一个专业的翻译引擎。
将用户的文本从 {{.SourceLang}} 翻译为 {{.TargetLang}}。
{{- if .TextMode}}

输出格式要求：
- 每个需要翻译的段落输出一行，格式为 [编号] 翻译文本
- 编号必须与原文中的编号完全一致
{{- else}}

协议与输出规则：
- 用户消息是一个 JSON 对象。
{{- end}}`)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "en", TargetLang: "zh",
		TextMode: true,
		Segments: []SegmentInput{
			{ID: "1", Source: "hello", Translate: true},
			{ID: "2", Source: "world", Translate: true},
		},
	}
	sys, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sys, "输出格式要求") {
		t.Errorf("system prompt missing text mode format instructions:\n%s", sys)
	}
	if strings.Contains(sys, "JSON") {
		t.Errorf("system prompt should not contain JSON instructions in text mode:\n%s", sys)
	}
	if !strings.Contains(usr, "[1] hello") {
		t.Errorf("user message missing segment 1:\n%s", usr)
	}
	if !strings.Contains(usr, "[2] world") {
		t.Errorf("user message missing segment 2:\n%s", usr)
	}
	// user 消息不应包含指令性文字
	if strings.Contains(usr, "翻译") {
		t.Errorf("user message should not contain instructions:\n%s", usr)
	}
}

func TestRenderer_TextModeWithContext(t *testing.T) {
	r, err := NewRenderer(`{{- if .TextMode}}text mode{{- else}}json mode{{- end}}`)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "en", TargetLang: "zh",
		TextMode: true,
		Segments: []SegmentInput{
			{ID: "0", Source: "context before", Translate: false},
			{ID: "1", Source: "hello", Translate: true},
			{ID: "2", Source: "world", Translate: true},
			{ID: "3", Source: "context after", Translate: false},
		},
	}
	sys, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sys, "text mode") {
		t.Errorf("system prompt should indicate text mode:\n%s", sys)
	}
	if !strings.Contains(usr, "[*] context before") {
		t.Errorf("user message missing context segment:\n%s", usr)
	}
	if !strings.Contains(usr, "[1] hello") {
		t.Errorf("user message missing segment 1:\n%s", usr)
	}
	if !strings.Contains(usr, "[2] world") {
		t.Errorf("user message missing segment 2:\n%s", usr)
	}
	if !strings.Contains(usr, "[*] context after") {
		t.Errorf("user message missing context segment:\n%s", usr)
	}
}

func TestRenderer_TextModeUserFormat(t *testing.T) {
	r, err := NewRenderer(defaultTestSystemTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "ja", TargetLang: "zh-Hans",
		TextMode: true,
		Segments: []SegmentInput{
			{ID: "1", Source: "本電子書籍を示すサムネイルなどのイメージ画像は、再ダウンロード時に予告なく変更される場合があります。", Translate: true},
			{ID: "2", Source: "本電子書籍は縦書きでレイアウトされています。", Translate: true},
			{ID: "*", Source: "CONTENTS", Translate: false},
		},
	}
	_, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	expected := "[1] 本電子書籍を示すサムネイルなどのイメージ画像は、再ダウンロード時に予告なく変更される場合があります。\n[2] 本電子書籍は縦書きでレイアウトされています。\n[*] CONTENTS"
	if usr != expected {
		t.Errorf("user message format mismatch:\ngot:\n%s\nwant:\n%s", usr, expected)
	}
}

func TestRenderer_TextModeWithRuby(t *testing.T) {
	r, err := NewRenderer(defaultTestSystemTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "ja", TargetLang: "zh-Hans",
		TextMode: true,
		RubyMode: RubyModeInline,
		Segments: []SegmentInput{
			{ID: "1", Source: "椎名は静かに微笑んだ。", Translate: true},
			{ID: "2", Source: "少年が呪を唱えた。", Translate: true},
			{ID: "*", Source: "CONTEXT", Translate: false},
		},
		RubyAnnotations: map[string][]RubyAnnotation{
			"1": {{Base: "椎名", Text: "しいな"}, {Base: "微笑", Text: "ほほえ"}},
			"2": {{Base: "呪", Text: "じゅ"}},
		},
	}
	_, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(usr, "[1] ⟦ruby:椎名/しいな⟧は静かに⟦ruby:微笑/ほほえ⟧んだ。") {
		t.Errorf("user message missing inline ruby for segment 1:\n%s", usr)
	}
	if !strings.Contains(usr, "[2] 少年が⟦ruby:呪/じゅ⟧を唱えた。") {
		t.Errorf("user message missing inline ruby for segment 2:\n%s", usr)
	}
	if !strings.Contains(usr, "[*] CONTEXT") {
		t.Errorf("user message missing context segment:\n%s", usr)
	}
	if strings.Contains(usr, "[ruby]") {
		t.Errorf("user message should not contain [ruby] section in inline mode:\n%s", usr)
	}
}

func TestRenderer_TextModeWithEmptyRuby(t *testing.T) {
	r, err := NewRenderer(defaultTestSystemTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "ja", TargetLang: "zh-Hans",
		TextMode: true,
		Segments: []SegmentInput{
			{ID: "1", Source: "hello", Translate: true},
		},
		RubyAnnotations: map[string][]RubyAnnotation{},
	}
	_, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(usr, "[ruby]") {
		t.Errorf("user message should not contain [ruby] section when annotations are empty:\n%s", usr)
	}
}

func TestRenderer_TextModeWithRubyInline(t *testing.T) {
	r, err := NewRenderer(defaultTestSystemTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "ja", TargetLang: "zh-Hans",
		TextMode: true,
		RubyMode: RubyModeInline,
		Segments: []SegmentInput{
			{ID: "1", Source: "椎名は静かに微笑んだ。", Translate: true},
			{ID: "2", Source: "少年が呪を唱えた。", Translate: true},
			{ID: "*", Source: "CONTEXT", Translate: false},
		},
		RubyAnnotations: map[string][]RubyAnnotation{
			"1": {{Base: "椎名", Text: "しいな"}, {Base: "微笑", Text: "ほほえ"}},
			"2": {{Base: "呪", Text: "じゅ"}},
		},
	}
	_, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(usr, "[1] ⟦ruby:椎名/しいな⟧は静かに⟦ruby:微笑/ほほえ⟧んだ。") {
		t.Errorf("user message missing inline ruby for segment 1:\n%s", usr)
	}
	if !strings.Contains(usr, "[2] 少年が⟦ruby:呪/じゅ⟧を唱えた。") {
		t.Errorf("user message missing inline ruby for segment 2:\n%s", usr)
	}
	if !strings.Contains(usr, "[*] CONTEXT") {
		t.Errorf("user message missing context segment:\n%s", usr)
	}
	if strings.Contains(usr, "[ruby]") {
		t.Errorf("user message should not contain [ruby] section in inline mode:\n%s", usr)
	}
}

func TestRenderer_TextModeWithRubySection(t *testing.T) {
	r, err := NewRenderer(defaultTestSystemTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "ja", TargetLang: "zh-Hans",
		TextMode: true,
		RubyMode: RubyModeSection,
		Segments: []SegmentInput{
			{ID: "1", Source: "椎名は静かに微笑んだ。", Translate: true},
			{ID: "2", Source: "少年が呪を唱えた。", Translate: true},
			{ID: "*", Source: "CONTEXT", Translate: false},
		},
		RubyAnnotations: map[string][]RubyAnnotation{
			"1": {{Base: "椎名", Text: "しいな"}, {Base: "微笑", Text: "ほほえ"}},
			"2": {{Base: "呪", Text: "じゅ"}},
		},
	}
	_, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// section 模式下，原文不应包含 inline ruby 标记
	if strings.Contains(usr, "⟦ruby:") {
		t.Errorf("user message should not contain inline ruby markers in section mode:\n%s", usr)
	}
	// 原文应保持干净
	if !strings.Contains(usr, "[1] 椎名は静かに微笑んだ。") {
		t.Errorf("user message missing clean segment 1:\n%s", usr)
	}
	if !strings.Contains(usr, "[2] 少年が呪を唱えた。") {
		t.Errorf("user message missing clean segment 2:\n%s", usr)
	}
	// 应包含 [ruby] 段落
	if !strings.Contains(usr, "[ruby]") {
		t.Errorf("user message missing [ruby] section:\n%s", usr)
	}
	if !strings.Contains(usr, "1: 椎名/しいな, 微笑/ほほえ") {
		t.Errorf("user message missing ruby annotations for segment 1:\n%s", usr)
	}
	if !strings.Contains(usr, "2: 呪/じゅ") {
		t.Errorf("user message missing ruby annotations for segment 2:\n%s", usr)
	}
	if !strings.Contains(usr, "[*] CONTEXT") {
		t.Errorf("user message missing context segment:\n%s", usr)
	}
}

func TestRenderer_TextModeWithRubySectionEmpty(t *testing.T) {
	r, err := NewRenderer(defaultTestSystemTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "ja", TargetLang: "zh-Hans",
		TextMode: true,
		RubyMode: RubyModeSection,
		Segments: []SegmentInput{
			{ID: "1", Source: "hello", Translate: true},
		},
		RubyAnnotations: map[string][]RubyAnnotation{},
	}
	_, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(usr, "[ruby]") {
		t.Errorf("user message should not contain [ruby] section when annotations are empty:\n%s", usr)
	}
}

func TestRenderer_TextModeWithRubyDefaultMode(t *testing.T) {
	r, err := NewRenderer(defaultTestSystemTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	data := Data{
		SourceLang: "ja", TargetLang: "zh-Hans",
		TextMode: true,
		// RubyMode 为空，应默认为 section（与引擎 resolveRubyMode 的 text 模式默认一致）
		Segments: []SegmentInput{
			{ID: "1", Source: "椎名は静かに微笑んだ。", Translate: true},
		},
		RubyAnnotations: map[string][]RubyAnnotation{
			"1": {{Base: "椎名", Text: "しいな"}},
		},
	}
	_, usr, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// 默认模式应使用 section 格式
	if !strings.Contains(usr, "[ruby]") {
		t.Errorf("default mode should contain [ruby] section:\n%s", usr)
	}
	if strings.Contains(usr, "⟦ruby:") {
		t.Errorf("default mode should not use inline ruby format:\n%s", usr)
	}
}
