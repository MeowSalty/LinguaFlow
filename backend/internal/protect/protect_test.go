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
			"__LF_000001__":   "SHORT",
			"__LF_000001__X":  "LONG",
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
