package repair

import (
	"testing"
)

var textOpts = Options{
	JSONStructural:   true,
	Partial:          true,
	PartialThreshold: 0.5,
	PromptUpgrade:    true,
}

func TestTryRepairText_HappyPath(t *testing.T) {
	in := "[1] 早上好\n[2] 很高兴认识你\n"
	r := TryRepairText(in, []string{"1", "2"}, textOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "早上好" || r.Trans["2"] != "很高兴认识你" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
	if len(r.Missing) != 0 {
		t.Errorf("expected no missing, got %v", r.Missing)
	}
}

func TestTryRepairText_WithContextSegments(t *testing.T) {
	in := "[*] Hello, how are you?\n[1] Good morning\n[*] See you later\n[2] Nice to meet you\n"
	r := TryRepairText(in, []string{"1", "2"}, textOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "Good morning" || r.Trans["2"] != "Nice to meet you" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
}

func TestTryRepairText_WithGlossary(t *testing.T) {
	in := "[1] 早上好\n[2] 很高兴认识你\n\n[glossary]\nLinguaFlow | 灵流 | 翻译平台名称\nAPI | 接口 | 应用程序编程接口\n"
	r := TryRepairText(in, []string{"1", "2"}, textOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v", r.ParseErr)
	}
	if len(r.Glos) != 2 {
		t.Fatalf("expected 2 glossary entries, got %d", len(r.Glos))
	}
	if r.Glos[0].Source != "LinguaFlow" || r.Glos[0].Target != "灵流" {
		t.Errorf("wrong glossary[0]: %#v", r.Glos[0])
	}
	if r.Glos[1].Source != "API" || r.Glos[1].Notes != "应用程序编程接口" {
		t.Errorf("wrong glossary[1]: %#v", r.Glos[1])
	}
}

func TestTryRepairText_MissingID(t *testing.T) {
	in := "[1] 早上好\n"
	opts := Options{
		JSONStructural:   true,
		Partial:          true,
		PartialThreshold: 0.6, // 缺失率 0.5 < 0.6，应为 partial
	}
	r := TryRepairText(in, []string{"1", "2"}, opts)
	if r.Fatal {
		t.Fatalf("should be partial, not fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if len(r.Missing) != 1 || r.Missing[0] != "2" {
		t.Errorf("expected missing [2], got %v", r.Missing)
	}
}

func TestTryRepairText_MissingID_Fatal(t *testing.T) {
	// 当缺失率 >= threshold 时应为 fatal
	in := "[1] a\n"
	opts := Options{
		JSONStructural:   true,
		Partial:          true,
		PartialThreshold: 0.3, // 缺失率 0.75 >= 0.3
	}
	r := TryRepairText(in, []string{"1", "2", "3", "4"}, opts)
	if !r.Fatal {
		t.Errorf("expected fatal for high missing ratio, got missing=%v", r.Missing)
	}
}

func TestTryRepairText_CodeFence(t *testing.T) {
	in := "```text\n[1] 早上好\n[2] 很高兴认识你\n```\n"
	r := TryRepairText(in, []string{"1", "2"}, textOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "早上好" || r.Trans["2"] != "很高兴认识你" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
}

func TestTryRepairText_BOM(t *testing.T) {
	in := "\uFEFF[1] 早上好\n[2] 很高兴认识你\n"
	r := TryRepairText(in, []string{"1", "2"}, textOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "早上好" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
}

func TestTryRepairText_ContinuationLines(t *testing.T) {
	in := "[1] 第一行\n继续第二行\n[2] 独立段落\n"
	r := TryRepairText(in, []string{"1", "2"}, textOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v", r.ParseErr)
	}
	expected := "第一行\n继续第二行"
	if r.Trans["1"] != expected {
		t.Errorf("wrong continuation: got %q, want %q", r.Trans["1"], expected)
	}
}

func TestTryRepairText_EmptyResponse(t *testing.T) {
	r := TryRepairText("", []string{"1"}, textOpts)
	if !r.Fatal {
		t.Error("expected fatal for empty response")
	}
}

func TestTryRepairText_NoTranslations(t *testing.T) {
	r := TryRepairText("just some random text\n", []string{"1"}, textOpts)
	if !r.Fatal {
		t.Error("expected fatal for no translations")
	}
}

func TestTryRepairText_RepairTracking(t *testing.T) {
	in := "```text\n[1] 早上好\n```\n"
	r := TryRepairText(in, []string{"1"}, textOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v", r.ParseErr)
	}
	found := false
	for _, op := range r.Repaired {
		if op == "text.strip-code-fence" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected strip-code-fence repair, got %v", r.Repaired)
	}
}

func TestStripCodeFence(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no fence", "hello", "hello"},
		{"fenced", "```text\nhello\n```", "hello"},
		{"fenced no lang", "```\nhello\n```", "hello"},
		{"nested ```", "```text\n```\ninner\n```", "```\ninner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripCodeFence(tt.in)
			if got != tt.want {
				t.Errorf("stripCodeFence(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTryRepairText_GlossaryWithTrailingTranslations(t *testing.T) {
	in := "[1] Hello\n[glossary]\nAPI | 接口\n[2] World\n"
	r := TryRepairText(in, []string{"1", "2"}, textOpts)
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
