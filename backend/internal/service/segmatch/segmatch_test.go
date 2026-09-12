package segmatch

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func boolPtr(value bool) *bool { return &value }

func TestSubstringFind(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		text string
		want []Match
	}{
		{
			name: "basic",
			opts: Options{Find: "cat"},
			text: "a cat",
			want: []Match{{Start: 2, End: 5}},
		},
		{
			name: "multiple non-overlapping matches",
			opts: Options{Find: "ana"},
			text: "bananana",
			want: []Match{{Start: 1, End: 4}, {Start: 5, End: 8}},
		},
		{
			name: "case sensitive",
			opts: Options{Find: "go", CaseSensitive: boolPtr(true)},
			text: "Go go GO",
			want: []Match{{Start: 3, End: 5}},
		},
		{
			name: "case insensitive",
			opts: Options{Find: "äPFEL", CaseSensitive: boolPtr(false)},
			text: "Äpfel äPFEL",
			want: []Match{{Start: 0, End: 6}, {Start: 7, End: 13}},
		},
		{
			name: "whole word mixed Chinese and English",
			opts: Options{Find: "cat", WholeWord: boolPtr(true)},
			text: "猫cat猫 cat，狗",
			want: []Match{{Start: 10, End: 13}},
		},
		{
			name: "whole word treats digits as word characters",
			opts: Options{Find: "cat", WholeWord: boolPtr(true)},
			text: "cat2 2cat cat_ _cat cat",
			want: []Match{{Start: 10, End: 13}, {Start: 16, End: 19}, {Start: 20, End: 23}},
		},
		{
			name: "empty find",
			opts: Options{Find: ""},
			text: "anything",
			want: nil,
		},
		{
			name: "percent and underscore are ordinary characters",
			opts: Options{Find: "%_"},
			text: "a%_b",
			want: []Match{{Start: 1, End: 3}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewMatcher(tt.opts)
			if err != nil {
				t.Fatalf("NewMatcher: %v", err)
			}
			if got := matcher.Find(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Find() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRegexFind(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		text string
		want []Match
	}{
		{
			name: "basic",
			opts: Options{Find: `\d+`, MatchMode: "regex"},
			text: "a12 b3",
			want: []Match{{Start: 1, End: 3}, {Start: 5, End: 6}},
		},
		{
			name: "case insensitive",
			opts: Options{Find: "cat", MatchMode: "regex", CaseSensitive: boolPtr(false)},
			text: "CAT cat",
			want: []Match{{Start: 0, End: 3}, {Start: 4, End: 7}},
		},
		{
			name: "whole word applies to regex",
			opts: Options{Find: "cat", MatchMode: "regex", WholeWord: boolPtr(true)},
			text: "concatenate cat, cat2 cat",
			want: []Match{{Start: 12, End: 15}, {Start: 22, End: 25}},
		},
		{
			name: "whole word regex with unicode boundaries",
			opts: Options{Find: "cat", MatchMode: "regex", WholeWord: boolPtr(true)},
			text: "猫cat猫 cat，狗",
			want: []Match{{Start: 10, End: 13}},
		},
		{
			name: "whole word regex follows standard global matching",
			opts: Options{Find: `b+`, MatchMode: "regex", WholeWord: boolPtr(true)},
			text: "abb aab bb",
			want: []Match{{Start: 8, End: 10}},
		},
		{
			name: "whole word regex ^ anchors to full string",
			opts: Options{Find: `^a*`, MatchMode: "regex", WholeWord: boolPtr(true)},
			text: "x a",
			want: nil,
		},
		{
			name: "whole word regex zero-width ^ anchors to full string",
			opts: Options{Find: `^`, MatchMode: "regex", WholeWord: boolPtr(true)},
			text: "a  b",
			want: nil,
		},
		{
			name: "whole word regex \\A anchors to full string",
			opts: Options{Find: `\Aa*`, MatchMode: "regex", WholeWord: boolPtr(true)},
			text: "x a",
			want: nil,
		},
		{
			name: "whole word regex \\b evaluated on full string",
			opts: Options{Find: `;c+|\bc+`, MatchMode: "regex", WholeWord: boolPtr(true)},
			text: "xc;cc",
			want: nil,
		},
		{
			name: "whole word regex with capture group",
			opts: Options{Find: `(\d+)px`, MatchMode: "regex", WholeWord: boolPtr(true)},
			text: "12px 5pxl",
			want: []Match{{Start: 0, End: 4}},
		},
		{
			name: "regex alternation still matches without whole word",
			opts: Options{Find: "cat|dog", MatchMode: "regex"},
			text: "concat cat dog",
			want: []Match{{Start: 3, End: 6}, {Start: 7, End: 10}, {Start: 11, End: 14}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewMatcher(tt.opts)
			if err != nil {
				t.Fatalf("NewMatcher: %v", err)
			}
			if got := matcher.Find(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Find() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRegexInvalidPattern(t *testing.T) {
	_, err := NewMatcher(Options{Find: "[", MatchMode: "regex"})
	if !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("NewMatcher error = %v, want errors.Is(..., ErrInvalidPattern)", err)
	}
}

// TestNewMatcherPatternLengthLimit 覆盖 MaxPatternRunes 上限：恰好 256 rune 的
// pattern 接受，257 rune 拒绝；错误经 errors.Is 可识别并携带实际/最大上下文。
// 多字节 rune 按 rune 而非字节计数，超长的无效 regex 优先报 too-long，空 pattern
// 不触发上限检查。
func TestNewMatcherPatternLengthLimit(t *testing.T) {
	atLimit := strings.Repeat("a", MaxPatternRunes)
	overLimit := strings.Repeat("a", MaxPatternRunes+1)
	multibyteAtLimit := strings.Repeat("猫", MaxPatternRunes)
	multibyteOverLimit := strings.Repeat("猫", MaxPatternRunes+1)

	for _, mode := range []string{"", "substring", "regex"} {
		for _, find := range []string{atLimit, multibyteAtLimit} {
			if _, err := NewMatcher(Options{Find: find, MatchMode: mode}); err != nil {
				t.Fatalf("NewMatcher(mode=%q, %d runes) = %v, want nil", mode, MaxPatternRunes, err)
			}
		}
		for _, find := range []string{overLimit, multibyteOverLimit} {
			_, err := NewMatcher(Options{Find: find, MatchMode: mode})
			if !errors.Is(err, ErrPatternTooLong) {
				t.Fatalf("NewMatcher(mode=%q, %d runes) error = %v, want errors.Is(..., ErrPatternTooLong)", mode, MaxPatternRunes+1, err)
			}
			msg := err.Error()
			if !strings.Contains(msg, strconv.Itoa(MaxPatternRunes+1)) || !strings.Contains(msg, strconv.Itoa(MaxPatternRunes)) {
				t.Fatalf("error = %q, want actual (%d) and max (%d) context", msg, MaxPatternRunes+1, MaxPatternRunes)
			}
		}
	}

	// 超长的无效 regex：长度检查先于 regexp.Compile，必须报 too-long 而非 invalid。
	if _, err := NewMatcher(Options{Find: strings.Repeat("[", MaxPatternRunes+1), MatchMode: "regex"}); !errors.Is(err, ErrPatternTooLong) {
		t.Fatalf("NewMatcher(overlong invalid regex) error = %v, want errors.Is(..., ErrPatternTooLong)", err)
	}
	// 空 pattern 不触发上限检查。
	for _, mode := range []string{"", "substring", "regex"} {
		if _, err := NewMatcher(Options{Find: "", MatchMode: mode}); err != nil {
			t.Fatalf("NewMatcher(mode=%q, empty) = %v, want nil", mode, err)
		}
	}
}

// TestCaseInsensitiveSubstringShortTextFastFail 覆盖大小写不敏感 substring 的
// O(1) 长度预筛：text 的字节数小于折叠 pattern 的 rune 数时 Find/HasMatch/
// ReplaceAll 立即给出否定结果，且长度阈值不误伤真实命中。
func TestCaseInsensitiveSubstringShortTextFastFail(t *testing.T) {
	find := strings.Repeat("ä", 10) // 10 runes, 20 bytes
	m, err := NewMatcher(Options{Find: find, CaseSensitive: boolPtr(false)})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	texts := []string{"", "a", "ä", strings.Repeat("a", 9)}
	for _, text := range texts {
		if got := m.Find(text); got != nil {
			t.Fatalf("Find(%q) = %#v, want nil", text, got)
		}
		if m.HasMatch(text) {
			t.Fatalf("HasMatch(%q) = true, want false", text)
		}
		if gotText, gotCount := m.ReplaceAll(text, "x"); gotText != text || gotCount != 0 {
			t.Fatalf("ReplaceAll(%q) = (%q, %d), want (%q, 0)", text, gotText, gotCount, text)
		}
	}
	// 恰好达到最小字节数的文本仍按真实语义匹配。
	full := strings.Repeat("ä", 10)
	if !m.HasMatch(full) {
		t.Fatalf("HasMatch(%q) = false, want true", full)
	}
	hasMatchConsistent(t, m, full)
	hasMatchConsistent(t, m, "short")
}

func TestReplaceAll(t *testing.T) {
	tests := []struct {
		name        string
		opts        Options
		text        string
		replaceWith string
		wantText    string
		wantCount   int
	}{
		{
			name:        "substring replacement",
			opts:        Options{Find: "cat"},
			text:        "cat and cat",
			replaceWith: "dog",
			wantText:    "dog and dog",
			wantCount:   2,
		},
		{
			name:        "empty replacement deletes matches",
			opts:        Options{Find: "-"},
			text:        "a-b-c",
			replaceWith: "",
			wantText:    "abc",
			wantCount:   2,
		},
		{
			name:        "regex capture replacement",
			opts:        Options{Find: `(\w+)-(\w+)`, MatchMode: "regex"},
			text:        "first-last second-item",
			replaceWith: `${2},$1`,
			wantText:    "last,first item,second",
			wantCount:   2,
		},
		{
			name:        "empty substring has no replacements",
			opts:        Options{Find: ""},
			text:        "abc",
			replaceWith: "x",
			wantText:    "abc",
			wantCount:   0,
		},
		{
			name:        "regex whole word filters replacements",
			opts:        Options{Find: "cat", MatchMode: "regex", WholeWord: boolPtr(true)},
			text:        "concatenate cat, cat2 cat",
			replaceWith: "dog",
			wantText:    "concatenate dog, cat2 dog",
			wantCount:   2,
		},
		{
			name:        "regex whole word keeps capture groups",
			opts:        Options{Find: `(\d+)px`, MatchMode: "regex", WholeWord: boolPtr(true)},
			text:        "12px 5pxl",
			replaceWith: "${1}em",
			wantText:    "12em 5pxl",
			wantCount:   1,
		},
		{
			name:        "regex named capture groups",
			opts:        Options{Find: `(?P<num>\d+)-(?P<unit>\w+)`, MatchMode: "regex"},
			text:        "12-px 8-em",
			replaceWith: "${unit}${num}",
			wantText:    "px12 em8",
			wantCount:   2,
		},
		{
			name:        "regex zero-width matches",
			opts:        Options{Find: `x*`, MatchMode: "regex"},
			text:        "ab",
			replaceWith: "-",
			wantText:    "-a-b-",
			wantCount:   3,
		},
		{
			name:        "regex zero-width matches with capture group",
			opts:        Options{Find: `(\d)*`, MatchMode: "regex"},
			text:        "a1b",
			replaceWith: "<$1>",
			wantText:    "<>a<1>b<>",
			wantCount:   3,
		},
		{
			name:        "regex non-participating group is dropped",
			opts:        Options{Find: `foo|(bar)`, MatchMode: "regex"},
			text:        "foo bar",
			replaceWith: "[$1]",
			wantText:    "[] [bar]",
			wantCount:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewMatcher(tt.opts)
			if err != nil {
				t.Fatalf("NewMatcher: %v", err)
			}
			gotText, gotCount := matcher.ReplaceAll(tt.text, tt.replaceWith)
			if gotText != tt.wantText || gotCount != tt.wantCount {
				t.Fatalf("ReplaceAll() = (%q, %d), want (%q, %d)", gotText, gotCount, tt.wantText, tt.wantCount)
			}
		})
	}
}

func TestZeroWidthWholeWordFind(t *testing.T) {
	// 零宽匹配的 whole-word 语义：仅在两侧都非词字符处被接受。
	matcher, err := NewMatcher(Options{Find: `\d*`, MatchMode: "regex", WholeWord: boolPtr(true)})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	// 位置 0 与末尾两侧均无词字符，零宽匹配被接受且不死循环。
	if got, want := matcher.Find(" a "), []Match{{Start: 0, End: 0}, {Start: 3, End: 3}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Find() = %#v, want %#v", got, want)
	}
	if gotText, gotCount := matcher.ReplaceAll(" a ", "#"); gotText != "# a #" || gotCount != 2 {
		t.Fatalf("ReplaceAll() = (%q, %d), want (%q, %d)", gotText, gotCount, "# a #", 2)
	}
}

func TestCandidateLiteral(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "case sensitive substring returns full find",
			opts: Options{Find: "CaT"},
			want: "CaT",
		},
		{
			name: "case insensitive substring returns empty",
			opts: Options{Find: "CaT", CaseSensitive: boolPtr(false)},
			want: "",
		},
		{
			name: "literal regex returns full pattern",
			opts: Options{Find: "CaT", MatchMode: "regex"},
			want: "CaT",
		},
		{
			name: "regex returns literal prefix",
			opts: Options{Find: `cats?`, MatchMode: "regex"},
			want: "cat",
		},
		{
			name: "regex with non-literal start returns empty",
			opts: Options{Find: `\d+cat`, MatchMode: "regex"},
			want: "",
		},
		{
			name: "case insensitive regex returns empty",
			opts: Options{Find: "CaT", MatchMode: "regex", CaseSensitive: boolPtr(false)},
			want: "",
		},
		{
			name: "empty find returns empty",
			opts: Options{Find: ""},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewMatcher(tt.opts)
			if err != nil {
				t.Fatalf("NewMatcher: %v", err)
			}
			if got := matcher.CandidateLiteral(); got != tt.want {
				t.Fatalf("CandidateLiteral() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewMatcherDefaultsAndUnknownMode(t *testing.T) {
	matcher, err := NewMatcher(Options{Find: "x"})
	if err != nil {
		t.Fatalf("NewMatcher default mode: %v", err)
	}
	if got := matcher.Find("x"); !reflect.DeepEqual(got, []Match{{Start: 0, End: 1}}) {
		t.Fatalf("default mode Find() = %#v", got)
	}

	if _, err := NewMatcher(Options{Find: "x", MatchMode: "glob"}); !errors.Is(err, ErrUnsupportedMatchMode) {
		t.Fatalf("unknown match mode error = %v, want errors.Is(..., ErrUnsupportedMatchMode)", err)
	}
}

// hasMatchConsistent 断言 HasMatch 与 len(Find) > 0 完全一致。
func hasMatchConsistent(t *testing.T, m Matcher, text string) {
	t.Helper()
	want := len(m.Find(text)) > 0
	if got := m.HasMatch(text); got != want {
		t.Fatalf("HasMatch(%q) = %v, inconsistent with Find (%d matches)", text, got, len(m.Find(text)))
	}
}

// TestHasMatchConsistentWithFind 穷举组合语料（substring/regex × 大小写 × 整词 ×
// 锚点/零宽/Unicode/交替分支），断言 HasMatch 与 Find 的布尔结果逐例一致。
func TestHasMatchConsistentWithFind(t *testing.T) {
	texts := []string{
		"",
		"a",
		"ab",
		"aab",
		"aaa",
		"ab ab",
		"x a",
		" a ",
		"  ",
		"a ",
		" a",
		"a1 b2 c3 d4 e5 f6",
		"concatenate cat, cat2 cat",
		"猫cat猫 cat，狗",
		"12px 5pxl",
		"CAT cat",
		"Äpfel äPFEL",
		"foo bar",
		"xc;cc",
		"abb aab bb",
		"\xff bad",
	}
	substringPatterns := []string{"cat", "ana", "go", "äPFEL", "%_", "a", "a b", ""}
	regexPatterns := []string{
		"cat", `\d`, `\d+`, `\d*`, `x*`, `^a`, `\Aa`, `a$`, `\ba`, `a\b`,
		`a|ab`, `ab|a`, `.a`, `a.a`, `aa`, `c.t`, `猫|dog`, `(\d+)px`,
		`foo|(bar)`, `;c+|\bc+`, `[ab]+`, `^`, `$`, `Äpfel|äpfel`, `\bcat\b`,
	}

	for _, wholeWord := range []bool{false, true} {
		for _, caseSensitive := range []bool{false, true} {
			ww := wholeWord
			cs := caseSensitive
			for _, p := range substringPatterns {
				m, err := NewMatcher(Options{Find: p, CaseSensitive: &cs, WholeWord: &ww})
				if err != nil {
					t.Fatalf("NewMatcher(substring %q): %v", p, err)
				}
				for _, text := range texts {
					hasMatchConsistent(t, m, text)
				}
			}
			for _, p := range regexPatterns {
				m, err := NewMatcher(Options{Find: p, MatchMode: "regex", CaseSensitive: &cs, WholeWord: &ww})
				if err != nil {
					t.Fatalf("NewMatcher(regex %q): %v", p, err)
				}
				for _, text := range texts {
					hasMatchConsistent(t, m, text)
				}
			}
		}
	}
}

// TestHasMatchZeroWidth 零宽 pattern 的 HasMatch 语义：与 Find（含整词过滤）一致，
// 且不因零宽高频匹配而死循环或误报。
func TestHasMatchZeroWidth(t *testing.T) {
	m, err := NewMatcher(Options{Find: `\d*`, MatchMode: "regex", WholeWord: boolPtr(true)})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	// Find 在 " a " 有 2 个通过整词过滤的零宽命中，HasMatch 必须一致为真。
	if got := m.Find(" a "); len(got) != 2 {
		t.Fatalf("Find() = %#v, want 2 zero-width matches", got)
	}
	if !m.HasMatch(" a ") {
		t.Fatal("HasMatch(\" a \") = false, want true")
	}
	// 原始零宽命中全部被整词过滤时必须一致为假。
	if got := m.Find("ab"); got != nil {
		t.Fatalf("Find(\"ab\") = %#v, want nil", got)
	}
	if m.HasMatch("ab") {
		t.Fatal("HasMatch(\"ab\") = true, want false")
	}
	// 非整词零宽：任意文本都存在零宽命中。
	m2, err := NewMatcher(Options{Find: `x*`, MatchMode: "regex"})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	if !m2.HasMatch("abc") || len(m2.Find("abc")) == 0 {
		t.Fatal("non-wholeword zero-width pattern must match everywhere")
	}
}

// TestHasMatchAvoidsMatchAllocation 列表布尔判定不得为否定结果构建全部匹配切片：
// 整词 regex 有大量原始命中但无一通过整词过滤，Find 必须为每个匹配分配切片
// （分配数随命中数线性增长），HasMatch 经边界预筛早停，分配数是与命中数无关的
// 小常数；非整词 regex 的 MatchString 与 substring 早停路径同理。
func TestHasMatchAvoidsMatchAllocation(t *testing.T) {
	m, err := NewMatcher(Options{Find: `\d`, MatchMode: "regex", WholeWord: boolPtr(true)})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	// 300 个原始命中（字母夹数字），全部因整词过滤被拒。
	var text strings.Builder
	for i := 0; i < 300; i++ {
		text.WriteString("a1 ")
	}
	bigText := text.String()
	if got := m.Find(bigText); got != nil {
		t.Fatalf("Find() = %d matches, want nil（原始命中全部被整词过滤）", len(got))
	}
	if m.HasMatch(bigText) {
		t.Fatal("HasMatch() = true, want false")
	}
	// regexp 执行器的 machine 复用经 sync.Pool，race/GC 下偶发一次分配，
	// 故布尔路径只断言与命中数无关的小常数；对照 Find 随命中数线性分配。
	hasMatchAllocs := testing.AllocsPerRun(50, func() { m.HasMatch(bigText) })
	if hasMatchAllocs > 2 {
		t.Fatalf("HasMatch allocs = %v, want <= 2（不得随命中数增长）", hasMatchAllocs)
	}
	findAllocs := testing.AllocsPerRun(50, func() { _ = m.Find(bigText) })
	if findAllocs < 100 {
		t.Fatalf("Find allocs = %v, want > 100（对照：300 个匹配逐个分配）", findAllocs)
	}

	// 非整词 regex：MatchString 布尔判定同理不随命中数分配。
	m2, err := NewMatcher(Options{Find: `\d+`, MatchMode: "regex"})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	if !m2.HasMatch(bigText) {
		t.Fatal("HasMatch() = false, want true")
	}
	if allocs := testing.AllocsPerRun(50, func() { m2.HasMatch(bigText) }); allocs > 2 {
		t.Fatalf("HasMatch allocs = %v, want <= 2", allocs)
	}

	// substring：首个命中即返回，不构建切片；strings.Index 路径确定性零分配。
	m3, err := NewMatcher(Options{Find: "cat", WholeWord: boolPtr(true)})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	if !m3.HasMatch("a cat") {
		t.Fatal("HasMatch() = false, want true")
	}
	if allocs := testing.AllocsPerRun(50, func() { m3.HasMatch("dog dog dog") }); allocs > 0 {
		t.Fatalf("substring HasMatch (no match) allocs = %v, want 0", allocs)
	}
}

// TestRegexWholeWordNlNoBoundaryConsistency 覆盖 \p{Nd} 边界修复：Nl/No 类别
// 的字符（如 Ⅻ、½）不是 isWordRune 意义上的词字符，紧邻它们的匹配必须被接受。
// boundary 预筛只有同样使用 \p{Nd} 才能与 Find 结论一致，否则整词 regex 的
// HasMatch 会在实际有命中时误报为否。
func TestRegexWholeWordNlNoBoundaryConsistency(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []Match
	}{
		{name: "Nl precedes match", text: "Ⅻcat", want: []Match{{Start: 3, End: 6}}},
		{name: "No precedes match", text: "½cat", want: []Match{{Start: 2, End: 5}}},
		{name: "Nl follows match", text: "catⅫ", want: []Match{{Start: 0, End: 3}}},
		{name: "No follows match", text: "cat½", want: []Match{{Start: 0, End: 3}}},
		{name: "Nd still rejects as word rune", text: "1cat", want: nil},
		{name: "letter still rejects as word rune", text: "acat", want: nil},
	}
	for _, caseSensitive := range []bool{false, true} {
		cs := caseSensitive
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				m, err := NewMatcher(Options{Find: "cat", MatchMode: "regex", CaseSensitive: &cs, WholeWord: boolPtr(true)})
				if err != nil {
					t.Fatalf("NewMatcher: %v", err)
				}
				if got := m.Find(tt.text); !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("Find(%q) = %#v, want %#v", tt.text, got, tt.want)
				}
				hasMatchConsistent(t, m, tt.text)
			})
		}
	}
}

// referenceAdvanceRunes 按 rune 数前进，是旧大小写不敏感匹配所用窗口扫描的
// 参考实现，独立于 KMP。
func referenceAdvanceRunes(text string, start, count int) (int, bool) {
	end := start
	for i := 0; i < count; i++ {
		if end >= len(text) {
			return 0, false
		}
		_, size := utf8.DecodeRuneInString(text[end:])
		end += size
	}
	return end, true
}

// referenceCaseInsensitiveFind 以 strings.EqualFold 的 rune 窗口扫描为语义基准。
func referenceCaseInsensitiveFind(find, text string, wholeWord bool) []Match {
	findRuneCount := utf8.RuneCountInString(find)
	var matches []Match
	for start := 0; start < len(text); {
		end, ok := referenceAdvanceRunes(text, start, findRuneCount)
		if ok && strings.EqualFold(text[start:end], find) &&
			(!wholeWord || isWholeWord(text, start, end)) {
			matches = append(matches, Match{Start: start, End: end})
			start = end
			continue
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		start += size
	}
	return matches
}

// referenceCaseSensitiveFind 以 strings.Index 的字节语义为基准。
func referenceCaseSensitiveFind(find, text string, wholeWord bool) []Match {
	var matches []Match
	for searchStart := 0; searchStart <= len(text); {
		relativeStart := strings.Index(text[searchStart:], find)
		if relativeStart < 0 {
			return matches
		}
		start := searchStart + relativeStart
		end := start + len(find)
		if !wholeWord || isWholeWord(text, start, end) {
			matches = append(matches, Match{Start: start, End: end})
			searchStart = end
		} else {
			searchStart = start + 1
		}
	}
	return matches
}

// TestSubstringDifferentialAgainstReference 用独立参考实现差分验证 substring
// 匹配：大小写不敏感路径对照 strings.EqualFold 窗口扫描，大小写敏感路径对照
// strings.Index。语料覆盖 ſ/ſ、Kelvin 符号、ß/ẞ/SS、希腊终结 sigma、非法
// UTF-8 与空串，并断言 HasMatch 与 Find 的布尔结果一致。
func TestSubstringDifferentialAgainstReference(t *testing.T) {
	texts := []string{
		"",
		"a",
		"ab",
		"aab",
		"aaa",
		"abc abc",
		"test TEST TeSt",
		"ſpecial special SPECIAL",
		"kelvin \u212A K k",
		"ß",
		"SS",
		"ẞ",
		"Straße STRASSE STRAẞE",
		"ΣΟΦΟΣ σοφος ςοφοσ",
		"Äpfel äPFEL",
		"猫cat猫 cat，狗",
		"\xffcat",
		"cat\xfe",
		"c\xffat",
		"a\xffb",
		"\uFFFDcat cat\uFFFD",
		"\xff\u212A\u017F",
		"x\xc3",
		"\xc3\x28",
		"xa-a-a",
		"a-a-a",
		"-a-a-",
		"xa-a-a xa-a-a",
		strings.Repeat("a", 64) + "b",
	}
	patterns := []string{
		"s", "S", "ſ", "k", "K", "\u212A", "ß", "ẞ",
		"ss", "strasse", "straße", "σοφος", "σοφοσ", "ΣΟΦΟΣ",
		"cat", "äpfel", "ÄPFEL", "a", "ab", "ana", "a-a", "-a-",
		"\xff", "\uFFFD",
		"", // 空串由 Find/HasMatch 单独短路
	}
	for _, wholeWord := range []bool{false, true} {
		for _, caseSensitive := range []bool{false, true} {
			ww, cs := wholeWord, caseSensitive
			for _, p := range patterns {
				m, err := NewMatcher(Options{Find: p, CaseSensitive: &cs, WholeWord: &ww})
				if err != nil {
					t.Fatalf("NewMatcher(%q): %v", p, err)
				}
				for _, text := range texts {
					got := m.Find(text)
					var want []Match
					switch {
					case p == "":
						want = nil
					case cs:
						want = referenceCaseSensitiveFind(p, text, ww)
					default:
						want = referenceCaseInsensitiveFind(p, text, ww)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("Find(text=%q) pattern=%q cs=%v ww=%v = %#v, want %#v",
							text, p, cs, ww, got, want)
					}
					if hm := m.HasMatch(text); hm != (len(want) > 0) {
						t.Fatalf("HasMatch(text=%q) pattern=%q cs=%v ww=%v = %v, want %v",
							text, p, cs, ww, hm, len(want) > 0)
					}
				}
			}
		}
	}
}

// TestCaseInsensitiveSubstringWholeWordRejectedOverlap 锁定整词拒绝后的重叠
// 候选语义：pattern "a-a" 在 "xa-a-a" 上首个候选 [1,4) 因左侧词字符 'x' 被拒，
// 起点前进一个 rune 后 [3,6) 左右边界均合格，必须命中。
func TestCaseInsensitiveSubstringWholeWordRejectedOverlap(t *testing.T) {
	m, err := NewMatcher(Options{Find: "a-a", CaseSensitive: boolPtr(false), WholeWord: boolPtr(true)})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	want := []Match{{Start: 3, End: 6}}
	if got := m.Find("xa-a-a"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Find(\"xa-a-a\") = %#v, want %#v", got, want)
	}
	if want := referenceCaseInsensitiveFind("a-a", "xa-a-a", true); !reflect.DeepEqual(m.Find("xa-a-a"), want) {
		t.Fatalf("Find(\"xa-a-a\") disagrees with reference: got %#v, want %#v", m.Find("xa-a-a"), want)
	}
	hasMatchConsistent(t, m, "xa-a-a")
}

// TestCaseInsensitiveSubstringSimpleFold 固定 SimpleFold 关键等价关系：
// ſ↔s/S、Kelvin 符号↔k/K、ß↔ẞ，以及 ß 与 SS 不相等（simple folding 不展开）。
func TestCaseInsensitiveSubstringSimpleFold(t *testing.T) {
	tests := []struct {
		name string
		find string
		text string
		want []Match
	}{
		{name: "long s matches s", find: "ſ", text: "s", want: []Match{{Start: 0, End: 1}}},
		{name: "s matches long s", find: "s", text: "ſ", want: []Match{{Start: 0, End: 2}}},
		{name: "long s matches S", find: "ſ", text: "S", want: []Match{{Start: 0, End: 1}}},
		{name: "kelvin sign matches k", find: "\u212A", text: "k", want: []Match{{Start: 0, End: 1}}},
		{name: "k matches kelvin sign", find: "k", text: "\u212A", want: []Match{{Start: 0, End: 3}}},
		{name: "kelvin sign matches K", find: "\u212A", text: "K", want: []Match{{Start: 0, End: 1}}},
		{name: "sharp s matches capital sharp s", find: "ß", text: "ẞ", want: []Match{{Start: 0, End: 3}}},
		{name: "capital sharp s matches sharp s", find: "ẞ", text: "ß", want: []Match{{Start: 0, End: 2}}},
		{name: "sharp s does not match SS", find: "ß", text: "SS", want: nil},
		{name: "SS does not match sharp s", find: "SS", text: "ß", want: nil},
		{name: "strasse does not match straße", find: "strasse", text: "Straße", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := NewMatcher(Options{Find: tt.find, CaseSensitive: boolPtr(false)})
			if err != nil {
				t.Fatalf("NewMatcher: %v", err)
			}
			if got := m.Find(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Find() = %#v, want %#v", got, tt.want)
			}
			hasMatchConsistent(t, m, tt.text)
		})
	}
}

// TestCaseInsensitiveSubstringInvalidUTF8 非法字节按 RuneError 处理：与
// strings.EqualFold 一致，单个非法字节与 U+FFFD 等价，字节偏移正确，不 panic。
func TestCaseInsensitiveSubstringInvalidUTF8(t *testing.T) {
	invalidByte, err := NewMatcher(Options{Find: "\xff", CaseSensitive: boolPtr(false)})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	if got := invalidByte.Find("\uFFFD"); !reflect.DeepEqual(got, []Match{{Start: 0, End: 3}}) {
		t.Fatalf("invalid byte pattern Find(\"\\uFFFD\") = %#v, want [{0 3}]", got)
	}

	replacement, err := NewMatcher(Options{Find: "\uFFFD", CaseSensitive: boolPtr(false)})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	if got := replacement.Find("\xff"); !reflect.DeepEqual(got, []Match{{Start: 0, End: 1}}) {
		t.Fatalf("U+FFFD pattern Find(\"\\xff\") = %#v, want [{0 1}]", got)
	}
	// 两个连续非法字节是两个 RuneError，各自与单个 U+FFFD 等价，故有两个命中。
	if got := replacement.Find("\xff\xff"); !reflect.DeepEqual(got, []Match{{Start: 0, End: 1}, {Start: 1, End: 2}}) {
		t.Fatalf("U+FFFD pattern Find(\"\\xff\\xff\") = %#v, want [{0 1} {1 2}]", got)
	}
	hasMatchConsistent(t, invalidByte, "\xff\uFFFD\xff")
	hasMatchConsistent(t, replacement, "\xff\uFFFD\xff")
}

// TestCaseInsensitiveSubstringHasMatchAllocation 大小写不敏感 substring 的
// HasMatch 也不得为否定结果构建全部匹配：无部分命中时不分配，大量命中时
// 首个命中即停、分配是与命中数无关的小常数；Find 仍随命中数线性分配。
func TestCaseInsensitiveSubstringHasMatchAllocation(t *testing.T) {
	m, err := NewMatcher(Options{Find: "cat", CaseSensitive: boolPtr(false)})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	noMatch := strings.Repeat("dog ", 300)
	if m.HasMatch(noMatch) {
		t.Fatal("HasMatch() = true, want false")
	}
	if allocs := testing.AllocsPerRun(50, func() { m.HasMatch(noMatch) }); allocs > 0 {
		t.Fatalf("case-insensitive HasMatch (no partial match) allocs = %v, want 0", allocs)
	}

	manyMatches := strings.Repeat("CAT cat ", 300)
	if !m.HasMatch(manyMatches) {
		t.Fatal("HasMatch() = false, want true")
	}
	hasMatchAllocs := testing.AllocsPerRun(50, func() { m.HasMatch(manyMatches) })
	if hasMatchAllocs > 2 {
		t.Fatalf("case-insensitive HasMatch (many matches) allocs = %v, want <= 2（不得随命中数增长）", hasMatchAllocs)
	}
	// Find 收集全部命中，切片按几何增长，分配数明显高于首个命中即停的 HasMatch。
	findAllocs := testing.AllocsPerRun(50, func() { _ = m.Find(manyMatches) })
	if findAllocs <= hasMatchAllocs {
		t.Fatalf("case-insensitive Find allocs = %v, want > HasMatch allocs = %v", findAllocs, hasMatchAllocs)
	}
}

// BenchmarkSubstringCaseInsensitiveFind 测量大小写不敏感 Find 的吞吐，
// 谓词覆盖 SimpleFold 等价（ſ/ſPECIAL）与非 ASCII 折叠。
func BenchmarkSubstringCaseInsensitiveFind(b *testing.B) {
	m, err := NewMatcher(Options{Find: "ſPECIAL", CaseSensitive: boolPtr(false)})
	if err != nil {
		b.Fatalf("NewMatcher: %v", err)
	}
	text := strings.Repeat("The ſPECIAL case SPECIAL special ", 200)
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(m.Find(text)) == 0 {
			b.Fatal("Find() = 0 matches, want > 0")
		}
	}
}

// BenchmarkSubstringCaseInsensitiveHasMatch 测量大小写不敏感 HasMatch 的
// 早停路径（无命中需扫完全串，不构建匹配切片）。
func BenchmarkSubstringCaseInsensitiveHasMatch(b *testing.B) {
	m, err := NewMatcher(Options{Find: "ſPECIAL", CaseSensitive: boolPtr(false)})
	if err != nil {
		b.Fatalf("NewMatcher: %v", err)
	}
	text := strings.Repeat("The ordinary case and ordinary matters ", 200)
	if m.HasMatch(text) {
		b.Fatal("HasMatch() = true, want false")
	}
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if m.HasMatch(text) {
			b.Fatal("HasMatch() = true, want false")
		}
	}
}
