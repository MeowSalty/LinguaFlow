package markdown

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func parseMD(t *testing.T, src string) *pipeline.Document {
	t.Helper()
	doc, err := New().Parse(context.Background(), strings.NewReader(src), "md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func renderMD(t *testing.T, doc *pipeline.Document, original string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := New().Render(context.Background(), doc, strings.NewReader(original), &buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String()
}

func byteRange(t *testing.T, seg pipeline.Segment) (int, int) {
	t.Helper()
	pos, ok := seg.Meta["md_byte_range"].([]int)
	if !ok || len(pos) < 2 {
		t.Fatalf("missing md_byte_range in meta: %#v", seg.Meta)
	}
	return pos[0], pos[1]
}

func TestParseHeadings(t *testing.T) {
	src := "# H1\n\n## H2 Title\n\n###### H6\n"
	doc := parseMD(t, src)
	if len(doc.Segments) != 3 {
		t.Fatalf("got %d segments, want 3: %+v", len(doc.Segments), sources(doc))
	}
	want := []struct {
		source string
		level  int
	}{
		{"H1", 1},
		{"H2 Title", 2},
		{"H6", 6},
	}
	for i, w := range want {
		seg := doc.Segments[i]
		if seg.Source != w.source {
			t.Errorf("seg[%d].Source = %q, want %q", i, seg.Source, w.source)
		}
		if seg.Meta["md_block"] != "heading" {
			t.Errorf("seg[%d].md_block = %v, want heading", i, seg.Meta["md_block"])
		}
		if seg.Meta["md_level"] != w.level {
			t.Errorf("seg[%d].md_level = %v, want %d", i, seg.Meta["md_level"], w.level)
		}
		if strings.HasPrefix(seg.Source, "#") {
			t.Errorf("seg[%d].Source should not include # prefix: %q", i, seg.Source)
		}
		start, end := byteRange(t, seg)
		if string([]byte(src)[start:end]) != seg.Source {
			t.Errorf("seg[%d] byte range mismatch: %q vs %q", i, string([]byte(src)[start:end]), seg.Source)
		}
	}
}

func TestParseMultilineParagraph(t *testing.T) {
	src := "Line one\nsoft break continues\n\nNext para.\n"
	doc := parseMD(t, src)
	if len(doc.Segments) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(doc.Segments), sources(doc))
	}
	if !strings.Contains(doc.Segments[0].Source, "\n") {
		t.Errorf("expected soft break newline in first paragraph, got %q", doc.Segments[0].Source)
	}
	if doc.Segments[0].Meta["md_block"] != "paragraph" {
		t.Errorf("md_block = %v", doc.Segments[0].Meta["md_block"])
	}
	if doc.Segments[1].Source != "Next para." {
		t.Errorf("seg[1] = %q", doc.Segments[1].Source)
	}
}

func TestParseCompactAndLooseLists(t *testing.T) {
	src := "- a\n- b\n\n1. loose\n\n   continues\n"
	doc := parseMD(t, src)
	// 紧凑列表：2 项；松散列表：项内 2 个段落
	if len(doc.Segments) != 4 {
		t.Fatalf("got %d segments, want 4: %+v", len(doc.Segments), sources(doc))
	}
	for i, want := range []string{"a", "b", "loose", "continues"} {
		if doc.Segments[i].Source != want {
			t.Errorf("seg[%d] = %q, want %q", i, doc.Segments[i].Source, want)
		}
		if doc.Segments[i].Meta["md_block"] != "list_item" {
			t.Errorf("seg[%d].md_block = %v, want list_item", i, doc.Segments[i].Meta["md_block"])
		}
	}
}

func TestParseGFMTable(t *testing.T) {
	src := "| a | b |\n|---|---|\n| 1 | 2 |\n"
	doc := parseMD(t, src)
	if len(doc.Segments) != 4 {
		t.Fatalf("got %d segments, want 4 cells: %+v", len(doc.Segments), sources(doc))
	}
	for i, want := range []string{"a", "b", "1", "2"} {
		if doc.Segments[i].Source != want {
			t.Errorf("seg[%d] = %q, want %q", i, doc.Segments[i].Source, want)
		}
		if doc.Segments[i].Meta["md_block"] != "table_cell" {
			t.Errorf("seg[%d].md_block = %v", i, doc.Segments[i].Meta["md_block"])
		}
	}
	// 分隔行与管道符在未翻译渲染中必须保留。
	out := renderMD(t, doc, src)
	if out != src {
		t.Errorf("round-trip table skeleton:\n got %q\nwant %q", out, src)
	}
}

func TestParseBlockquote(t *testing.T) {
	src := "> quoted text\n>\n> second\n"
	doc := parseMD(t, src)
	if len(doc.Segments) < 1 {
		t.Fatal("expected blockquote segments")
	}
	for _, seg := range doc.Segments {
		if seg.Meta["md_block"] != "blockquote" {
			t.Errorf("md_block = %v, want blockquote (source=%q)", seg.Meta["md_block"], seg.Source)
		}
		if strings.HasPrefix(strings.TrimSpace(seg.Source), ">") {
			t.Errorf("source should not include > marker: %q", seg.Source)
		}
	}
}

func TestParseFencedCodeNoSegment(t *testing.T) {
	src := "Before\n\n```go\nfmt.Println(\"hi\")\n```\n\nAfter\n"
	doc := parseMD(t, src)
	if len(doc.Segments) != 2 {
		t.Fatalf("got %d segments, want 2 (code excluded): %+v", len(doc.Segments), sources(doc))
	}
	if doc.Segments[0].Source != "Before" || doc.Segments[1].Source != "After" {
		t.Errorf("sources = %+v", sources(doc))
	}
	out := renderMD(t, doc, src)
	if out != src {
		t.Errorf("code block must pass through:\n got %q\nwant %q", out, src)
	}
}

func TestParseInlineCodeAndLink(t *testing.T) {
	src := "Use `fmt` and [docs](https://example.com).\n"
	doc := parseMD(t, src)
	if len(doc.Segments) != 1 {
		t.Fatalf("got %d segments: %+v", len(doc.Segments), sources(doc))
	}
	if !strings.Contains(doc.Segments[0].Source, "`fmt`") {
		t.Errorf("expected inline code in source: %q", doc.Segments[0].Source)
	}
	if !strings.Contains(doc.Segments[0].Source, "[docs](https://example.com)") {
		t.Errorf("expected link markdown in source: %q", doc.Segments[0].Source)
	}
}

func TestParseYAMLFrontmatter(t *testing.T) {
	src := "---\ntitle: hello\n---\n\n# Title\n\nBody text.\n"
	doc := parseMD(t, src)
	for _, seg := range doc.Segments {
		if strings.Contains(seg.Source, "title:") || seg.Source == "---" {
			t.Errorf("frontmatter should not produce segments: %q", seg.Source)
		}
	}
	if len(doc.Segments) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(doc.Segments), sources(doc))
	}
	out := renderMD(t, doc, src)
	if out != src {
		t.Errorf("frontmatter must pass through:\n got %q\nwant %q", out, src)
	}
}

func TestParseInvalidYAMLFrontmatterNotSegmented(t *testing.T) {
	// YAML 无效时 goldmark-meta 会留下文档级 TextBlock。
	src := "---\n: not valid yaml [\n---\n\n# Title\n"
	doc := parseMD(t, src)
	for _, seg := range doc.Segments {
		if strings.Contains(seg.Source, "not valid") || strings.Contains(seg.Source, "---") {
			t.Errorf("invalid frontmatter must not produce segments: %q", seg.Source)
		}
		if seg.Meta["md_block"] == "list_item" && !strings.Contains(src, "- ") {
			t.Errorf("unexpected list_item segment: %q", seg.Source)
		}
	}
	if len(doc.Segments) != 1 || doc.Segments[0].Source != "Title" {
		t.Fatalf("got %+v, want [Title]", sources(doc))
	}
	out := renderMD(t, doc, src)
	if out != src {
		t.Errorf("invalid frontmatter must pass through:\n got %q\nwant %q", out, src)
	}
}

func TestRenderRoundTripUntranslated(t *testing.T) {
	src := "---\nk: v\n---\n\n# Hello\n\nPara with **bold** and `code`.\n\n- item1\n- item2\n\n| h1 | h2 |\n|----|----|\n| c1 | c2 |\n\n```js\nconsole.log(1)\n```\n\n> quote me\n"
	doc := parseMD(t, src)
	out := renderMD(t, doc, src)
	if out != src {
		t.Errorf("round-trip mismatch:\n got %q\nwant %q", out, src)
	}
}

func TestRenderReplacesTranslatedSegments(t *testing.T) {
	src := "# Hello\n\nWorld\n"
	doc := parseMD(t, src)
	if len(doc.Segments) != 2 {
		t.Fatalf("got %d segments: %+v", len(doc.Segments), sources(doc))
	}
	doc.Segments[0].Target = "你好"
	doc.Segments[1].Target = "世界"
	out := renderMD(t, doc, src)
	if !strings.Contains(out, "# 你好\n") {
		t.Errorf("heading prefix should remain: %q", out)
	}
	if !strings.Contains(out, "世界") {
		t.Errorf("paragraph target missing: %q", out)
	}
	if strings.Contains(out, "Hello") || strings.Contains(out, "World") {
		t.Errorf("source text should be replaced: %q", out)
	}
}

func TestByteRangeMatchesSource(t *testing.T) {
	src := "# Title\n\nBody\n\n- list\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	raw := []byte(src)
	doc := parseMD(t, src)
	for i, seg := range doc.Segments {
		start, end := byteRange(t, seg)
		got := string(raw[start:end])
		if got != seg.Source {
			t.Errorf("seg[%d] range %d:%d = %q, Source = %q", i, start, end, got, seg.Source)
		}
	}
}

func TestFileSizeLimit(t *testing.T) {
	// 此处仅校验超限常量与正常小文件路径；不在 CI 中轻易分配 16MiB+ 内存。
	if maxMarkdownBytes <= 0 {
		t.Fatal("maxMarkdownBytes must be positive")
	}
	doc := parseMD(t, "ok\n")
	if len(doc.Segments) != 1 {
		t.Fatalf("got %d", len(doc.Segments))
	}
}

func sources(doc *pipeline.Document) []string {
	out := make([]string, len(doc.Segments))
	for i, s := range doc.Segments {
		out[i] = s.Source
	}
	return out
}
