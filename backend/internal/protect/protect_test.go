package protect

import (
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// roundTrip：protect 后再把含占位符的字符串直接搬到 target，unprotect 应得回原文。
func roundTrip(t *testing.T, p Protector, original string) {
	t.Helper()
	seg := &pipeline.Segment{Source: original}
	if err := p.Protect(seg); err != nil {
		t.Fatalf("protect: %v", err)
	}
	seg.Target = seg.Source
	if err := p.Unprotect(seg); err != nil {
		t.Fatalf("unprotect: %v", err)
	}
	if seg.Target != original {
		t.Fatalf("round-trip mismatch\nwant: %q\ngot:  %q", original, seg.Target)
	}
}

func TestCodeProtector_RoundTrip(t *testing.T) {
	cases := []string{
		"hello `world` foo",
		"plain text without code",
		"```go\nfmt.Println(\"hi\")\n```",
		"mix `inline` and:\n```\nblock\n```\nafter",
	}
	for _, c := range cases {
		roundTrip(t, &CodeProtector{}, c)
	}
}

func TestLinkProtector_RoundTrip(t *testing.T) {
	cases := []string{
		"see [docs](https://example.com) please",
		"image ![alt](https://e.com/a.png) here",
		"auto <https://example.com> link",
		"ref [foo][1] link",
		"no link here",
	}
	for _, c := range cases {
		roundTrip(t, &LinkProtector{}, c)
	}
}

func TestLinkProtector_PreservesVisibleText(t *testing.T) {
	seg := &pipeline.Segment{Source: "see [docs](https://example.com)"}
	if err := (&LinkProtector{}).Protect(seg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seg.Source, "[docs]") {
		t.Errorf("visible text lost: %q", seg.Source)
	}
	if strings.Contains(seg.Source, "https://") {
		t.Errorf("URL not protected: %q", seg.Source)
	}
}

func TestPlaceholderProtector_RoundTrip(t *testing.T) {
	cases := []string{
		"hello {{ .Name }}!",
		"value is {0} and {1}",
		"format %s and %d",
		"env $HOME and ${PATH}",
		"plain text",
	}
	for _, c := range cases {
		roundTrip(t, &PlaceholderProtector{}, c)
	}
}

func TestXMLProtector_RoundTrip(t *testing.T) {
	cases := []string{
		`click <a href="x">here</a>`,
		`<br/> ends line`,
		`<custom-tag attr="v"/>middle</custom-tag>`,
		"no tags at all",
	}
	for _, c := range cases {
		roundTrip(t, &XMLProtector{}, c)
	}
}

func TestCompose_RoundTrip(t *testing.T) {
	p := Compose(&CodeProtector{}, &LinkProtector{}, &PlaceholderProtector{}, &XMLProtector{})
	source := "Use `code` and [link](https://x.com) with {{var}} <br/>"
	roundTrip(t, p, source)
}

func TestFromRules(t *testing.T) {
	p := FromRules([]string{"code", "link", "unknown", "placeholder", "xml"})
	source := "`x` [y](z) {{w}} <a/>"
	roundTrip(t, p, source)
}

// S1: 占位符之间存在前缀关系时，restoreAll 应按 key 长度倒序，
// 不会先替短 key 而吞掉长 key。
func TestRestoreAll_OrdersByLengthDesc(t *testing.T) {
	seg := &pipeline.Segment{
		Protected: map[string]string{
			"__LF_000001__":  "SHORT",
			"__LF_000001__X": "LONG",
		},
		Target: "a __LF_000001__X b __LF_000001__ c",
	}
	seg.Target = restoreAll(seg.Target, seg.Protected)
	want := "a LONG b SHORT c"
	if seg.Target != want {
		t.Fatalf("want %q, got %q", want, seg.Target)
	}
}

// S2: 当回填后的原文里 *恰好* 含有另一个占位符字面时，
// composed.Unprotect 只回填一次，不会把字面再次替换。
func TestCompose_NoDoubleRestore(t *testing.T) {
	seg := &pipeline.Segment{
		Protected: map[string]string{
			"__LF_000001__": "the literal __LF_000002__ should remain",
			"__LF_000002__": "WRONG",
		},
		Target: "head __LF_000001__ tail",
	}
	c := Compose(&CodeProtector{}, &LinkProtector{})
	if err := c.Unprotect(seg); err != nil {
		t.Fatal(err)
	}
	want := "head the literal __LF_000002__ should remain tail"
	if seg.Target != want {
		t.Fatalf("double-restore detected\nwant: %q\ngot:  %q", want, seg.Target)
	}
}

// S5: 完整性校验在 target 中缺失占位符时应报告。
func TestMissingPlaceholders(t *testing.T) {
	seg := &pipeline.Segment{
		Protected: map[string]string{
			"__LF_000001__": "A",
			"__LF_000002__": "B",
			"__LF_000003__": "C",
		},
		Target: "kept __LF_000002__ only",
	}
	got := MissingPlaceholders(seg)
	want := []string{"__LF_000001__", "__LF_000003__"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

// S6: mergeAdjacentPlaceholders 应将相邻占位符合并为单个占位符。
func TestMergeAdjacentPlaceholders(t *testing.T) {
	cases := []struct {
		name      string
		source    string
		protected map[string]string
		wantSrc   string
		wantKeys  []string // 合并后应存在的 key
	}{
		{
			name:   "two adjacent placeholders",
			source: "__LF_000001____LF_000002__after",
			protected: map[string]string{
				"__LF_000001__": "<ruby>",
				"__LF_000002__": "<rt>",
			},
			wantSrc:  "__LF_000003__after",
			wantKeys: []string{"__LF_000003__"},
		},
		{
			name:   "two groups of adjacent placeholders",
			source: "__LF_000001____LF_000002__じゅ__LF_000003____LF_000004__",
			protected: map[string]string{
				"__LF_000001__": "<ruby>",
				"__LF_000002__": "<rt>",
				"__LF_000003__": "</rt>",
				"__LF_000004__": "</ruby>",
			},
			wantSrc:  "__LF_000005__じゅ__LF_000006__",
			wantKeys: []string{"__LF_000005__", "__LF_000006__"},
		},
		{
			name:   "non-adjacent placeholders not merged",
			source: "__LF_000001__ text __LF_000002__",
			protected: map[string]string{
				"__LF_000001__": "A",
				"__LF_000002__": "B",
			},
			wantSrc:  "__LF_000001__ text __LF_000002__",
			wantKeys: []string{"__LF_000001__", "__LF_000002__"},
		},
		{
			name:   "single placeholder unchanged",
			source: "text __LF_000001__ more",
			protected: map[string]string{
				"__LF_000001__": "A",
			},
			wantSrc:  "text __LF_000001__ more",
			wantKeys: []string{"__LF_000001__"},
		},
		{
			name:   "no placeholders",
			source: "plain text",
			protected: map[string]string{
				"__LF_000001__": "A",
			},
			wantSrc:  "plain text",
			wantKeys: []string{"__LF_000001__"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seg := &pipeline.Segment{
				Source:    tc.source,
				Protected: make(map[string]string),
			}
			for k, v := range tc.protected {
				seg.Protected[k] = v
			}
			mergeAdjacentPlaceholders(seg)
			if seg.Source != tc.wantSrc {
				t.Errorf("source: got %q, want %q", seg.Source, tc.wantSrc)
			}
			for _, k := range tc.wantKeys {
				if _, ok := seg.Protected[k]; !ok {
					t.Errorf("expected key %q not found in Protected map", k)
				}
			}
		})
	}
}

// S7: composed Protect+Unprotect 对含相邻占位符的 HTML 应正确 round-trip。
func TestCompose_AdjacentPlaceholders_RoundTrip(t *testing.T) {
	// 模拟 epub 中 </span><ruby> 的场景
	source := `────</span><ruby>椎名<rt>しいな</rt></ruby>`
	p := Compose(&XMLProtector{})
	roundTrip(t, p, source)
}

// S8: 含相邻占位符的 unprotect 应正确还原（中间有文本内容）。
func TestCompose_Unprotect_AdjacentPlaceholders(t *testing.T) {
	seg := &pipeline.Segment{
		Protected: map[string]string{
			"__LF_000001__": `<span class="x">`,
			"__LF_000002__": "</span>",
			"__LF_000003__": "<ruby>",
			"__LF_000004__": "<rt>",
			"__LF_000005__": "</rt>",
			"__LF_000006__": "</ruby>",
		},
		// 相邻占位符：__LF_000002____LF_000003__ 和 __LF_000005____LF_000006__
		Target: "────__LF_000002____LF_000003__椎名__LF_000004__しいな__LF_000005____LF_000006__",
	}
	c := Compose(&XMLProtector{})
	if err := c.Unprotect(seg); err != nil {
		t.Fatal(err)
	}
	want := `────</span><ruby>椎名<rt>しいな</rt></ruby>`
	if seg.Target != want {
		t.Fatalf("unprotect mismatch\nwant: %q\ngot:  %q", want, seg.Target)
	}
}

// S9: RubyProtector Protect 应剥离 ruby 标签并存储注音到 Meta。
// 注音还原委托给 RubyRestorer（RubyRestore stage），而非 Unprotect。
func TestRubyProtector_ProtectAndRestore(t *testing.T) {
	restorer := NewRubyRestorer("ruby_output")

	cases := []struct {
		input    string
		base     string // protect 后 Source 应仅保留基底文本
		restored string // 还原后期望的 Target（相邻逐字注音会被合并为词级注音）
	}{
		{`<ruby>呪<rt>じゅ</rt></ruby>`, `呪`, `<ruby>呪<rt>じゅ</rt></ruby>`},
		{`<ruby>勤<rt>いそ</rt></ruby>`, `勤`, `<ruby>勤<rt>いそ</rt></ruby>`},
		// 相邻 per-kanji ruby 被合并为词级注音
		{`<ruby>微<rt>ほほ</rt></ruby><ruby>笑<rt>え</rt></ruby>`, `微笑`, `<ruby>微笑<rt>ほほえ</rt></ruby>`},
		{`plain text without ruby`, `plain text without ruby`, `plain text without ruby`},
		{`<ruby>椎名<rt>しいな</rt></ruby>`, `椎名`, `<ruby>椎名<rt>しいな</rt></ruby>`},
	}
	for _, tc := range cases {
		seg := &pipeline.Segment{Source: tc.input}
		if err := (&RubyProtector{}).Protect(seg); err != nil {
			t.Fatalf("protect(%q): %v", tc.input, err)
		}
		if seg.Source != tc.base {
			t.Errorf("protect(%q): got source %q, want %q", tc.input, seg.Source, tc.base)
		}

		// 模拟 LLM 返回：target = 保护后的 source
		seg.Target = seg.Source

		// 通过 RubyRestorer 还原注音：从 Meta 中提取 ruby_annotations 并转换
		if seg.Meta != nil {
			if annotations, ok := seg.Meta["ruby_annotations"].([]RubyAnnotation); ok {
				rubyOutput := make([]RubyOutputEntry, len(annotations))
				for i, a := range annotations {
					rubyOutput[i] = RubyOutputEntry{Base: a.Base, Text: a.Text}
				}
				if err := restorer.Restore(seg, rubyOutput); err != nil {
					t.Fatalf("restore(%q): %v", tc.input, err)
				}
			}
		}

		if seg.Target != tc.restored {
			t.Errorf("round-trip(%q):\n  want: %q\n  got:  %q", tc.input, tc.restored, seg.Target)
		}
	}
}

// S10: RubyProtector 保护后 ruby 标签应被剥离，基底文本保留，注音存入 Meta。
func TestRubyProtector_ProtectsContent(t *testing.T) {
	seg := &pipeline.Segment{Source: `<ruby>呪<rt>じゅ</rt></ruby>`}
	if err := (&RubyProtector{}).Protect(seg); err != nil {
		t.Fatal(err)
	}
	// ruby 标签应被剥离
	if strings.Contains(seg.Source, "<ruby>") {
		t.Errorf("<ruby> tag should be stripped: %q", seg.Source)
	}
	if strings.Contains(seg.Source, "<rt>") {
		t.Errorf("<rt> tag should be stripped: %q", seg.Source)
	}
	// 注音内容不应出现在 Source 中
	if strings.Contains(seg.Source, "じゅ") {
		t.Errorf("furigana not protected: %q", seg.Source)
	}
	// 基底文本应保留
	if !strings.Contains(seg.Source, "呪") {
		t.Errorf("kanji lost: %q", seg.Source)
	}
	// 注音应存入 Meta
	if seg.Meta == nil {
		t.Fatal("Meta is nil after protect")
	}
	annotations, ok := seg.Meta["ruby_annotations"].([]RubyAnnotation)
	if !ok {
		t.Fatalf("ruby_annotations not found or wrong type in Meta: %v", seg.Meta)
	}
	if len(annotations) == 0 {
		t.Fatal("ruby_annotations is empty")
	}
	if annotations[0].Base != "呪" || annotations[0].Text != "じゅ" {
		t.Errorf("annotation mismatch: got base=%q text=%q, want base=呪 text=じゅ",
			annotations[0].Base, annotations[0].Text)
	}
}

// S11: RubyProtector + XMLProtector 组合保护后，LLM 看不到注音内容。
func TestCompose_RubyAndXML_HidesContent(t *testing.T) {
	seg := &pipeline.Segment{Source: `<ruby>呪<rt>じゅ</rt></ruby>`}
	p := Compose(&RubyProtector{}, &XMLProtector{})
	if err := p.Protect(seg); err != nil {
		t.Fatal(err)
	}
	// 注音内容应被保护
	if strings.Contains(seg.Source, "じゅ") {
		t.Errorf("furigana visible to LLM: %q", seg.Source)
	}
	// 汉字仍应可见（可翻译）
	if !strings.Contains(seg.Source, "呪") {
		t.Errorf("kanji lost: %q", seg.Source)
	}
}

// S12: FromRules 不再处理 "ruby"，RubyProtector 由 buildPipeline 单独处理。
func TestFromRules_NoRuby(t *testing.T) {
	p := FromRules([]string{"code", "link", "placeholder", "ruby", "xml"})
	source := `<ruby>呪<rt>じゅ</rt></ruby>`
	seg := &pipeline.Segment{Source: source}
	if err := p.Protect(seg); err != nil {
		t.Fatal(err)
	}
	// FromRules 不含 RubyProtector，所以 ruby 标签应原样保留给 XMLProtector 处理
	// 但 "じゅ" 不应通过 RubyProtector 被提取
	if seg.Meta != nil {
		if _, ok := seg.Meta["ruby_annotations"]; ok {
			t.Error("FromRules should not include RubyProtector")
		}
	}
}

// S14: 无 <rt> 标签时 RubyProtector 不影响内容。
func TestRubyProtector_NoRtTags(t *testing.T) {
	cases := []string{
		`plain text`,
		`<p>no ruby here</p>`,
	}
	for _, c := range cases {
		seg := &pipeline.Segment{Source: c}
		if err := (&RubyProtector{}).Protect(seg); err != nil {
			t.Fatal(err)
		}
		if seg.Source != c {
			t.Errorf("RubyProtector modified non-ruby text: %q → %q", c, seg.Source)
		}
	}
}

// S15: mergeAdjacentPlaceholders 合并后的值应正确拼接。
func TestMergeAdjacentPlaceholders_MergedValue(t *testing.T) {
	seg := &pipeline.Segment{
		Source: "__LF_000001____LF_000002__",
		Protected: map[string]string{
			"__LF_000001__": "<ruby>",
			"__LF_000002__": "<rt>",
		},
	}
	mergeAdjacentPlaceholders(seg)
	// 合并后应只剩一个 key，值为 "<ruby><rt>"
	if len(seg.Protected) != 1 {
		t.Fatalf("expected 1 key, got %d: %v", len(seg.Protected), seg.Protected)
	}
	for _, v := range seg.Protected {
		if v != "<ruby><rt>" {
			t.Errorf("merged value: got %q, want %q", v, "<ruby><rt>")
		}
	}
}

// S16: RubyProtector + XMLProtector + mergeAdjacentPlaceholders + RubyRestorer 完整 round-trip。
// 模拟实际 pipeline：Protect → LLM → Unprotect → RubyRestore。
func TestMergeAdjacentPlaceholders_RoundTrip(t *testing.T) {
	restorer := NewRubyRestorer("ruby_output")
	cases := []struct {
		input    string
		restored string // 还原后期望的 Target（相邻逐字注音会被合并为词级注音）
	}{
		{`<ruby>呪<rt>じゅ</rt></ruby>`, `<ruby>呪<rt>じゅ</rt></ruby>`},
		{`────</span><ruby>椎名<rt>しいな</rt></ruby>`, `────</span><ruby>椎名<rt>しいな</rt></ruby>`},
		{`<ruby>微<rt>ほほ</rt></ruby><ruby>笑<rt>え</rt></ruby>`, `<ruby>微笑<rt>ほほえ</rt></ruby>`},
	}
	p := Compose(&RubyProtector{}, &XMLProtector{})
	for _, tc := range cases {
		seg := &pipeline.Segment{Source: tc.input}
		if err := p.Protect(seg); err != nil {
			t.Fatalf("protect(%q): %v", tc.input, err)
		}
		// 模拟 LLM 返回
		seg.Target = seg.Source
		// Unprotect 还原 XML 占位符
		if err := p.Unprotect(seg); err != nil {
			t.Fatalf("unprotect(%q): %v", tc.input, err)
		}
		// RubyRestorer 还原注音
		if seg.Meta != nil {
			if annotations, ok := seg.Meta["ruby_annotations"].([]RubyAnnotation); ok {
				rubyOutput := make([]RubyOutputEntry, len(annotations))
				for i, a := range annotations {
					rubyOutput[i] = RubyOutputEntry{Base: a.Base, Text: a.Text}
				}
				if len(rubyOutput) > 0 {
					if err := restorer.Restore(seg, rubyOutput); err != nil {
						t.Fatalf("restore(%q): %v", tc.input, err)
					}
				}
			}
		}
		if seg.Target != tc.restored {
			t.Errorf("round-trip(%q):\n  want: %q\n  got:  %q", tc.input, tc.restored, seg.Target)
		}
	}
}
