package segmatch

import (
	"errors"
	"reflect"
	"strings"
	"testing"
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
