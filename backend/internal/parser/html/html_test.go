package html

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func parseHTML(t *testing.T, src string) *pipeline.Document {
	t.Helper()
	doc, err := New().Parse(context.Background(), strings.NewReader(src), "html")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func renderHTML(t *testing.T, doc *pipeline.Document, original string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := New().Render(context.Background(), doc, strings.NewReader(original), &buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String()
}

func byteRange(t *testing.T, seg pipeline.Segment) (int, int) {
	t.Helper()
	pos, ok := seg.Meta["html_byte_range"].([]int)
	if !ok || len(pos) < 2 {
		t.Fatalf("missing html_byte_range in meta: %#v", seg.Meta)
	}
	return pos[0], pos[1]
}

func sources(doc *pipeline.Document) []string {
	out := make([]string, len(doc.Segments))
	for i, s := range doc.Segments {
		out[i] = s.Source
	}
	return out
}

func TestParseBlockElements(t *testing.T) {
	src := `<!DOCTYPE html><html><body>` +
		`<p>Para</p>` +
		`<h2>Heading</h2>` +
		`<ul><li>Item</li></ul>` +
		`<table><tr><td>Cell</td></tr></table>` +
		`<blockquote>Quote</blockquote>` +
		`</body></html>`
	doc := parseHTML(t, src)
	if len(doc.Segments) != 5 {
		t.Fatalf("got %d segments, want 5: %+v", len(doc.Segments), sources(doc))
	}
	want := []struct {
		source string
		tag    string
	}{
		{"Para", "p"},
		{"Heading", "h2"},
		{"Item", "li"},
		{"Cell", "td"},
		{"Quote", "blockquote"},
	}
	for i, w := range want {
		seg := doc.Segments[i]
		if seg.Source != w.source {
			t.Errorf("seg[%d].Source = %q, want %q", i, seg.Source, w.source)
		}
		if seg.Meta["html_tag"] != w.tag {
			t.Errorf("seg[%d].html_tag = %v, want %q", i, seg.Meta["html_tag"], w.tag)
		}
		if seg.Meta["html_block"] != "block" {
			t.Errorf("seg[%d].html_block = %v, want block", i, seg.Meta["html_block"])
		}
		start, end := byteRange(t, seg)
		if string([]byte(src)[start:end]) != seg.Source {
			t.Errorf("seg[%d] byte range mismatch: %q vs %q", i, string([]byte(src)[start:end]), seg.Source)
		}
	}
}

func TestParseTitleAndHeading(t *testing.T) {
	src := `<!DOCTYPE html><html><head><title>Page Title</title></head>` +
		`<body><h1>Body Heading</h1></body></html>`
	doc := parseHTML(t, src)
	if len(doc.Segments) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(doc.Segments), sources(doc))
	}
	if doc.Segments[0].Source != "Page Title" || doc.Segments[0].Meta["html_tag"] != "title" {
		t.Errorf("seg[0] = %q tag=%v, want title", doc.Segments[0].Source, doc.Segments[0].Meta["html_tag"])
	}
	if doc.Segments[1].Source != "Body Heading" || doc.Segments[1].Meta["html_tag"] != "h1" {
		t.Errorf("seg[1] = %q tag=%v, want h1", doc.Segments[1].Source, doc.Segments[1].Meta["html_tag"])
	}
}

func TestSkipScriptStylePreCode(t *testing.T) {
	src := `<html><body>` +
		`<p>Before</p>` +
		`<script>var x = "no translate";</script>` +
		`<style>.x{color:red}</style>` +
		`<pre>preformatted</pre>` +
		`<p>Has <code>inline</code> code</p>` +
		`<p>After</p>` +
		`</body></html>`
	doc := parseHTML(t, src)
	for _, seg := range doc.Segments {
		if strings.Contains(seg.Source, "no translate") ||
			strings.Contains(seg.Source, "color:red") ||
			seg.Source == "preformatted" {
			t.Errorf("skip tag content should not be a segment: %q", seg.Source)
		}
	}
	// inline code is inside p, so p segment includes <code>…</code>
	found := false
	for _, seg := range doc.Segments {
		if strings.Contains(seg.Source, "<code>") {
			found = true
			if !strings.Contains(seg.Source, "inline") {
				t.Errorf("expected inline code markup in p source: %q", seg.Source)
			}
		}
	}
	if !found {
		t.Errorf("expected p segment containing <code>, got %+v", sources(doc))
	}
	out := renderHTML(t, doc, src)
	if out != src {
		t.Errorf("skip regions must pass through:\n got %q\nwant %q", out, src)
	}
}

func TestInlineTagsPreservedOnRender(t *testing.T) {
	src := `<html><body><p>Hello <b>world</b></p></body></html>`
	doc := parseHTML(t, src)
	if len(doc.Segments) != 1 {
		t.Fatalf("got %d segments: %+v", len(doc.Segments), sources(doc))
	}
	if doc.Segments[0].Source != "Hello <b>world</b>" {
		t.Fatalf("source = %q", doc.Segments[0].Source)
	}
	doc.Segments[0].Target = "你好 <b>世界</b>"
	out := renderHTML(t, doc, src)
	want := `<html><body><p>你好 <b>世界</b></p></body></html>`
	if out != want {
		t.Errorf("render:\n got %q\nwant %q", out, want)
	}
}

func TestBytePassthroughInvariants(t *testing.T) {
	src := `<!DOCTYPE html><!--c--><html><body>` +
		`<p class="a" id='b' data-x=1>Hi<br>there</p>` +
		`<img src="x.png" alt="pic">` +
		`</body></html>`
	doc := parseHTML(t, src)
	out := renderHTML(t, doc, src)
	if out != src {
		t.Errorf("byte passthrough:\n got %q\nwant %q", out, src)
	}
	if strings.Contains(out, "<br/>") || strings.Contains(out, "<br />") {
		t.Errorf("<br> must not be rewritten: %q", out)
	}
	if !strings.Contains(out, `class="a" id='b' data-x=1`) {
		t.Errorf("attribute order/quotes must be preserved: %q", out)
	}
	if !strings.Contains(out, "<!DOCTYPE html>") || !strings.Contains(out, "<!--c-->") {
		t.Errorf("doctype/comment must be preserved: %q", out)
	}
}

func TestMultipleParagraphsNonOverlapping(t *testing.T) {
	src := `<html><body><p>One</p><p>Two</p><p>Three</p></body></html>`
	doc := parseHTML(t, src)
	if len(doc.Segments) != 3 {
		t.Fatalf("got %d: %+v", len(doc.Segments), sources(doc))
	}
	var lastEnd int
	for i, seg := range doc.Segments {
		start, end := byteRange(t, seg)
		if start < lastEnd {
			t.Errorf("seg[%d] overlaps previous: start=%d lastEnd=%d", i, start, lastEnd)
		}
		lastEnd = end
	}
	doc.Segments[0].Target = "一"
	doc.Segments[1].Target = "二"
	doc.Segments[2].Target = "三"
	out := renderHTML(t, doc, src)
	want := `<html><body><p>一</p><p>二</p><p>三</p></body></html>`
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestRenderRoundTripUntranslated(t *testing.T) {
	src := `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Demo</title>
</head>
<body>
<h1>Hello</h1>
<p>Para with <b>bold</b> and <a href="/x">link</a>.</p>
<ul>
<li>item1</li>
<li>item2</li>
</ul>
<script>console.log(1)</script>
<style>p{color:red}</style>
</body>
</html>
`
	doc := parseHTML(t, src)
	out := renderHTML(t, doc, src)
	if out != src {
		t.Errorf("round-trip mismatch:\n got %q\nwant %q", out, src)
	}
}

func TestRenderReplacesTranslatedSegments(t *testing.T) {
	src := `<html><head><title>Hello</title></head><body>` +
		`<h1>Title</h1><p>World</p>` +
		`<script>var t="Hello";</script>` +
		`</body></html>`
	doc := parseHTML(t, src)
	if len(doc.Segments) != 3 {
		t.Fatalf("got %d segments: %+v", len(doc.Segments), sources(doc))
	}
	doc.Segments[0].Target = "你好"
	doc.Segments[1].Target = "标题"
	doc.Segments[2].Target = "世界"
	out := renderHTML(t, doc, src)
	if !strings.Contains(out, "<title>你好</title>") {
		t.Errorf("title not replaced: %q", out)
	}
	if !strings.Contains(out, "<h1>标题</h1>") {
		t.Errorf("h1 not replaced: %q", out)
	}
	if !strings.Contains(out, "<p>世界</p>") {
		t.Errorf("p not replaced: %q", out)
	}
	if !strings.Contains(out, `var t="Hello";`) {
		t.Errorf("script must remain: %q", out)
	}
	if strings.Contains(out, "<title>Hello</title>") || strings.Contains(out, "<h1>Title</h1>") || strings.Contains(out, "<p>World</p>") {
		t.Errorf("source text should be replaced: %q", out)
	}
}

func TestCRLFNormalization(t *testing.T) {
	src := "<html><body><p>Line\r\none</p></body></html>"
	doc := parseHTML(t, src)
	if len(doc.Segments) != 1 {
		t.Fatalf("got %d: %+v", len(doc.Segments), sources(doc))
	}
	if doc.Segments[0].Source != "Line\none" {
		t.Errorf("source = %q, want LF-normalized", doc.Segments[0].Source)
	}
	doc.Segments[0].Target = "行\n一"
	out := renderHTML(t, doc, src)
	// Render also normalizes CRLF, so output uses \n.
	want := "<html><body><p>行\n一</p></body></html>"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestEmptyBlockNoSegment(t *testing.T) {
	src := `<html><body><p>   </p><p>ok</p></body></html>`
	doc := parseHTML(t, src)
	if len(doc.Segments) != 1 || doc.Segments[0].Source != "ok" {
		t.Fatalf("got %+v, want [ok]", sources(doc))
	}
}

func TestFileSizeLimit(t *testing.T) {
	if maxHTMLBytes <= 0 {
		t.Fatal("maxHTMLBytes must be positive")
	}
	// 超限：LimitReader 读 max+1 字节即拒绝。
	big := strings.Repeat("a", maxHTMLBytes+1)
	_, err := New().Parse(context.Background(), strings.NewReader(big), "html")
	if err == nil {
		t.Fatal("expected size limit error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %v, want exceeds", err)
	}
	doc := parseHTML(t, "<html><body><p>ok</p></body></html>")
	if len(doc.Segments) != 1 {
		t.Fatalf("got %d", len(doc.Segments))
	}
}

func TestExtensions(t *testing.T) {
	exts := New().Extensions()
	if len(exts) != 2 || exts[0] != ".html" || exts[1] != ".htm" {
		t.Errorf("Extensions = %v", exts)
	}
}

func TestFormatIsHTML(t *testing.T) {
	doc := parseHTML(t, "<p>x</p>")
	if doc.Format != "html" {
		t.Errorf("Format = %q", doc.Format)
	}
}
