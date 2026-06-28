package protect

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// T6: ruby_output 模式 — 文本匹配还原。
// 给定译文包含基底文本，RubyRestorer 应在匹配位置插入 <ruby> 标签。
func TestRubyRestorer_RubyOutput_BasicRestore(t *testing.T) {
	restorer := NewRubyRestorer("ruby_output")

	cases := []struct {
		name       string
		target     string
		output     []RubyOutputEntry
		wantTarget string
	}{
		{
			name:   "single entry match",
			target: "呪を唱える",
			output: []RubyOutputEntry{
				{Base: "呪", Text: "じゅ"},
			},
			wantTarget: `<ruby>呪<rt>じゅ</rt></ruby>を唱える`,
		},
		{
			name:   "multiple entries",
			target: "呪を唱えて微笑む",
			output: []RubyOutputEntry{
				{Base: "呪", Text: "じゅ"},
				{Base: "微笑", Text: "ほほえ"},
			},
			wantTarget: `<ruby>呪<rt>じゅ</rt></ruby>を唱えて<ruby>微笑<rt>ほほえ</rt></ruby>む`,
		},
		{
			name:   "word-level annotation",
			target: "椎名が来た",
			output: []RubyOutputEntry{
				{Base: "椎名", Text: "しいな"},
			},
			wantTarget: `<ruby>椎名<rt>しいな</rt></ruby>が来た`,
		},
		{
			name:       "empty output list",
			target:     "呪を唱える",
			output:     nil,
			wantTarget: "呪を唱える",
		},
		{
			name:       "empty base string skipped",
			target:     "呪を唱える",
			output:     []RubyOutputEntry{{Base: "", Text: "skip"}},
			wantTarget: "呪を唱える",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seg := &model.Segment{Target: tc.target}
			if err := restorer.Restore(seg, tc.output, nil); err != nil {
				t.Fatalf("Restore(%q): %v", tc.target, err)
			}
			if seg.Target != tc.wantTarget {
				t.Errorf("Restore(%q):\n  want: %q\n  got:  %q", tc.target, tc.wantTarget, seg.Target)
			}
		})
	}
}

// T7: inline_markers 模式 — 正则还原。
// 译文中的 ⟦ruby:base/text⟧ 标记应被替换为 <ruby> 标签。
func TestRubyRestorer_InlineMarkers_BasicRestore(t *testing.T) {
	restorer := NewRubyRestorer("inline_markers")

	cases := []struct {
		name       string
		target     string
		wantTarget string
	}{
		{
			name:       "single inline marker",
			target:     `⟦ruby:呪/じゅ⟧を唱える`,
			wantTarget: `<ruby>呪<rt>じゅ</rt></ruby>を唱える`,
		},
		{
			name:       "multiple inline markers",
			target:     `⟦ruby:呪/じゅ⟧を唱えて⟦ruby:微笑/ほほえ⟧む`,
			wantTarget: `<ruby>呪<rt>じゅ</rt></ruby>を唱えて<ruby>微笑<rt>ほほえ</rt></ruby>む`,
		},
		{
			name:       "no markers",
			target:     `plain text without markers`,
			wantTarget: `plain text without markers`,
		},
		{
			name:       "marker with word-level base",
			target:     `⟦ruby:椎名/しいな⟧が来た`,
			wantTarget: `<ruby>椎名<rt>しいな</rt></ruby>が来た`,
		},
		{
			name:       "marker in mixed context with XML",
			target:     `────⟦ruby:椎名/しいな⟧です`,
			wantTarget: `────<ruby>椎名<rt>しいな</rt></ruby>です`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seg := &model.Segment{Target: tc.target}
			// inline_markers 模式不使用 rubyOutput 参数
			if err := restorer.Restore(seg, nil, nil); err != nil {
				t.Fatalf("Restore(%q): %v", tc.target, err)
			}
			if seg.Target != tc.wantTarget {
				t.Errorf("Restore(%q):\n  want: %q\n  got:  %q", tc.target, tc.wantTarget, seg.Target)
			}
		})
	}
}

// T8: 译文中找不到基底文本 → 静默跳过，不报错。
func TestRubyRestorer_RubyOutput_BaseNotFound(t *testing.T) {
	restorer := NewRubyRestorer("ruby_output")

	cases := []struct {
		name   string
		target string
		output []RubyOutputEntry
	}{
		{
			name:   "single base not in target",
			target: "翻訳されたテキスト",
			output: []RubyOutputEntry{
				{Base: "呪", Text: "じゅ"},
			},
		},
		{
			name:   "all bases missing",
			target: "completely different text",
			output: []RubyOutputEntry{
				{Base: "呪", Text: "じゅ"},
				{Base: "微笑", Text: "ほほえ"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seg := &model.Segment{Target: tc.target}
			err := restorer.Restore(seg, tc.output, nil)
			// 不应返回错误
			if err != nil {
				t.Fatalf("Restore(%q): unexpected error: %v", tc.target, err)
			}
			// Target 应保持不变
			if seg.Target != tc.target {
				t.Errorf("Restore(%q): target should be unchanged, got %q", tc.target, seg.Target)
			}
		})
	}
}

// T9: 部分匹配 → 仅还原匹配的部分，未匹配的静默跳过。
func TestRubyRestorer_RubyOutput_PartialMatch(t *testing.T) {
	restorer := NewRubyRestorer("ruby_output")

	// 译文中只包含部分基底文本
	seg := &model.Segment{
		Target: "呪を唱える", // 包含 "呪"，不包含 "微笑"
	}
	output := []RubyOutputEntry{
		{Base: "呪", Text: "じゅ"},
		{Base: "微笑", Text: "ほほえ"},
	}

	if err := restorer.Restore(seg, output, nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// 只有 "呪" 应被还原为 ruby，"微笑" 应静默跳过
	want := `<ruby>呪<rt>じゅ</rt></ruby>を唱える`
	if seg.Target != want {
		t.Errorf("partial match:\n  want: %q\n  got:  %q", want, seg.Target)
	}
}

// T9b: 重复基底文本 — 同一基底出现多次时应按顺序逐一还原。
func TestRubyRestorer_RubyOutput_DuplicateBase(t *testing.T) {
	restorer := NewRubyRestorer("ruby_output")

	seg := &model.Segment{
		Target: "呪と呪",
	}
	output := []RubyOutputEntry{
		{Base: "呪", Text: "じゅ"},
		{Base: "呪", Text: "のろ"},
	}

	if err := restorer.Restore(seg, output, nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// 第一个 "呪" → じゅ，第二个 "呪" → のろ
	want := `<ruby>呪<rt>じゅ</rt></ruby>と<ruby>呪<rt>のろ</rt></ruby>`
	if seg.Target != want {
		t.Errorf("duplicate base:\n  want: %q\n  got:  %q", want, seg.Target)
	}
}

// T10: 双源匹配回退 — LLM 的 base 匹配失败时，用原始 annotation 的 base 匹配。
func TestRubyRestorer_RubyOutput_FallbackToOriginalBase(t *testing.T) {
	restorer := NewRubyRestorer("ruby_output")

	cases := []struct {
		name       string
		target     string
		output     []RubyOutputEntry
		originals  []RubyAnnotation
		wantTarget string
	}{
		{
			name:   "LLM base not found, fallback to original base",
			target: "握住白焉的手上加了几分力道",
			output: []RubyOutputEntry{
				{Base: "白色焉", Text: "びゃくえん"}, // LLM 猜错了 base
			},
			originals: []RubyAnnotation{
				{Base: "白焉", Text: "びゃくえん"}, // 原始 base 在译文中存在
			},
			wantTarget: `握住<ruby>白焉<rt>びゃくえん</rt></ruby>的手上加了几分力道`,
		},
		{
			name:   "LLM base matches, no fallback needed",
			target: "握住白焉的手上加了几分力道",
			output: []RubyOutputEntry{
				{Base: "白焉", Text: "びゃくえん"},
			},
			originals: []RubyAnnotation{
				{Base: "白焉", Text: "びゃくえん"},
			},
			wantTarget: `握住<ruby>白焉<rt>びゃくえん</rt></ruby>的手上加了几分力道`,
		},
		{
			name:   "neither LLM base nor original base found",
			target: "挖凿山体形成的巨大平地",
			output: []RubyOutputEntry{
				{Base: "挖", Text: "えぐ"}, // LLM 的 base 不完整
			},
			originals: []RubyAnnotation{
				{Base: "抉", Text: "えぐ"}, // 原始 base 已被翻译，也不在译文中
			},
			// "挖" 存在于 "挖凿" 中，所以 LLM 的 base 实际能匹配到
			wantTarget: `<ruby>挖<rt>えぐ</rt></ruby>凿山体形成的巨大平地`,
		},
		{
			name:   "fallback skipped when original base equals LLM base",
			target: "完全不同のテキスト",
			output: []RubyOutputEntry{
				{Base: "呪", Text: "じゅ"},
			},
			originals: []RubyAnnotation{
				{Base: "呪", Text: "じゅ"}, // 与 LLM base 相同，不应重复尝试
			},
			wantTarget: "完全不同のテキスト", // 两者都不匹配，不变
		},
		{
			name:   "nil originals, no fallback",
			target: "完全不同のテキスト",
			output: []RubyOutputEntry{
				{Base: "呪", Text: "じゅ"},
			},
			originals:  nil,
			wantTarget: "完全不同のテキスト",
		},
		{
			name:   "multiple entries with mixed fallback",
			target: "创造した白焉で唱える",
			output: []RubyOutputEntry{
				{Base: "创", Text: "つく"},      // LLM base 匹配
				{Base: "白色焉", Text: "びゃくえん"}, // LLM base 不匹配，回退
			},
			originals: []RubyAnnotation{
				{Base: "創", Text: "つく"},
				{Base: "白焉", Text: "びゃくえん"},
			},
			wantTarget: `<ruby>创<rt>つく</rt></ruby>造した<ruby>白焉<rt>びゃくえん</rt></ruby>で唱える`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seg := &model.Segment{Target: tc.target}
			if err := restorer.Restore(seg, tc.output, tc.originals); err != nil {
				t.Fatalf("Restore(%q): %v", tc.target, err)
			}
			if seg.Target != tc.wantTarget {
				t.Errorf("Restore(%q):\n  want: %q\n  got:  %q", tc.target, tc.wantTarget, seg.Target)
			}
		})
	}
}

// T11: Kind 字段不影响还原逻辑 — RubyRestorer 不关心 kind，仅用 base/text 匹配。
func TestRubyRestorer_RubyOutput_WithKindField(t *testing.T) {
	restorer := NewRubyRestorer("ruby_output")

	seg := &model.Segment{Target: "呪を唱える"}
	output := []RubyOutputEntry{
		{Base: "呪", Text: "じゅ", Kind: "phonetic"},
	}
	if err := restorer.Restore(seg, output, nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	want := `<ruby>呪<rt>じゅ</rt></ruby>を唱える`
	if seg.Target != want {
		t.Errorf("with kind field:\n  want: %q\n  got:  %q", want, seg.Target)
	}
}
