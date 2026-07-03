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
			result, err := restorer.Restore(seg, tc.output, nil)
			if err != nil {
				t.Fatalf("Restore(%q): %v", tc.target, err)
			}
			if seg.Target != tc.wantTarget {
				t.Errorf("Restore(%q):\n  want: %q\n  got:  %q", tc.target, tc.wantTarget, seg.Target)
			}
			if result.Total > 0 && !result.IsFull() {
				t.Errorf("Restore(%q): expected full match, got matched=%d total=%d", tc.target, result.Matched, result.Total)
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
		{
			name:       "marker with kind suffix",
			target:     `⟦ruby:瓦砾/がれき/phonetic⟧上说道`,
			wantTarget: `<ruby>瓦砾<rt>がれき</rt></ruby>上说道`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seg := &model.Segment{Target: tc.target}
			entries := ParseInlineMarkers(tc.target)
			result, err := restorer.Restore(seg, entries, nil)
			if err != nil {
				t.Fatalf("Restore(%q): %v", tc.target, err)
			}
			if seg.Target != tc.wantTarget {
				t.Errorf("Restore(%q):\n  want: %q\n  got:  %q", tc.target, tc.wantTarget, seg.Target)
			}
			if tc.wantTarget != tc.target && !result.IsFull() {
				t.Errorf("Restore(%q): expected full match, got matched=%d total=%d", tc.target, result.Matched, result.Total)
			}
		})
	}
}

// T7b: inline_markers 模式 — preserve_kinds 过滤。
func TestRubyRestorer_InlineMarkers_PreserveKinds(t *testing.T) {
	restorer := NewRubyRestorer("inline_markers")
	keepSet := map[string]bool{"creative": true}

	filterByKinds := func(output []RubyOutputEntry, keep map[string]bool) []RubyOutputEntry {
		var result []RubyOutputEntry
		for _, entry := range output {
			if entry.Kind == "" || keep[entry.Kind] {
				result = append(result, entry)
			}
		}
		return result
	}

	cases := []struct {
		name       string
		target     string
		wantTarget string
	}{
		{
			name:       "phonetic filtered out",
			target:     `⟦ruby:瓦砾/がれき/phonetic⟧上说道`,
			wantTarget: `瓦砾上说道`,
		},
		{
			name:       "creative preserved",
			target:     `⟦ruby:瓦砾/がれき/creative⟧上说道`,
			wantTarget: `<ruby>瓦砾<rt>がれき</rt></ruby>上说道`,
		},
		{
			name:       "mixed kinds",
			target:     `⟦ruby:瓦砾/がれき/phonetic⟧と⟦ruby:微笑/ほほえ/creative⟧む`,
			wantTarget: `瓦砾と<ruby>微笑<rt>ほほえ</rt></ruby>む`,
		},
		{
			name:       "no kind suffix preserved",
			target:     `⟦ruby:瓦砾/がれき⟧上说道`,
			wantTarget: `<ruby>瓦砾<rt>がれき</rt></ruby>上说道`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seg := &model.Segment{Target: tc.target}
			entries := ParseInlineMarkers(tc.target)
			filtered := filterByKinds(entries, keepSet)
			_, err := restorer.Restore(seg, filtered, nil)
			if err != nil {
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
			result, err := restorer.Restore(seg, tc.output, nil)
			// 不应返回错误
			if err != nil {
				t.Fatalf("Restore(%q): unexpected error: %v", tc.target, err)
			}
			// Target 应保持不变
			if seg.Target != tc.target {
				t.Errorf("Restore(%q): target should be unchanged, got %q", tc.target, seg.Target)
			}
			// 应报告 0 匹配
			if result.Matched != 0 {
				t.Errorf("Restore(%q): expected 0 matched, got %d", tc.target, result.Matched)
			}
		})
	}
}

// T9: 部分匹配 → 返回部分匹配结果，调用方可据此决定是否重试。
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

	result, err := restorer.Restore(seg, output, nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// 只有 "呪" 应被还原为 ruby，"微笑" 应静默跳过
	want := `<ruby>呪<rt>じゅ</rt></ruby>を唱える`
	if seg.Target != want {
		t.Errorf("partial match:\n  want: %q\n  got:  %q", want, seg.Target)
	}

	// 应报告部分匹配：2 条中只匹配了 1 条
	if result.Matched != 1 {
		t.Errorf("partial match: expected matched=1, got %d", result.Matched)
	}
	if result.Total != 2 {
		t.Errorf("partial match: expected total=2, got %d", result.Total)
	}
	if result.IsFull() {
		t.Error("partial match: IsFull() should be false")
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

	result, err := restorer.Restore(seg, output, nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// 第一个 "呪" → じゅ，第二个 "呪" → のろ
	want := `<ruby>呪<rt>じゅ</rt></ruby>と<ruby>呪<rt>のろ</rt></ruby>`
	if seg.Target != want {
		t.Errorf("duplicate base:\n  want: %q\n  got:  %q", want, seg.Target)
	}
	if !result.IsFull() {
		t.Errorf("duplicate base: expected full match, got matched=%d total=%d", result.Matched, result.Total)
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
			result, err := restorer.Restore(seg, tc.output, tc.originals)
			if err != nil {
				t.Fatalf("Restore(%q): %v", tc.target, err)
			}
			if seg.Target != tc.wantTarget {
				t.Errorf("Restore(%q):\n  want: %q\n  got:  %q", tc.target, tc.wantTarget, seg.Target)
			}
			_ = result
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
	result, err := restorer.Restore(seg, output, nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	want := `<ruby>呪<rt>じゅ</rt></ruby>を唱える`
	if seg.Target != want {
		t.Errorf("with kind field:\n  want: %q\n  got:  %q", want, seg.Target)
	}
	if !result.IsFull() {
		t.Errorf("with kind field: expected full match, got matched=%d total=%d", result.Matched, result.Total)
	}
}
