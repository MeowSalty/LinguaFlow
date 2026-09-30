package prompt_test

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
	"strings"
	"testing"
)

// mustNewDefaultRenderer 用真实内置翻译模板构建 prompt.Renderer，用于校验模板中的指令文本。
func mustNewDefaultRenderer(t *testing.T) *prompt.Renderer {
	t.Helper()
	r, err := prompt.NewRenderer(templates.EmbeddedPromptTemplate())
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	return r
}

func TestRenderer_SectionMode_EchoIDInstruction(t *testing.T) {
	r := mustNewDefaultRenderer(t)
	data := prompt.Data{
		SourceLang: "ja", TargetLang: "zh-Hans",
		Protocol: prompt.ProtocolText,
		RubyMode: prompt.RubyModeSection,
		Segments: []prompt.SegmentInput{
			{ID: "1", Source: "椎名は静かに微笑んだ。", Translate: true},
		},
		RubyAnnotations: map[string][]prompt.RubyAnnotation{
			"1": {{ID: "1", Base: "椎名", Text: "しいな"}},
		},
	}
	sys, _, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sys, "编号: 基底 | 标注 | 类型 | id") {
		t.Errorf("section system prompt missing 4-field echo-id output rule:\n%s", sys)
	}
	if !strings.Contains(sys, "#id") {
		t.Errorf("section system prompt missing #id echo explanation:\n%s", sys)
	}
	if strings.Contains(sys, "⟦ruby:") {
		t.Errorf("section system prompt should not carry inline marker instructions:\n%s", sys)
	}
}

func TestRenderer_InlineMode_Unchanged(t *testing.T) {
	r := mustNewDefaultRenderer(t)
	data := prompt.Data{
		SourceLang: "ja", TargetLang: "zh-Hans",
		Protocol: prompt.ProtocolText,
		RubyMode: prompt.RubyModeInline,
		Segments: []prompt.SegmentInput{
			{ID: "1", Source: "椎名は静かに微笑んだ。", Translate: true},
		},
		RubyAnnotations: map[string][]prompt.RubyAnnotation{
			"1": {{ID: "1", Base: "椎名", Text: "しいな"}},
		},
	}
	sys, _, err := r.Render(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sys, "⟦ruby:基底/标注/类型⟧") {
		t.Errorf("inline system prompt missing marker format:\n%s", sys)
	}
	if strings.Contains(sys, "基底 | 标注 | 类型 | id") {
		t.Errorf("inline system prompt should not carry section 4-field rule:\n%s", sys)
	}
	if strings.Contains(sys, "⟦ruby:基底/标注/类型/id⟧") {
		t.Errorf("inline system prompt should not inject id into marker:\n%s", sys)
	}
}

func TestRenderer_AllProtocolRubyModeCombosRender(t *testing.T) {
	r := mustNewDefaultRenderer(t)
	data := prompt.Data{
		SourceLang: "ja", TargetLang: "zh-Hans",
		Segments: []prompt.SegmentInput{
			{ID: "1", Source: "椎名は静かに微笑んだ。", Translate: true},
		},
		RubyAnnotations: map[string][]prompt.RubyAnnotation{
			"1": {{ID: "1", Base: "椎名", Text: "しいな"}},
		},
		InlineBootstrap:   true,
		MaxBootstrapTerms: 10,
	}
	combos := []struct {
		protocol prompt.Protocol
		mode     string
	}{
		{prompt.ProtocolText, prompt.RubyModeJSON},
		{prompt.ProtocolText, prompt.RubyModeInline},
		{prompt.ProtocolText, prompt.RubyModeSection},
		{prompt.ProtocolText, ""},
		{prompt.ProtocolJSONLoose, prompt.RubyModeJSON},
		{prompt.ProtocolJSONLoose, prompt.RubyModeInline},
		{prompt.ProtocolJSONLoose, prompt.RubyModeSection},
		{prompt.ProtocolJSONLoose, ""},
		{prompt.ProtocolJSONStrict, prompt.RubyModeJSON},
		{prompt.ProtocolJSONStrict, prompt.RubyModeInline},
		{prompt.ProtocolJSONStrict, prompt.RubyModeSection},
		{prompt.ProtocolJSONStrict, ""},
	}
	for _, tc := range combos {
		d := data
		d.Protocol = tc.protocol
		d.RubyMode = tc.mode
		if _, _, err := r.Render(d); err != nil {
			t.Errorf("render protocol=%s rubyMode=%q: %v", tc.protocol, tc.mode, err)
		}
	}
}
