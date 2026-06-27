package glossary

import "testing"

func TestIsCJKTarget(t *testing.T) {
	cases := []struct {
		lang string
		want bool
	}{
		{"zh", true},
		{"ja", true},
		{"ko", true},
		{"th", true},
		{"lo", true},
		{"my", true},
		{"km", true},
		{"zh-CN", true},
		{"zh_TW", true},
		{"JA-JP", true}, // 主 tag 大小写无关
		{" zh ", true},  // 容许首尾空白
		{"en", false},
		{"en-US", false},
		{"es", false},
		{"fr", false},
		{"de", false},
		{"ru", false},
		{"", false},
		{"zhh", false}, // 不是 "zh"
	}
	for _, tc := range cases {
		if got := IsCJKTarget(tc.lang); got != tc.want {
			t.Errorf("IsCJKTarget(%q) = %v, want %v", tc.lang, got, tc.want)
		}
	}
}

func TestSafeReplace_CJKDirectReplaceAll(t *testing.T) {
	got, replaced, warn := SafeReplace("使用 A2 时记得释放 A2 资源", "A2", "A1", "zh")
	if got != "使用 A1 时记得释放 A1 资源" {
		t.Errorf("text mismatch: %q", got)
	}
	if !replaced {
		t.Error("replaced should be true")
	}
	if warn != "" {
		t.Errorf("warn should be empty for CJK, got %q", warn)
	}
}

func TestSafeReplace_CJKNoMatch(t *testing.T) {
	got, replaced, warn := SafeReplace("纯中文文本", "A2", "A1", "zh")
	if got != "纯中文文本" {
		t.Errorf("text should be unchanged, got %q", got)
	}
	if replaced {
		t.Error("replaced should be false")
	}
	if warn != "" {
		t.Errorf("warn should be empty, got %q", warn)
	}
}

func TestSafeReplace_LatinIndependentMatch(t *testing.T) {
	got, replaced, warn := SafeReplace("use AI carefully", "AI", "ML", "en")
	if got != "use ML carefully" {
		t.Errorf("text mismatch: %q", got)
	}
	if !replaced {
		t.Error("replaced should be true")
	}
	if warn != "" {
		t.Errorf("warn should be empty, got %q", warn)
	}
}

func TestSafeReplace_LatinSubstringOnly(t *testing.T) {
	// "ai" 出现在 "wait" 和 "rain" 内部，没有独立匹配。
	got, replaced, warn := SafeReplace("wait and rain", "ai", "oo", "en")
	if got != "wait and rain" {
		t.Errorf("text should be unchanged, got %q", got)
	}
	if replaced {
		t.Error("replaced should be false (only substring matches)")
	}
	if warn != "ambiguous-substring-only" {
		t.Errorf("warn want %q, got %q", "ambiguous-substring-only", warn)
	}
}

func TestSafeReplace_LatinMixed(t *testing.T) {
	// "AI" 出现两次：一次独立（句首），一次作为 "AIfoo" 子串（紧邻字母 f）。
	got, replaced, warn := SafeReplace("AI and AIfoo", "AI", "ML", "en")
	if got != "ML and AIfoo" {
		t.Errorf("text mismatch: %q", got)
	}
	if !replaced {
		t.Error("replaced should be true")
	}
	if warn == "" {
		t.Error("warn should report skipped substring")
	}
}

func TestSafeReplace_LatinUnicodeBoundary(t *testing.T) {
	// 词边界判定要走 rune 层；ü 是多 byte，但 "KI" 两侧的空格/字符串端点都不是词内字符。
	got, replaced, warn := SafeReplace("über die KI gestern", "KI", "AI", "de")
	if got != "über die AI gestern" {
		t.Errorf("text mismatch: %q", got)
	}
	if !replaced {
		t.Error("replaced should be true")
	}
	if warn != "" {
		t.Errorf("warn should be empty, got %q", warn)
	}
}

func TestSafeReplace_LatinUnicodeNeighborIsLetter(t *testing.T) {
	// "KI" 紧邻一个 letter rune（ü）应被视作子串。
	got, replaced, warn := SafeReplace("KIüber", "KI", "AI", "de")
	if got != "KIüber" {
		t.Errorf("text should be unchanged (neighbor ü is letter), got %q", got)
	}
	if replaced {
		t.Error("replaced should be false")
	}
	if warn != "ambiguous-substring-only" {
		t.Errorf("warn want %q, got %q", "ambiguous-substring-only", warn)
	}
}

func TestSafeReplace_EmptyOrIdentical(t *testing.T) {
	// from 为空：noop。
	got, replaced, warn := SafeReplace("anything", "", "X", "en")
	if got != "anything" || replaced || warn != "" {
		t.Errorf("empty from should be noop: text=%q replaced=%v warn=%q", got, replaced, warn)
	}
	// from == to：noop。
	got, replaced, warn = SafeReplace("anything AI here", "AI", "AI", "en")
	if got != "anything AI here" || replaced || warn != "" {
		t.Errorf("identical from/to should be noop: text=%q replaced=%v warn=%q", got, replaced, warn)
	}
}

func TestSafeReplace_LatinEdgePunctuation(t *testing.T) {
	// 标点边界：句末 "AI." 应视作独立匹配。
	got, replaced, _ := SafeReplace("future is AI.", "AI", "ML", "en")
	if got != "future is ML." {
		t.Errorf("text mismatch: %q", got)
	}
	if !replaced {
		t.Error("replaced should be true")
	}
}

func TestCaseInsensitiveReplace(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		old      string
		new      string
		want     string
		wantRepl bool
	}{
		{
			name:     "basic replacement",
			s:        "Hello World",
			old:      "hello",
			new:      "Hi",
			want:     "Hi World",
			wantRepl: true,
		},
		{
			name:     "no match",
			s:        "Hello World",
			old:      "xyz",
			new:      "Hi",
			want:     "Hello World",
			wantRepl: false,
		},
		{
			name:     "empty old string",
			s:        "Hello World",
			old:      "",
			new:      "Hi",
			want:     "Hello World",
			wantRepl: false,
		},
		{
			name:     "multiple matches",
			s:        "AI is AI good",
			old:      "ai",
			new:      "AI tech",
			want:     "AI tech is AI tech good",
			wantRepl: true,
		},
		{
			name:     "mixed case match",
			s:        "This is a TeSt",
			old:      "test",
			new:      "example",
			want:     "This is a example",
			wantRepl: true,
		},
		{
			name:     "exact case already matches",
			s:        "Hello World",
			old:      "Hello",
			new:      "Hi",
			want:     "Hi World",
			wantRepl: true,
		},
		{
			name:     "CJK text unaffected by ToLower",
			s:        "人工智能正在改变世界",
			old:      "人工智能",
			new:      "AI",
			want:     "AI正在改变世界",
			wantRepl: true,
		},
		{
			name:     "no case-insensitive match",
			s:        "Hello for it",
			old:      "ai",
			new:      "AI",
			want:     "Hello for it",
			wantRepl: false, // "ai" does not appear as a substring in "Hello for it"
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, replaced := CaseInsensitiveReplace(tt.s, tt.old, tt.new)
			if got != tt.want {
				t.Errorf("CaseInsensitiveReplace(%q, %q, %q) = %q, want %q", tt.s, tt.old, tt.new, got, tt.want)
			}
			if replaced != tt.wantRepl {
				t.Errorf("CaseInsensitiveReplace(%q, %q, %q) replaced = %v, want %v", tt.s, tt.old, tt.new, replaced, tt.wantRepl)
			}
		})
	}
}
