package epub

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// testChapter 定义测试用章节数据。
type testChapter struct {
	filename string // ZIP 内文件名，如 "OEBPS/chapter1.xhtml"
	content  string // XHTML body 内容
	id       string // manifest 中的 id
}

// createTestEPUB 在内存中创建一个最小的 EPUB ZIP。
// chapters 按顺序出现在 spine 中。
func createTestEPUB(t *testing.T, chapters []testChapter) []byte {
	t.Helper()
	return createTestEPUBWithTitle(t, chapters, "Test Book")
}

// createTestEPUBWithTitle 创建带自定义标题的测试 EPUB。
func createTestEPUBWithTitle(t *testing.T, chapters []testChapter, title string) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	// 1. mimetype（第一个条目，不压缩）
	mw, err := w.CreateHeader(&zip.FileHeader{
		Name:   "mimetype",
		Method: zip.Store,
	})
	if err != nil {
		t.Fatalf("create mimetype: %v", err)
	}
	if _, err := io.WriteString(mw, "application/epub+zip"); err != nil {
		t.Fatalf("write mimetype: %v", err)
	}

	// 2. META-INF/container.xml
	cw, err := w.Create("META-INF/container.xml")
	if err != nil {
		t.Fatalf("create container.xml: %v", err)
	}
	if _, err := io.WriteString(cw, `<?xml version="1.0" encoding="UTF-8"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`); err != nil {
		t.Fatalf("write container.xml: %v", err)
	}

	// 3. OEBPS/content.opf
	var manifestItems, spineItems string
	for _, ch := range chapters {
		// href 在 OPF 中是相对于 OPF 所在目录的路径
		manifestItems += fmt.Sprintf(`    <item id="%s" href="%s" media-type="application/xhtml+xml"/>
`, ch.id, path.Base(ch.filename))
		spineItems += fmt.Sprintf(`    <itemref idref="%s"/>
`, ch.id)
	}

	opf := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>%s</dc:title>
    <dc:identifier id="uid">urn:uuid:12345</dc:identifier>
  </metadata>
  <manifest>
%s  </manifest>
  <spine>
%s  </spine>
</package>`, title, manifestItems, spineItems)

	ow, err := w.Create("OEBPS/content.opf")
	if err != nil {
		t.Fatalf("create content.opf: %v", err)
	}
	if _, err := io.WriteString(ow, opf); err != nil {
		t.Fatalf("write content.opf: %v", err)
	}

	// 4. 各章节 XHTML
	for _, ch := range chapters {
		cw, err := w.Create(ch.filename)
		if err != nil {
			t.Fatalf("create %s: %v", ch.filename, err)
		}
		xhtml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>Chapter</title></head>
<body>
%s
</body>
</html>`, ch.content)
		if _, err := io.WriteString(cw, xhtml); err != nil {
			t.Fatalf("write %s: %v", ch.filename, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// createTestEPUBWithExtraFiles 创建包含额外非 XHTML 文件的测试 EPUB。
func createTestEPUBWithExtraFiles(t *testing.T, chapters []testChapter, extraFiles map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	// mimetype
	mw, err := w.CreateHeader(&zip.FileHeader{
		Name:   "mimetype",
		Method: zip.Store,
	})
	if err != nil {
		t.Fatalf("create mimetype: %v", err)
	}
	if _, err := io.WriteString(mw, "application/epub+zip"); err != nil {
		t.Fatalf("write mimetype: %v", err)
	}

	// container.xml
	cw, err := w.Create("META-INF/container.xml")
	if err != nil {
		t.Fatalf("create container.xml: %v", err)
	}
	if _, err := io.WriteString(cw, `<?xml version="1.0" encoding="UTF-8"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`); err != nil {
		t.Fatalf("write container.xml: %v", err)
	}

	// content.opf
	var manifestItems, spineItems string
	for _, ch := range chapters {
		manifestItems += fmt.Sprintf(`    <item id="%s" href="%s" media-type="application/xhtml+xml"/>
`, ch.id, path.Base(ch.filename))
		spineItems += fmt.Sprintf(`    <itemref idref="%s"/>
`, ch.id)
	}

	opf := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Test Book</dc:title>
    <dc:identifier id="uid">urn:uuid:12345</dc:identifier>
  </metadata>
  <manifest>
%s  </manifest>
  <spine>
%s  </spine>
</package>`, manifestItems, spineItems)

	ow, err := w.Create("OEBPS/content.opf")
	if err != nil {
		t.Fatalf("create content.opf: %v", err)
	}
	if _, err := io.WriteString(ow, opf); err != nil {
		t.Fatalf("write content.opf: %v", err)
	}

	// 章节
	for _, ch := range chapters {
		cw, err := w.Create(ch.filename)
		if err != nil {
			t.Fatalf("create %s: %v", ch.filename, err)
		}
		xhtml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>Chapter</title></head>
<body>
%s
</body>
</html>`, ch.content)
		if _, err := io.WriteString(cw, xhtml); err != nil {
			t.Fatalf("write %s: %v", ch.filename, err)
		}
	}

	// 额外文件
	for name, content := range extraFiles {
		ew, err := w.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := io.WriteString(ew, content); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// newParser 返回一个新的 EPUB Parser 实例。
func newParser() *Parser { return New() }

// ==========================================================================
// Parse 测试
// ==========================================================================

func TestParse(t *testing.T) {
	t.Run("testParseSimpleEPUB", testParseSimpleEPUB)
	t.Run("testParseMultiChapterEPUB", testParseMultiChapterEPUB)
	t.Run("testParseEPUBWithInlineTags", testParseEPUBWithInlineTags)
	t.Run("testParseEPUBWithRuby", testParseEPUBWithRuby)
	t.Run("testParseEPUBSkipsScriptAndStyle", testParseEPUBSkipsScriptAndStyle)
	t.Run("testParseEPUBSkipsEmptyParagraph", testParseEPUBSkipsEmptyParagraph)
	t.Run("testParseEPUBTocNav", testParseEPUBTocNav)
	t.Run("testParseEPUBSkipsPageListNav", testParseEPUBSkipsPageListNav)
	t.Run("testParseEPUBSkipsLandmarksNav", testParseEPUBSkipsLandmarksNav)
	t.Run("testParseEPUBMeta", testParseEPUBMeta)
	t.Run("testParseEPUBNestedXHTML", testParseEPUBNestedXHTML)
}

func testParseSimpleEPUB(t *testing.T) {
	data := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/chapter1.xhtml", content: "<p>こんにちは世界</p>", id: "ch1"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(doc.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(doc.Segments))
	}

	seg := doc.Segments[0]
	if seg.Source != "こんにちは世界" {
		t.Errorf("Source = %q, want %q", seg.Source, "こんにちは世界")
	}

	requiredMeta := []string{"epub_file", "epub_title", "epub_id", "element_path", "content_hash", "tag"}
	for _, key := range requiredMeta {
		if _, ok := seg.Meta[key]; !ok {
			t.Errorf("Meta missing key %q", key)
		}
	}
}

func testParseMultiChapterEPUB(t *testing.T) {
	data := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>第一章内容</p>", id: "ch1"},
		{filename: "OEBPS/ch2.xhtml", content: "<p>第二章内容</p>", id: "ch2"},
		{filename: "OEBPS/ch3.xhtml", content: "<p>第三章内容</p>", id: "ch3"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(doc.Segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(doc.Segments))
	}

	expected := []struct {
		file  string
		title string
		id    string
	}{
		{"OEBPS/ch1.xhtml", "Test Book", "ch1"},
		{"OEBPS/ch2.xhtml", "Test Book", "ch2"},
		{"OEBPS/ch3.xhtml", "Test Book", "ch3"},
	}

	for i, seg := range doc.Segments {
		if seg.Source != expected[i].title[:0]+"第" {
			// 只检查 epub_file 和 epub_id
		}
		if ep, ok := seg.Meta["epub_file"].(string); !ok || ep != expected[i].file {
			t.Errorf("segment[%d] epub_file = %v, want %q", i, ep, expected[i].file)
		}
		if id, ok := seg.Meta["epub_id"].(string); !ok || id != expected[i].id {
			t.Errorf("segment[%d] epub_id = %v, want %q", i, id, expected[i].id)
		}
		if et, ok := seg.Meta["epub_title"].(string); !ok || et != expected[i].title {
			t.Errorf("segment[%d] epub_title = %v, want %q", i, et, expected[i].title)
		}
	}
}

func testParseEPUBWithInlineTags(t *testing.T) {
	data := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>これは<strong>重要</strong>なです</p>", id: "ch1"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(doc.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(doc.Segments))
	}

	expected := "これは<strong>重要</strong>なです"
	if doc.Segments[0].Source != expected {
		t.Errorf("Source = %q, want %q", doc.Segments[0].Source, expected)
	}
}

func testParseEPUBWithRuby(t *testing.T) {
	data := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p><ruby>漢字<rt>かんじ</rt></ruby>です</p>", id: "ch1"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(doc.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(doc.Segments))
	}

	expected := "<ruby>漢字<rt>かんじ</rt></ruby>です"
	if doc.Segments[0].Source != expected {
		t.Errorf("Source = %q, want %q", doc.Segments[0].Source, expected)
	}
}

func testParseEPUBSkipsScriptAndStyle(t *testing.T) {
	// script/style 作为独立元素（非块级元素的子元素）应被完全跳过
	data := createTestEPUB(t, []testChapter{
		{
			filename: "OEBPS/ch1.xhtml",
			content:  "<p>text</p><script>var x=1;</script><style>.a{}</style>",
			id:       "ch1",
		},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(doc.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(doc.Segments))
	}
	if doc.Segments[0].Source != "text" {
		t.Errorf("Source = %q, want %q", doc.Segments[0].Source, "text")
	}
}

func testParseEPUBSkipsEmptyParagraph(t *testing.T) {
	data := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p></p><p>  </p>", id: "ch1"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(doc.Segments) != 0 {
		t.Fatalf("expected 0 segments, got %d", len(doc.Segments))
	}
}

func testParseEPUBTocNav(t *testing.T) {
	navContent := `<nav epub:type="toc">
<ul>
<li><a href="#ch1">第一章</a></li>
<li><a href="#ch2">第二章</a></li>
</ul>
</nav>`
	data := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/nav.xhtml", content: navContent, id: "nav"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(doc.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(doc.Segments))
	}

	expected := []string{"第一章", "第二章"}
	for i, seg := range doc.Segments {
		if seg.Source != expected[i] {
			t.Errorf("segment[%d] Source = %q, want %q", i, seg.Source, expected[i])
		}
		if tag, ok := seg.Meta["tag"].(string); !ok || tag != "a" {
			t.Errorf("segment[%d] tag = %v, want %q", i, seg.Meta["tag"], "a")
		}
	}
}

func testParseEPUBSkipsPageListNav(t *testing.T) {
	navContent := `<nav epub:type="page-list">
<ul><li><a href="#p1">1</a></li></ul>
</nav>`
	data := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/nav.xhtml", content: navContent, id: "nav"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(doc.Segments) != 0 {
		t.Fatalf("expected 0 segments, got %d", len(doc.Segments))
	}
}

func testParseEPUBSkipsLandmarksNav(t *testing.T) {
	navContent := `<nav epub:type="landmarks">
<ul><li><a href="#toc">目录</a></li></ul>
</nav>`
	data := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/nav.xhtml", content: navContent, id: "nav"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(doc.Segments) != 0 {
		t.Fatalf("expected 0 segments, got %d", len(doc.Segments))
	}
}

func testParseEPUBMeta(t *testing.T) {
	data := createTestEPUBWithTitle(t, []testChapter{
		{filename: "OEBPS/chapter1.xhtml", content: "<p>テスト</p>", id: "ch1"},
	}, "メタテスト")

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(doc.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(doc.Segments))
	}

	seg := doc.Segments[0]

	// epub_file
	if ep, ok := seg.Meta["epub_file"].(string); !ok || ep != "OEBPS/chapter1.xhtml" {
		t.Errorf("Meta[epub_file] = %v, want %q", seg.Meta["epub_file"], "OEBPS/chapter1.xhtml")
	}
	// epub_title
	if et, ok := seg.Meta["epub_title"].(string); !ok || et != "メタテスト" {
		t.Errorf("Meta[epub_title] = %v, want %q", seg.Meta["epub_title"], "メタテスト")
	}
	// epub_id
	if eid, ok := seg.Meta["epub_id"].(string); !ok || eid != "ch1" {
		t.Errorf("Meta[epub_id] = %v, want %q", seg.Meta["epub_id"], "ch1")
	}
	// element_path
	if _, ok := seg.Meta["element_path"].(string); !ok {
		t.Errorf("Meta[element_path] should be a non-empty string, got %v", seg.Meta["element_path"])
	}
	// content_hash
	if _, ok := seg.Meta["content_hash"].(string); !ok {
		t.Errorf("Meta[content_hash] should be a non-empty string, got %v", seg.Meta["content_hash"])
	}
	// tag
	if tag, ok := seg.Meta["tag"].(string); !ok || tag != "p" {
		t.Errorf("Meta[tag] = %v, want %q", seg.Meta["tag"], "p")
	}
}

func testParseEPUBNestedXHTML(t *testing.T) {
	content := `<div><p>段落1</p><p>段落2</p></div>`
	data := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: content, id: "ch1"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(doc.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(doc.Segments))
	}
	if doc.Segments[0].Source != "段落1" {
		t.Errorf("segment[0] Source = %q, want %q", doc.Segments[0].Source, "段落1")
	}
	if doc.Segments[1].Source != "段落2" {
		t.Errorf("segment[1] Source = %q, want %q", doc.Segments[1].Source, "段落2")
	}
}

// ==========================================================================
// Render 测试
// ==========================================================================

func TestRender(t *testing.T) {
	t.Run("testRenderRoundTrip", testRenderRoundTrip)
	t.Run("testRenderWithTranslation", testRenderWithTranslation)
	t.Run("testRenderMimetypeFirst", testRenderMimetypeFirst)
	t.Run("testRenderPreservesNonXHTML", testRenderPreservesNonXHTML)
	t.Run("testRenderEmptyDocument", testRenderEmptyDocument)
}

func testRenderRoundTrip(t *testing.T) {
	original := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>原文テキスト</p>", id: "ch1"},
	})

	p := newParser()

	// Parse
	doc, err := p.Parse(context.Background(), bytes.NewReader(original))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// 设置 Target = Source（原样 Render）
	for i := range doc.Segments {
		doc.Segments[i].Target = doc.Segments[i].Source
	}

	// Render
	var rendered bytes.Buffer
	err = p.Render(context.Background(), doc, bytes.NewReader(original), &rendered)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}

	// 重新 Parse 验证
	doc2, err := p.Parse(context.Background(), bytes.NewReader(rendered.Bytes()))
	if err != nil {
		t.Fatalf("Re-parse error: %v", err)
	}
	if len(doc2.Segments) != len(doc.Segments) {
		t.Fatalf("re-parsed segment count = %d, want %d", len(doc2.Segments), len(doc.Segments))
	}
	if doc2.Segments[0].Source != doc.Segments[0].Source {
		t.Errorf("re-parsed Source = %q, want %q", doc2.Segments[0].Source, doc.Segments[0].Source)
	}
}

func testRenderWithTranslation(t *testing.T) {
	original := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>原文テキスト</p>", id: "ch1"},
	})

	p := newParser()

	doc, err := p.Parse(context.Background(), bytes.NewReader(original))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// 修改 Target
	translation := "翻译文本"
	doc.Segments[0].Target = translation

	// Render
	var rendered bytes.Buffer
	err = p.Render(context.Background(), doc, bytes.NewReader(original), &rendered)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}

	// 重新 Parse 验证译文在正确位置
	doc2, err := p.Parse(context.Background(), bytes.NewReader(rendered.Bytes()))
	if err != nil {
		t.Fatalf("Re-parse error: %v", err)
	}
	if len(doc2.Segments) != 1 {
		t.Fatalf("re-parsed segment count = %d, want 1", len(doc2.Segments))
	}
	if doc2.Segments[0].Source != translation {
		t.Errorf("re-parsed Source = %q, want %q", doc2.Segments[0].Source, translation)
	}
}

func testRenderMimetypeFirst(t *testing.T) {
	original := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>text</p>", id: "ch1"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(original))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	for i := range doc.Segments {
		doc.Segments[i].Target = doc.Segments[i].Source
	}

	var rendered bytes.Buffer
	err = p.Render(context.Background(), doc, bytes.NewReader(original), &rendered)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}

	// 验证 mimetype 是第一个条目且不压缩
	zr, err := zip.NewReader(bytes.NewReader(rendered.Bytes()), int64(rendered.Len()))
	if err != nil {
		t.Fatalf("open rendered zip: %v", err)
	}
	if len(zr.File) == 0 {
		t.Fatal("rendered zip is empty")
	}

	first := zr.File[0]
	if first.Name != "mimetype" {
		t.Errorf("first entry = %q, want %q", first.Name, "mimetype")
	}
	if first.Method != zip.Store {
		t.Errorf("mimetype method = %d, want zip.Store (%d)", first.Method, zip.Store)
	}
}

func testRenderPreservesNonXHTML(t *testing.T) {
	original := createTestEPUBWithExtraFiles(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>text</p>", id: "ch1"},
	}, map[string]string{
		"OEBPS/styles/main.css":  "body { color: red; }",
		"OEBPS/images/cover.png": "\x89PNG\r\n\x1a\n fake png data",
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(original))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	for i := range doc.Segments {
		doc.Segments[i].Target = doc.Segments[i].Source
	}

	var rendered bytes.Buffer
	err = p.Render(context.Background(), doc, bytes.NewReader(original), &rendered)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}

	// 重新打开 rendered EPUB，验证非 XHTML 文件内容不变
	zr, err := zip.NewReader(bytes.NewReader(rendered.Bytes()), int64(rendered.Len()))
	if err != nil {
		t.Fatalf("open rendered zip: %v", err)
	}

	fileMap := make(map[string]string)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		fileMap[f.Name] = string(data)
	}

	if fileMap["OEBPS/styles/main.css"] != "body { color: red; }" {
		t.Errorf("CSS content changed")
	}
	if !strings.Contains(fileMap["OEBPS/images/cover.png"], "fake png data") {
		t.Errorf("image content changed")
	}
}

func testRenderEmptyDocument(t *testing.T) {
	original := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>原文</p>", id: "ch1"},
	})

	p := newParser()

	// 空 Document（无 Segments）
	emptyDoc := &pipeline.Document{
		Segments: []pipeline.Segment{},
		Format:   "epub",
	}

	var rendered bytes.Buffer
	err := p.Render(context.Background(), emptyDoc, bytes.NewReader(original), &rendered)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}

	// 验证输出仍然是有效的 EPUB，内容应与原始相同
	doc2, err := p.Parse(context.Background(), bytes.NewReader(rendered.Bytes()))
	if err != nil {
		t.Fatalf("Re-parse error: %v", err)
	}
	// 原文应保留（未被替换）
	if len(doc2.Segments) != 1 {
		t.Fatalf("re-parsed segment count = %d, want 1", len(doc2.Segments))
	}
	if doc2.Segments[0].Source != "原文" {
		t.Errorf("re-parsed Source = %q, want %q", doc2.Segments[0].Source, "原文")
	}
}

// ==========================================================================
// 边界情况测试
// ==========================================================================

func TestEdgeCases(t *testing.T) {
	t.Run("testParseDRMEpub", testParseDRMEpub)
	t.Run("testParseCorruptedZip", testParseCorruptedZip)
	t.Run("testParseMissingContainerXML", testParseMissingContainerXML)
}

func testParseDRMEpub(t *testing.T) {
	// 创建包含 encryption.xml 的 EPUB
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	mw, _ := w.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	io.WriteString(mw, "application/epub+zip")

	cw, _ := w.Create("META-INF/container.xml")
	io.WriteString(cw, `<?xml version="1.0" encoding="UTF-8"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)

	// DRM encryption.xml
	ew, _ := w.Create("META-INF/encryption.xml")
	io.WriteString(ew, `<?xml version="1.0" encoding="UTF-8"?>
<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <EncryptedData/>
</encryption>`)

	w.Close()

	p := newParser()
	_, err := p.Parse(context.Background(), bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Fatal("expected error for DRM protected EPUB, got nil")
	}
	if !strings.Contains(err.Error(), "DRM") {
		t.Errorf("error = %q, should contain 'DRM'", err.Error())
	}
}

func testParseCorruptedZip(t *testing.T) {
	corrupted := []byte("this is not a valid zip file content")
	p := newParser()
	_, err := p.Parse(context.Background(), bytes.NewReader(corrupted))
	if err == nil {
		t.Fatal("expected error for corrupted ZIP, got nil")
	}
}

func testParseMissingContainerXML(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	mw, _ := w.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	io.WriteString(mw, "application/epub+zip")

	// 故意不创建 container.xml
	w.Close()

	p := newParser()
	_, err := p.Parse(context.Background(), bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Fatal("expected error for missing container.xml, got nil")
	}
	if !strings.Contains(err.Error(), "container") {
		t.Errorf("error = %q, should mention container", err.Error())
	}
}

// ==========================================================================
// renderXHTML 修复验证测试
//
// 以下测试覆盖设计文档 backend/docs/epub-xhtml-fix-design.md 第 5 节中
// 列出的测试用例，验证 EPUB XHTML 处理器修复的正确性。
// ==========================================================================

// createTestZipFile 创建包含单个 XHTML 文件的内存 ZIP。
// 返回 zip.Reader 和文件条目（保持底层数据引用有效）。
func createTestZipFile(t *testing.T, filename, xhtmlContent string) (*zip.Reader, *zip.File) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, err := w.Create(filename)
	if err != nil {
		t.Fatalf("create %s: %v", filename, err)
	}
	if _, err := io.WriteString(fw, xhtmlContent); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	data := buf.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip reader: %v", err)
	}
	if len(zr.File) == 0 {
		t.Fatal("zip has no files")
	}
	return zr, zr.File[0]
}

// extractSegments 是对 extractSegmentsFromXHTML 的测试辅助封装。
func extractSegments(t *testing.T, xhtmlContent, filePath string) []pipeline.Segment {
	t.Helper()
	segs, err := extractSegmentsFromXHTML([]byte(xhtmlContent), filePath)
	if err != nil {
		t.Fatalf("extractSegmentsFromXHTML: %v", err)
	}
	return segs
}

// TestRenderPreservesXHTMLStructure 验证 xmlns 属性不重复、xmlns:epub
// 前缀保持正确、xml:lang 等属性保持不变。
func TestRenderPreservesXHTMLStructure(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="ja">
<head><title>Test</title></head>
<body><p>テスト</p></body>
</html>`

	_, f := createTestZipFile(t, "OEBPS/chapter.xhtml", xhtml)
	rendered, err := renderXHTML(f, nil)
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)

	// 验证 xmlns 属性不重复
	if n := strings.Count(output, `xmlns="http://www.w3.org/1999/xhtml"`); n != 1 {
		t.Errorf("xmlns 出现 %d 次（期望 1 次）:\n%s", n, output)
	}

	// 验证 xmlns:epub 前缀保持正确（不变成 _xmlns:epub）
	if !strings.Contains(output, `xmlns:epub="http://www.idpf.org/2007/ops"`) {
		t.Errorf("xmlns:epub 前缀未正确保留:\n%s", output)
	}
	if strings.Contains(output, "_xmlns:epub") {
		t.Errorf("xmlns:epub 被错误序列化为 _xmlns:epub:\n%s", output)
	}

	// 验证 xml:lang 属性保持不变
	if !strings.Contains(output, `xml:lang="ja"`) {
		t.Errorf("xml:lang 属性未保留:\n%s", output)
	}

	// 验证输出可通过 XML 解析器正常解析
	verifier := xml.NewDecoder(bytes.NewReader(rendered))
	for {
		_, err := verifier.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("输出不是合法的 XML: %v\n%s", err, output)
		}
	}
}

// TestRenderPreservesVoidElements 验证 void 元素（meta、link、br、img）
// 不生成闭合标签、不添加多余 xmlns。
func TestRenderPreservesVoidElements(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><meta charset="UTF-8" /><link rel="stylesheet" href="style.css" /></head>
<body><p>テスト</p><br/><img src="test.png" /></body>
</html>`

	_, f := createTestZipFile(t, "OEBPS/chapter.xhtml", xhtml)
	rendered, err := renderXHTML(f, nil)
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)

	// 验证 void 元素不生成闭合标签
	if strings.Contains(output, "</meta>") {
		t.Errorf("<meta> 不应生成闭合标签:\n%s", output)
	}
	if strings.Contains(output, "</br>") {
		t.Errorf("<br> 不应生成闭合标签:\n%s", output)
	}
	if strings.Contains(output, "</link>") {
		t.Errorf("<link> 不应生成闭合标签:\n%s", output)
	}
	if strings.Contains(output, "</img>") {
		t.Errorf("<img> 不应生成闭合标签:\n%s", output)
	}

	// 验证 void 元素保持原始自闭合格式
	if !strings.Contains(output, `<meta charset="UTF-8" />`) {
		t.Errorf("<meta> 自闭合格式未保留:\n%s", output)
	}
	if !strings.Contains(output, `<br/>`) {
		t.Errorf("<br/> 自闭合格式未保留:\n%s", output)
	}
	if !strings.Contains(output, `<img src="test.png" />`) {
		t.Errorf("<img> 自闭合格式未保留:\n%s", output)
	}

	// 验证 void 元素不添加多余 xmlns
	if strings.Count(output, `xmlns="http://www.w3.org/1999/xhtml"`) != 1 {
		t.Errorf("xmlns 属性应只出现一次:\n%s", output)
	}

	// 验证输出是合法 XML
	verifier := xml.NewDecoder(bytes.NewReader(rendered))
	for {
		_, err := verifier.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("输出不是合法的 XML: %v\n%s", err, output)
		}
	}
}

// TestRenderReplacesTranslation 验证日文原文被替换为中文译文，
// 非翻译内容保持不变。
func TestRenderReplacesTranslation(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body>
<p>テスト段落</p>
<p>変更しない段落</p>
</body>
</html>`

	_, f := createTestZipFile(t, "OEBPS/ch1.xhtml", xhtml)
	segs := extractSegments(t, xhtml, "OEBPS/ch1.xhtml")
	if len(segs) < 1 {
		t.Fatal("未提取到任何段落")
	}

	// 只翻译第一个段落
	segs[0].Target = "测试段落"
	rendered, err := renderXHTML(f, segs[:1])
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)

	// 验证日文原文被替换为中文译文
	if !strings.Contains(output, "测试段落") {
		t.Errorf("译文未出现在输出中:\n%s", output)
	}
	if strings.Contains(output, "テスト段落") {
		t.Errorf("日文原文未被替换:\n%s", output)
	}

	// 验证非翻译内容保持不变
	if !strings.Contains(output, "変更しない段落") {
		t.Errorf("非翻译内容丢失:\n%s", output)
	}
}

// TestSiblingElementPathUniqueness 验证同级同名元素生成不同的 element_path，
// 且都能被正确替换。
func TestSiblingElementPathUniqueness(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body>
<div>
	 <p>第一段</p>
	 <div>间隔</div>
	 <p>第二段</p>
	 <p>第三段</p>
</div>
</body>
</html>`

	_, f := createTestZipFile(t, "OEBPS/ch1.xhtml", xhtml)
	segs := extractSegments(t, xhtml, "OEBPS/ch1.xhtml")
	if len(segs) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segs))
	}

	// 验证三个 <p> 段落生成不同的 element_path
	paths := make(map[string]bool)
	for _, seg := range segs {
		ep, _ := seg.Meta["element_path"].(string)
		if paths[ep] {
			t.Errorf("element_path 重复: %q", ep)
		}
		paths[ep] = true
	}
	if len(paths) != 3 {
		t.Errorf("期望 3 个不同的 element_path，实际 %d 个", len(paths))
	}

	// 为每个段落设置不同译文
	segs[0].Target = "译文一"
	segs[1].Target = "译文二"
	segs[2].Target = "译文三"

	rendered, err := renderXHTML(f, segs)
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)

	// 验证三个段落都被正确替换
	for _, target := range []string{"译文一", "译文二", "译文三"} {
		if !strings.Contains(output, target) {
			t.Errorf("译文 %q 未出现在输出中:\n%s", target, output)
		}
	}
	// 验证原文被替换
	for _, src := range []string{"第一段", "第二段", "第三段"} {
		if strings.Contains(output, src) {
			t.Errorf("原文 %q 未被替换:\n%s", src, output)
		}
	}
	// 验证间隔内容保持不变
	if !strings.Contains(output, "间隔") {
		t.Errorf("间隔内容丢失:\n%s", output)
	}
}

// TestVoidElementPathStackIntegrity 验证包含 head/meta/link 的完整 XHTML 中，
// body 内元素路径包含 html 前缀，且翻译替换正常工作。
func TestVoidElementPathStackIntegrity(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><meta charset="UTF-8" /><link rel="stylesheet" href="style.css" /></head>
<body><p>テスト</p></body>
</html>`

	_, f := createTestZipFile(t, "OEBPS/ch1.xhtml", xhtml)
	segs := extractSegments(t, xhtml, "OEBPS/ch1.xhtml")
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segs))
	}

	// 验证路径包含 html 前缀
	ep, _ := segs[0].Meta["element_path"].(string)
	if !strings.HasPrefix(ep, "html/") {
		t.Errorf("element_path = %q, 应以 'html/' 开头", ep)
	}

	// 验证完整路径为 html/body/p（void 元素不破坏路径栈）
	expectedPath := "html/body/p"
	if ep != expectedPath {
		t.Errorf("element_path = %q, want %q", ep, expectedPath)
	}

	// 验证翻译替换正常工作
	segs[0].Target = "测试"
	rendered, err := renderXHTML(f, segs)
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)

	if !strings.Contains(output, "测试") {
		t.Errorf("译文未出现在输出中:\n%s", output)
	}
	if strings.Contains(output, "テスト") {
		t.Errorf("原文未被替换:\n%s", output)
	}

	// 验证 void 元素保持不变
	if !strings.Contains(output, `<meta charset="UTF-8" />`) {
		t.Errorf("meta 元素未保留:\n%s", output)
	}
	if strings.Contains(output, "</meta>") {
		t.Errorf("meta 不应有闭合标签:\n%s", output)
	}
}

// TestRenderReplaceWithNestedElements 验证替换模式下嵌套元素被正确跳过
// （replaceDepth 递增/递减）。
func TestRenderReplaceWithNestedElements(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body><p>テキスト<strong>太字</strong>もっと</p></body>
</html>`

	_, f := createTestZipFile(t, "OEBPS/ch1.xhtml", xhtml)
	segs := extractSegments(t, xhtml, "OEBPS/ch1.xhtml")
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segs))
	}

	segs[0].Target = "翻译文本"
	rendered, err := renderXHTML(f, segs)
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)

	// 验证译文完整写入，不被嵌套的 <strong> 打断
	if !strings.Contains(output, "翻译文本") {
		t.Errorf("译文未出现在输出中:\n%s", output)
	}
	// 验证嵌套的原始内容被替换（不应出现）
	if strings.Contains(output, "テキスト") {
		t.Errorf("原文テキスト 未被替换:\n%s", output)
	}
	if strings.Contains(output, "太字") {
		t.Errorf("原文太字 未被替换:\n%s", output)
	}
	// 验证 <p> 标签保持完整
	if !strings.Contains(output, "<p>") || !strings.Contains(output, "</p>") {
		t.Errorf("<p> 标签不完整:\n%s", output)
	}
}

// TestRenderReplaceWithVoidChildElement 验证替换模式下 void 子元素（如 br）
// 被正确处理。
func TestRenderReplaceWithVoidChildElement(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body><p>テキスト<br/>改行</p></body>
</html>`

	_, f := createTestZipFile(t, "OEBPS/ch1.xhtml", xhtml)
	segs := extractSegments(t, xhtml, "OEBPS/ch1.xhtml")
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segs))
	}

	segs[0].Target = "翻译替换"
	rendered, err := renderXHTML(f, segs)
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)

	// 验证译文完整写入
	if !strings.Contains(output, "翻译替换") {
		t.Errorf("译文未出现在输出中:\n%s", output)
	}
	// 验证 <br/> 被正确跳过（不在输出中单独出现）
	if strings.Contains(output, "<br/>") {
		t.Errorf("<br/> 应在替换模式中被跳过:\n%s", output)
	}
	// 验证 <p> 标签保持完整
	if !strings.Contains(output, "<p>翻译替换</p>") {
		t.Errorf("替换结果不正确:\n%s", output)
	}
}

// TestRenderEmptyTargetFallsBackToSource 验证 Target 为空时使用 Source
// 作为替换内容。
func TestRenderEmptyTargetFallsBackToSource(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body><p>テスト</p></body>
</html>`

	_, f := createTestZipFile(t, "OEBPS/ch1.xhtml", xhtml)
	segs := extractSegments(t, xhtml, "OEBPS/ch1.xhtml")
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segs))
	}

	// 不设置 Target（保持空字符串）
	segs[0].Target = ""
	rendered, err := renderXHTML(f, segs)
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)

	// 验证 Target 为空时，Source 作为替换内容
	if !strings.Contains(output, "テスト") {
		t.Errorf("Source 未作为 fallback 出现在输出中:\n%s", output)
	}
	if !strings.Contains(output, "<p>テスト</p>") {
		t.Errorf("fallback 替换结果不正确:\n%s", output)
	}
}

// TestRenderPreservesXMLProcessingInstruction 验证 <?xml ...?> 和
// <?xml-stylesheet ...?> 处理指令保持不变。
func TestRenderPreservesXMLProcessingInstruction(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<?xml-stylesheet type="text/css" href="style.css"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>Test</title></head>
<body><p>テスト</p></body>
</html>`

	_, f := createTestZipFile(t, "OEBPS/ch1.xhtml", xhtml)
	segs := extractSegments(t, xhtml, "OEBPS/ch1.xhtml")
	if len(segs) < 1 {
		t.Fatal("未提取到段落")
	}
	segs[0].Target = "测试"

	rendered, err := renderXHTML(f, segs)
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)

	// 验证 <?xml ...?> 处理指令保持在输出首位
	if !strings.HasPrefix(output, "<?xml ") {
		t.Errorf("输出不以 <?xml 开头:\n%s", output)
	}

	// 验证 <?xml-stylesheet ...?> 处理指令保持原样
	if !strings.Contains(output, `<?xml-stylesheet type="text/css" href="style.css"?>`) {
		t.Errorf("xml-stylesheet 处理指令未保留:\n%s", output)
	}

	// 验证译文替换正常（PI 不影响替换逻辑）
	if !strings.Contains(output, "测试") {
		t.Errorf("译文未出现在输出中:\n%s", output)
	}

	// 验证输出是合法 XML
	verifier := xml.NewDecoder(bytes.NewReader(rendered))
	for {
		_, err := verifier.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("输出不是合法的 XML: %v\n%s", err, output)
		}
	}
}

// TestPathTrackerChildCounterReset 验证深层嵌套中 pop 后子级计数器被重置。
func TestPathTrackerChildCounterReset(t *testing.T) {
	pt := newPathTracker()

	// 构建路径: div/div/p
	pt.push("div") // 外层 div
	pt.push("div") // 第一个内层 div
	pt.push("p")   // 第一个内层 div 中的 p

	if got := pt.path(); got != "div/div/p" {
		t.Errorf("第一次 path = %q, want %q", got, "div/div/p")
	}

	// pop p 和第一个内层 div
	pt.pop() // pop p，重置 p 的子级计数器
	pt.pop() // pop 第一个内层 div，重置 div 的子级计数器

	// 推入第二个内层 div 和 p
	pt.push("div") // 第二个内层 div（索引应为 1）
	pt.push("p")   // 第二个内层 div 中的 p（计数器应被重置为 0）

	// 验证路径不同（div 索引不同）
	if got := pt.path(); got != "div/div[1]/p" {
		t.Errorf("第二次 path = %q, want %q", got, "div/div[1]/p")
	}

	// 验证子级计数器被正确重置：p 的索引应为 0（不是 1）
	pt.pop() // pop p

	// 在第二个内层 div 中再推入一个 p
	pt.push("p")
	if got := pt.path(); got != "div/div[1]/p[1]" {
		t.Errorf("第三次 path = %q, want %q", got, "div/div[1]/p[1]")
	}
}

// TestRenderReplacesMultipleParagraphs 验证一个 XHTML 文件中多个 <p>
// 被替换为各自对应的译文。
func TestRenderReplacesMultipleParagraphs(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body>
<p>段落一</p>
<p>段落二</p>
<p>段落三</p>
</body>
</html>`

	_, f := createTestZipFile(t, "OEBPS/ch1.xhtml", xhtml)
	segs := extractSegments(t, xhtml, "OEBPS/ch1.xhtml")
	if len(segs) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(segs))
	}

	// 为每个段落设置不同译文
	segs[0].Target = "翻译一"
	segs[1].Target = "翻译二"
	segs[2].Target = "翻译三"

	rendered, err := renderXHTML(f, segs)
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)

	// 验证每个段落被替换为各自对应的译文
	for i, target := range []string{"翻译一", "翻译二", "翻译三"} {
		if !strings.Contains(output, target) {
			t.Errorf("segment[%d] 译文 %q 未出现在输出中:\n%s", i, target, output)
		}
	}

	// 验证原文被替换
	for _, src := range []string{"段落一", "段落二", "段落三"} {
		if strings.Contains(output, src) {
			t.Errorf("原文 %q 未被替换:\n%s", src, output)
		}
	}

	// 验证输出是合法 XML
	verifier := xml.NewDecoder(bytes.NewReader(rendered))
	for {
		_, err := verifier.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("输出不是合法的 XML: %v\n%s", err, output)
		}
	}
}
