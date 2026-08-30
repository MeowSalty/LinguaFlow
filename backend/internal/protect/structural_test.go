package protect

import (
	"errors"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// TestIsStructuralOnly 验证结构段判定与保护链同源、随配置浮动。
// 表按 protector 配置分组：nil（无保护配置的降级）、全四规则（config 默认）、
// 缩减规则、含 ruby 的组合链——同一输入在不同配置下期望可不同，这正是
// "判定随配置浮动"语义的回归锚点。
func TestIsStructuralOnly(t *testing.T) {
	allRules := []string{"code", "link", "placeholder", "xml"}

	tests := []struct {
		name string
		text string
		p    Protector
		want bool
	}{
		// --- nil protector：占位符语法退化为普通文本（浮动语义端点） ---
		{name: "nil protector pure punctuation", text: "◇◇◇", p: nil, want: true},
		{name: "nil protector ellipsis", text: "……", p: nil, want: true},
		{name: "nil protector asterisks", text: "***", p: nil, want: true},
		{name: "nil protector bare placeholder is plain text", text: "{{name}}", p: nil, want: false},
		{name: "nil protector mixed placeholder punctuation", text: "……{{name}}", p: nil, want: false},
		{name: "nil protector empty string", text: "", p: nil, want: true},

		// --- 空与纯空白（快路径短路） ---
		{name: "empty string", text: "", p: FromRules(allRules), want: true},
		{name: "whitespace only", text: "   \n\t", p: FromRules(allRules), want: true},

		// --- 纯占位符 / 纯结构语法（整段被保护吞掉） ---
		{name: "double brace only", text: "{{name}}", p: FromRules(allRules), want: true},
		{name: "printf verb only", text: "%s", p: FromRules(allRules), want: true},
		{name: "shell var only", text: "${var}", p: FromRules(allRules), want: true},
		{name: "single brace only", text: "{user_name}", p: FromRules(allRules), want: true},
		{name: "self closing xml only", text: "<br/>", p: FromRules(allRules), want: true},
		{name: "closing xml only", text: "</p>", p: FromRules(allRules), want: true},
		{name: "inline code only", text: "`code`", p: FromRules(allRules), want: true},

		// --- 混合段：保护跨度 + 残余仅标点（本次统一行为的核心） ---
		{name: "ellipsis with placeholder", text: "……{{name}}", p: FromRules(allRules), want: true},
		{name: "brackets with printf verb", text: "【%s】", p: FromRules(allRules), want: true},
		{name: "xml with ellipsis", text: "<br/>...<br/>", p: FromRules(allRules), want: true},
		{name: "code span with ellipsis", text: "`code`……", p: FromRules(allRules), want: true},

		// --- 有可译内容（非结构段） ---
		{name: "plain english", text: "Hello world", p: FromRules(allRules), want: false},
		{name: "cjk text", text: "你好，世界", p: FromRules(allRules), want: false},
		{name: "text with embedded placeholder", text: "Hello {{name}}", p: FromRules(allRules), want: false},
		{name: "printf with visible text", text: "%d items", p: FromRules(allRules), want: false},
		{name: "link with visible text", text: "[Click here](http://x)", p: FromRules(allRules), want: false},
		{name: "digits only", text: "123", p: FromRules(allRules), want: false},

		// --- 边界：link 子跨度粒度 / printf 动词部分匹配 / 字面量 key ---
		{name: "empty label link", text: "[](http://x)", p: FromRules(allRules), want: true},
		{name: "percent with text", text: "50% off", p: FromRules(allRules), want: false},
		{name: "bare percent number", text: "100%", p: FromRules(allRules), want: false},
		{name: "literal placeholder key not in mapping", text: "__LF_000001__", p: FromRules(allRules), want: false},

		// --- 缩减规则：xml 未配置时 <br/> 是普通文本 ---
		{name: "xml not protected under reduced rules", text: "<br/>", p: FromRules([]string{"placeholder"}), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStructuralOnly(tt.p, tt.text); got != tt.want {
				t.Errorf("IsStructuralOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsStructuralOnly_RubyComposed 验证含 ruby 的组合链：
// ruby 剥离注音但保留汉字底文——底文有字则非结构段；
// 空底文的退化元素剥离后无内容，判为结构段。
func TestIsStructuralOnly_RubyComposed(t *testing.T) {
	allRules := []string{"code", "link", "placeholder", "xml"}
	p := Compose(NewRubyProtector(), FromRules(allRules))

	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "ruby with kanji base keeps base text", text: "<ruby>漢<rt>かん</rt></ruby>", want: false},
		{name: "ruby with empty base degrades to structural", text: "<ruby><rt>あ</rt></ruby>", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStructuralOnly(p, tt.text); got != tt.want {
				t.Errorf("IsStructuralOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

// failingProtector 保护链恒失败，用于验证分析失败路径的 fail-safe 与错误透传。
type failingProtector struct{}

func (failingProtector) Name() string                   { return "failing" }
func (failingProtector) Unprotect(*model.Segment) error { return nil }
func (failingProtector) Protect(*model.Segment) error   { return errors.New("protect failed") }

// TestAnalyzeStructural_Artifacts 验证保护产物与直接调用 ProtectText 完全一致，
// 以及零产物路径：短路（纯标点）、nil protector、保护链失败。
func TestAnalyzeStructural_Artifacts(t *testing.T) {
	allRules := []string{"code", "link", "placeholder", "xml"}
	p := FromRules(allRules)

	t.Run("artifacts match ProtectText", func(t *testing.T) {
		text := "Hello {{name}}, see [link](http://x)"
		a := AnalyzeStructural(p, text)
		if a.Structural {
			t.Fatalf("Structural = true, want false")
		}
		if a.Err != nil {
			t.Fatalf("Err = %v, want nil", a.Err)
		}
		wantProtected, wantMapping, err := ProtectText(p, text)
		if err != nil {
			t.Fatalf("ProtectText: %v", err)
		}
		if a.Protected != wantProtected {
			t.Errorf("Protected = %q, want %q", a.Protected, wantProtected)
		}
		if !reflect.DeepEqual(a.Mapping, wantMapping) {
			t.Errorf("Mapping = %v, want %v", a.Mapping, wantMapping)
		}
	})

	t.Run("structural text still carries artifacts", func(t *testing.T) {
		a := AnalyzeStructural(p, "……{{name}}")
		if !a.Structural {
			t.Fatalf("Structural = false, want true")
		}
		if a.Protected == "" || len(a.Mapping) == 0 {
			t.Errorf("structural analysis should still carry artifacts: %+v", a)
		}
	})

	t.Run("short circuit carries no artifacts", func(t *testing.T) {
		a := AnalyzeStructural(p, "◇◇◇")
		if !a.Structural || a.Err != nil {
			t.Fatalf("Analysis = %+v, want Structural=true, no err", a)
		}
		if a.Protected != "" || a.Mapping != nil {
			t.Errorf("short-circuit analysis should carry no artifacts: %+v", a)
		}
	})

	t.Run("nil protector carries no artifacts", func(t *testing.T) {
		a := AnalyzeStructural(nil, "Hello {{name}}")
		if a.Structural || a.Err != nil {
			t.Fatalf("Analysis = %+v, want Structural=false, no err", a)
		}
		if a.Protected != "" || a.Mapping != nil {
			t.Errorf("nil protector should carry no artifacts: %+v", a)
		}
	})

	t.Run("protect error propagates fail-safe", func(t *testing.T) {
		a := AnalyzeStructural(failingProtector{}, "Hello {{name}}")
		if a.Err == nil {
			t.Fatalf("Err = nil, want non-nil")
		}
		if a.Structural {
			t.Errorf("Structural = true, want false (fail-safe: treat as content)")
		}
		if a.Protected != "" || a.Mapping != nil {
			t.Errorf("failed analysis should carry no artifacts: %+v", a)
		}
	})
}

// TestAnalysis_Apply 验证落盘语义：产物与直接 Protect 同结果、
// 零产物路径无副作用、错误透传且不改动 seg。
func TestAnalysis_Apply(t *testing.T) {
	allRules := []string{"code", "link", "placeholder", "xml"}
	p := FromRules(allRules)

	t.Run("apply matches direct Protect", func(t *testing.T) {
		text := "Hello {{name}}"
		got := &model.Segment{Source: text}
		if err := AnalyzeStructural(p, text).Apply(got); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		want := &model.Segment{Source: text}
		if err := p.Protect(want); err != nil {
			t.Fatalf("Protect: %v", err)
		}
		if got.Source != want.Source {
			t.Errorf("Source = %q, want %q", got.Source, want.Source)
		}
		if !reflect.DeepEqual(got.Protected, want.Protected) {
			t.Errorf("Protected = %v, want %v", got.Protected, want.Protected)
		}
	})

	t.Run("no artifacts is a no-op", func(t *testing.T) {
		seg := &model.Segment{Source: "◇◇◇"}
		if err := AnalyzeStructural(p, "◇◇◇").Apply(seg); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if seg.Source != "◇◇◇" || seg.Protected != nil {
			t.Errorf("short-circuit apply should be a no-op: %+v", seg)
		}
	})

	t.Run("error propagates without mutation", func(t *testing.T) {
		seg := &model.Segment{Source: "Hello {{name}}"}
		if err := AnalyzeStructural(failingProtector{}, "Hello {{name}}").Apply(seg); err == nil {
			t.Fatalf("Apply err = nil, want non-nil")
		}
		if seg.Source != "Hello {{name}}" || seg.Protected != nil {
			t.Errorf("failed apply should not mutate seg: %+v", seg)
		}
	})
}

// TestAnalysis_Apply_RubyChain 钉死 Apply ≡ 直接 Protect 的完整落盘契约，
// 含保护链的 Meta 副作用（回归：旧 Apply 只搬运 Source/Protected，启用 ruby 的
// 链经临时段执行时 ruby_items 副作用被丢弃，译后注音回填整体静默失效）。
func TestAnalysis_Apply_RubyChain(t *testing.T) {
	allRules := []string{"code", "link", "placeholder", "xml"}
	p := Compose(NewRubyProtector(), FromRules(allRules))

	applyEqualsProtect := func(t *testing.T, text string) {
		t.Helper()
		got := &model.Segment{Source: text}
		if err := AnalyzeStructural(p, text).Apply(got); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		want := &model.Segment{Source: text}
		if err := p.Protect(want); err != nil {
			t.Fatalf("Protect: %v", err)
		}
		if got.Source != want.Source {
			t.Errorf("Source = %q, want %q", got.Source, want.Source)
		}
		if !reflect.DeepEqual(got.Protected, want.Protected) {
			t.Errorf("Protected = %v, want %v", got.Protected, want.Protected)
		}
		if !reflect.DeepEqual(got.Meta, want.Meta) {
			t.Errorf("Meta = %v, want %v", got.Meta, want.Meta)
		}
	}

	t.Run("ruby with other protected spans", func(t *testing.T) {
		applyEqualsProtect(t, "<ruby>漢<rt>かん</rt></ruby> and {{name}}")
	})

	t.Run("ruby only keeps stripped source and meta despite empty mapping", func(t *testing.T) {
		// 回归锚点：无占位符跨度时 Mapping 为 nil，但链仍剥离了 ruby 标签并写入
		// Meta——Apply 必须落盘 Source 与 Meta，否则 ruby 标签裸留 Source 进 prompt。
		text := "<ruby>漢<rt>かん</rt></ruby>です"
		got := &model.Segment{Source: text}
		a := AnalyzeStructural(p, text)
		if err := a.Apply(got); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if a.Mapping != nil {
			t.Fatalf("Mapping = %v, want nil (no placeholder spans)", a.Mapping)
		}
		want := &model.Segment{Source: text}
		if err := p.Protect(want); err != nil {
			t.Fatalf("Protect: %v", err)
		}
		if got.Source != want.Source {
			t.Errorf("Source = %q, want %q (ruby tags must be stripped)", got.Source, want.Source)
		}
		if !reflect.DeepEqual(got.Meta, want.Meta) {
			t.Errorf("Meta = %v, want %v (ruby_items must be applied)", got.Meta, want.Meta)
		}
		if _, ok := got.Meta["ruby_items"]; !ok {
			t.Errorf("Meta missing ruby_items after Apply: %v", got.Meta)
		}
	})

	t.Run("apply merges meta into existing seg meta", func(t *testing.T) {
		text := "<ruby>漢<rt>かん</rt></ruby>です"
		got := &model.Segment{Source: text, Meta: map[string]any{"block": "p"}}
		if err := AnalyzeStructural(p, text).Apply(got); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if got.Meta["block"] != "p" {
			t.Errorf("existing meta key clobbered: %v", got.Meta)
		}
		if _, ok := got.Meta["ruby_items"]; !ok {
			t.Errorf("ruby_items not merged into existing meta: %v", got.Meta)
		}
	})
}
