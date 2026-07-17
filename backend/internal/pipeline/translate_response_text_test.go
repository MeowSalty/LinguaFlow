package pipeline

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

var textRepairOpts = repair.Options{
	JSONStructural:   true,
	Partial:          true,
	PartialThreshold: 0.5,
	PromptUpgrade:    true,
}

func TestParseBatchResponseLenientText_HappyPath(t *testing.T) {
	text := "[1] 早上好\n[2] 很高兴认识你\n"
	r := parseBatchResponseLenientText(text, []string{"1", "2"}, textRepairOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "早上好" || r.Trans["2"] != "很高兴认识你" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
	if len(r.Glos) != 0 {
		t.Errorf("expected no glossary, got %d", len(r.Glos))
	}
}

func TestParseBatchResponseLenientText_WithContextSegments(t *testing.T) {
	text := "[*] Hello, how are you?\n[*] How's the weather?\n[1] Good morning\n[2] Nice to meet you\n[*] See you later\n"
	r := parseBatchResponseLenientText(text, []string{"1", "2"}, textRepairOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "Good morning" || r.Trans["2"] != "Nice to meet you" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
}

func TestParseBatchResponseLenientText_ContinuationLines(t *testing.T) {
	text := "[1] 第一行\n继续第二行\n继续第三行\n[2] 独立段落\n"
	r := parseBatchResponseLenientText(text, []string{"1", "2"}, textRepairOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v", r.ParseErr)
	}
	expected := "第一行\n继续第二行\n继续第三行"
	if r.Trans["1"] != expected {
		t.Errorf("wrong continuation: got %q, want %q", r.Trans["1"], expected)
	}
	if r.Trans["2"] != "独立段落" {
		t.Errorf("wrong trans[2]: %q", r.Trans["2"])
	}
}

func TestParseBatchResponseLenientText_WithGlossary(t *testing.T) {
	text := "[1] 早上好\n[2] 很高兴认识你\n\n[glossary]\nLinguaFlow | 灵流 | 翻译平台名称\nAPI | 接口\n"
	r := parseBatchResponseLenientText(text, []string{"1", "2"}, textRepairOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "早上好" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
	if len(r.Glos) != 2 {
		t.Fatalf("expected 2 glossary entries, got %d", len(r.Glos))
	}
	if r.Glos[0].Source != "LinguaFlow" || r.Glos[0].Target != "灵流" || r.Glos[0].Notes != "翻译平台名称" {
		t.Errorf("wrong glos[0]: %#v", r.Glos[0])
	}
	if r.Glos[1].Source != "API" || r.Glos[1].Target != "接口" || r.Glos[1].Notes != "" {
		t.Errorf("wrong glos[1]: %#v", r.Glos[1])
	}
}

func TestParseBatchResponseLenientText_MissingID(t *testing.T) {
	text := "[1] 早上好\n"
	opt := repair.Options{
		JSONStructural:   true,
		Partial:          true,
		PartialThreshold: 0.6,
	}
	r := parseBatchResponseLenientText(text, []string{"1", "2"}, opt)
	if r.Fatal {
		t.Fatalf("should be partial, not fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if len(r.Missing) != 1 || r.Missing[0] != "2" {
		t.Errorf("expected missing [2], got %v", r.Missing)
	}
}

func TestParseBatchResponseLenientText_EmptyLines(t *testing.T) {
	text := "\n[1] 早上好\n\n\n[2] 很高兴认识你\n\n"
	r := parseBatchResponseLenientText(text, []string{"1", "2"}, textRepairOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "早上好" || r.Trans["2"] != "很高兴认识你" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
}

func TestParseBatchResponseLenientText_EmptyResponse(t *testing.T) {
	r := parseBatchResponseLenientText("", []string{"1"}, textRepairOpts)
	if !r.Fatal {
		t.Fatal("expected fatal for empty response")
	}
}

func TestParseBatchResponseLenientText_CodeFence(t *testing.T) {
	text := "Here is the translation:\n\n```text\n[1] 早上好\n[2] 很高兴认识你\n```\n"
	r := parseBatchResponseLenientText(text, []string{"1", "2"}, textRepairOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "早上好" || r.Trans["2"] != "很高兴认识你" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
}

func TestParseBatchResponseLenientText_WithThinking(t *testing.T) {
	text := "<thinking>Let me translate these...</thinking>\n\n[1] 早上好\n[2] 很高兴认识你\n"
	r := parseBatchResponseLenientText(text, []string{"1", "2"}, textRepairOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "早上好" || r.Trans["2"] != "很高兴认识你" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
}

func TestParseBatchResponseLenientText_InlineRuby(t *testing.T) {
	text := "[1] 早上好\n[2] 很高兴⟦ruby:认识/にんしつ/phonetic⟧你\n"
	r := parseBatchResponseLenientText(text, []string{"1", "2"}, textRepairOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v", r.ParseErr)
	}
	if r.Trans["2"] != "很高兴⟦ruby:认识/にんしつ/phonetic⟧你" {
		t.Errorf("ruby marker lost: %q", r.Trans["2"])
	}
}

func TestParseBatchResponseLenientText_GlossaryWithTrailingTranslations(t *testing.T) {
	text := "[1] Hello\n[glossary]\nAPI | 接口\n[2] World\n"
	r := parseBatchResponseLenientText(text, []string{"1", "2"}, textRepairOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "Hello" || r.Trans["2"] != "World" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
	if len(r.Glos) != 1 || r.Glos[0].Source != "API" {
		t.Errorf("wrong glossary: %#v", r.Glos)
	}
}
