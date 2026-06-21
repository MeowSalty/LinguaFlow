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
	title    string // <head><title> 内容，为空时使用 "Chapter"
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
		chTitle := ch.title
		if chTitle == "" {
			chTitle = "Chapter"
		}
		xhtml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>%s</title></head>
<body>
%s
</body>
</html>`, chTitle, ch.content)
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
		chTitle := ch.title
		if chTitle == "" {
			chTitle = "Chapter"
		}
		xhtml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>%s</title></head>
<body>
%s
</body>
</html>`, chTitle, ch.content)
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

	requiredMeta := []string{"epub_file", "epub_title", "epub_chapter_title", "epub_id", "element_path", "content_hash", "tag"}
	for _, key := range requiredMeta {
		if _, ok := seg.Meta[key]; !ok {
			t.Errorf("Meta missing key %q", key)
		}
	}
}

func testParseMultiChapterEPUB(t *testing.T) {
	data := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>第一章内容</p>", id: "ch1", title: "第一章 开始"},
		{filename: "OEBPS/ch2.xhtml", content: "<p>第二章内容</p>", id: "ch2", title: "第二章 发展"},
		{filename: "OEBPS/ch3.xhtml", content: "<p>第三章内容</p>", id: "ch3", title: "第三章 结局"},
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
		file         string
		bookTitle    string
		chapterTitle string
		id           string
	}{
		{"OEBPS/ch1.xhtml", "Test Book", "第一章 开始", "ch1"},
		{"OEBPS/ch2.xhtml", "Test Book", "第二章 发展", "ch2"},
		{"OEBPS/ch3.xhtml", "Test Book", "第三章 结局", "ch3"},
	}

	for i, seg := range doc.Segments {
		if ep, ok := seg.Meta["epub_file"].(string); !ok || ep != expected[i].file {
			t.Errorf("segment[%d] epub_file = %v, want %q", i, ep, expected[i].file)
		}
		if id, ok := seg.Meta["epub_id"].(string); !ok || id != expected[i].id {
			t.Errorf("segment[%d] epub_id = %v, want %q", i, id, expected[i].id)
		}
		if et, ok := seg.Meta["epub_title"].(string); !ok || et != expected[i].bookTitle {
			t.Errorf("segment[%d] epub_title = %v, want %q", i, et, expected[i].bookTitle)
		}
		if ct, ok := seg.Meta["epub_chapter_title"].(string); !ok || ct != expected[i].chapterTitle {
			t.Errorf("segment[%d] epub_chapter_title = %v, want %q", i, ct, expected[i].chapterTitle)
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
	content := `<div><p>段落 1</p><p>段落 2</p></div>`
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
	if doc.Segments[0].Source != "段落 1" {
		t.Errorf("segment[0] Source = %q, want %q", doc.Segments[0].Source, "段落1")
	}
	if doc.Segments[1].Source != "段落 2" {
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

	// 构建路径：div/div/p
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

// TestExtractChapterTitle 验证 extractChapterTitle 的标题提取逻辑。
func TestExtractChapterTitle(t *testing.T) {
	tests := []struct {
		name     string
		xhtml    string
		expected string
	}{
		{
			name: "优先使用 head 中的 title",
			xhtml: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>第一章 序言</title></head>
<body><h1>不应使用此标题</h1><p>内容</p></body>
</html>`,
			expected: "第一章 序言",
		},
		{
			name: "回退到 h1 标题",
			xhtml: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head></head>
<body><h1>章节标题</h1><p>内容</p></body>
</html>`,
			expected: "章节标题",
		},
		{
			name: "回退到 h2 标题",
			xhtml: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head></head>
<body><p>段落</p><h2>二级标题</h2><p>内容</p></body>
</html>`,
			expected: "二级标题",
		},
		{
			name: "回退到 h3 标题",
			xhtml: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head></head>
<body><p>段落</p><h3>三级标题</h3></body>
</html>`,
			expected: "三级标题",
		},
		{
			name: "无标题时返回空字符串",
			xhtml: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head></head>
<body><p>只有段落没有标题</p></body>
</html>`,
			expected: "",
		},
		{
			name: "title 为空时回退到 h1",
			xhtml: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>  </title></head>
<body><h1>实际标题</h1></body>
</html>`,
			expected: "实际标题",
		},
		{
			name: "标题含内联标签时提取纯文本",
			xhtml: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>第<b>一</b>章</title></head>
<body><p>内容</p></body>
</html>`,
			expected: "第一章",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractChapterTitle([]byte(tt.xhtml))
			if got != tt.expected {
				t.Errorf("extractChapterTitle() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// ==========================================================================
// NCX 目录标题提取测试
// ==========================================================================

func TestNCXTitleExtraction(t *testing.T) {
	t.Run("testNCXTitlesPriority", testNCXTitlesPriority)
	t.Run("testNCXTitlesOverwriteXHTMLTitle", testNCXTitlesOverwriteXHTMLTitle)
	t.Run("testNCXNestedNavPoints", testNCXNestedNavPoints)
	t.Run("testTOCFileFixedTitle", testTOCFileFixedTitle)
	t.Run("testNoNCXFallsBackToXHTMLTitle", testNoNCXFallsBackToXHTMLTitle)
	t.Run("testNCXWithAnchorInSrc", testNCXWithAnchorInSrc)
}

// createTestEPUBWithNCX 创建包含 NCX 文件的测试 EPUB。
// ncxNavPoints 定义 NCX 中的章节映射，格式为 []struct{Src, Label string}。
func createTestEPUBWithNCX(t *testing.T, chapters []testChapter, ncxNavPoints []struct{ Src, Label string }) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	// 1. mimetype
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

	// 2. container.xml
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

	// 3. content.opf (包含 NCX manifest 条目)
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
%s    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
  </manifest>
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

	// 4. toc.ncx
	var navPointsXML string
	for _, np := range ncxNavPoints {
		navPointsXML += fmt.Sprintf(`    <navPoint>
      <navLabel><text>%s</text></navLabel>
      <content src="%s"/>
    </navPoint>
`, np.Label, np.Src)
	}

	ncxXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head>
    <meta name="dtb:uid" content="urn:uuid:12345"/>
  </head>
  <docTitle><text>Test Book</text></docTitle>
  <navMap>
%s  </navMap>
</ncx>`, navPointsXML)

	ncxw, err := w.Create("OEBPS/toc.ncx")
	if err != nil {
		t.Fatalf("create toc.ncx: %v", err)
	}
	if _, err := io.WriteString(ncxw, ncxXML); err != nil {
		t.Fatalf("write toc.ncx: %v", err)
	}

	// 5. 各章节 XHTML
	for _, ch := range chapters {
		cw, err := w.Create(ch.filename)
		if err != nil {
			t.Fatalf("create %s: %v", ch.filename, err)
		}
		chTitle := ch.title
		if chTitle == "" {
			chTitle = "Chapter"
		}
		xhtml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>%s</title></head>
<body>
%s
</body>
</html>`, chTitle, ch.content)
		if _, err := io.WriteString(cw, xhtml); err != nil {
			t.Fatalf("write %s: %v", ch.filename, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// testNCXTitlesPriority 验证 NCX 中的标题优先于 XHTML 中的 <title>。
func testNCXTitlesPriority(t *testing.T) {
	data := createTestEPUBWithNCX(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>内容 1</p>", id: "ch1", title: "XHTML 标题 1"},
		{filename: "OEBPS/ch2.xhtml", content: "<p>内容 2</p>", id: "ch2", title: "XHTML 标题 2"},
	}, []struct{ Src, Label string }{
		{"ch1.xhtml", "NCX 第一章"},
		{"ch2.xhtml", "NCX 第二章"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(doc.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(doc.Segments))
	}

	// NCX 标题应优先于 XHTML <title>
	if ct, ok := doc.Segments[0].Meta["epub_chapter_title"].(string); !ok || ct != "NCX 第一章" {
		t.Errorf("segment[0] epub_chapter_title = %v, want %q", doc.Segments[0].Meta["epub_chapter_title"], "NCX第一章")
	}
	if ct, ok := doc.Segments[1].Meta["epub_chapter_title"].(string); !ok || ct != "NCX 第二章" {
		t.Errorf("segment[1] epub_chapter_title = %v, want %q", doc.Segments[1].Meta["epub_chapter_title"], "NCX第二章")
	}
}

// testNCXTitlesOverwriteXHTMLTitle 验证 NCX 中有标题时，XHTML <title> 不会被使用。
func testNCXTitlesOverwriteXHTMLTitle(t *testing.T) {
	data := createTestEPUBWithNCX(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>内容</p>", id: "ch1", title: "书籍名称"},
	}, []struct{ Src, Label string }{
		{"ch1.xhtml", "プロローグ"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(doc.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(doc.Segments))
	}

	// NCX 标题 "プロローグ" 应优先于 XHTML <title> "书籍名称"
	ct, ok := doc.Segments[0].Meta["epub_chapter_title"].(string)
	if !ok || ct != "プロローグ" {
		t.Errorf("epub_chapter_title = %v, want %q", ct, "プロローグ")
	}
}

// testNCXNestedNavPoints 验证嵌套的 navPoint 也能正确提取标题。
func testNCXNestedNavPoints(t *testing.T) {
	// 手动构建带嵌套 navPoint 的 EPUB
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	// mimetype
	mw, _ := w.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	io.WriteString(mw, "application/epub+zip")

	// container.xml
	cw, _ := w.Create("META-INF/container.xml")
	io.WriteString(cw, `<?xml version="1.0" encoding="UTF-8"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)

	// content.opf
	ow, _ := w.Create("OEBPS/content.opf")
	io.WriteString(ow, `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Test Book</dc:title>
    <dc:identifier id="uid">urn:uuid:12345</dc:identifier>
  </metadata>
  <manifest>
    <item id="ch1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
    <item id="ch2" href="ch2.xhtml" media-type="application/xhtml+xml"/>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
  </manifest>
  <spine>
    <itemref idref="ch1"/>
    <itemref idref="ch2"/>
  </spine>
</package>`)

	// toc.ncx with nested navPoints
	ncxw, _ := w.Create("OEBPS/toc.ncx")
	io.WriteString(ncxw, `<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head/>
  <docTitle><text>Test</text></docTitle>
  <navMap>
    <navPoint>
      <navLabel><text>第一部</text></navLabel>
      <content src="ch1.xhtml"/>
      <navPoint>
        <navLabel><text>第一章</text></navLabel>
        <content src="ch2.xhtml"/>
      </navPoint>
    </navPoint>
  </navMap>
</ncx>`)

	// ch1.xhtml
	c1w, _ := w.Create("OEBPS/ch1.xhtml")
	io.WriteString(c1w, `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>嵌套测试</title></head>
<body><p>内容 1</p></body>
</html>`)

	// ch2.xhtml
	c2w, _ := w.Create("OEBPS/ch2.xhtml")
	io.WriteString(c2w, `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>嵌套测试 2</title></head>
<body><p>内容 2</p></body>
</html>`)

	w.Close()

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(doc.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(doc.Segments))
	}

	// ch1 应使用嵌套 navPoint 中的第一个匹配（第一部）
	if ct, ok := doc.Segments[0].Meta["epub_chapter_title"].(string); !ok || ct != "第一部" {
		t.Errorf("segment[0] epub_chapter_title = %v, want %q", doc.Segments[0].Meta["epub_chapter_title"], "第一部")
	}
	// ch2 应使用嵌套的 navPoint（第一章）
	if ct, ok := doc.Segments[1].Meta["epub_chapter_title"].(string); !ok || ct != "第一章" {
		t.Errorf("segment[1] epub_chapter_title = %v, want %q", doc.Segments[1].Meta["epub_chapter_title"], "第一章")
	}
}

// testTOCFileFixedTitle 验证目录文件（文件名含 "toc"）使用固定名称 "Contents"。
func testTOCFileFixedTitle(t *testing.T) {
	// 使用 NCX，但其中一个章节文件名含 "toc"
	data := createTestEPUBWithNCX(t, []testChapter{
		{filename: "OEBPS/p-toc-001.xhtml", content: "<p>目录内容</p>", id: "toc", title: "书籍名称"},
		{filename: "OEBPS/ch1.xhtml", content: "<p>章节内容</p>", id: "ch1", title: "第一章"},
	}, []struct{ Src, Label string }{
		{"p-toc-001.xhtml", "不应使用此标题"},
		{"ch1.xhtml", "NCX 第一章"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(doc.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(doc.Segments))
	}

	// TOC 文件应使用固定名称 "Contents"（优先级最高）
	ct, ok := doc.Segments[0].Meta["epub_chapter_title"].(string)
	if !ok || ct != "Contents" {
		t.Errorf("TOC segment epub_chapter_title = %v, want %q", ct, "Contents")
	}

	// 非 TOC 文件应使用 NCX 标题
	ct2, ok := doc.Segments[1].Meta["epub_chapter_title"].(string)
	if !ok || ct2 != "NCX 第一章" {
		t.Errorf("ch1 segment epub_chapter_title = %v, want %q", ct2, "NCX第一章")
	}
}

// testNoNCXFallsBackToXHTMLTitle 验证没有 NCX 文件时回退到 XHTML <title>。
func testNoNCXFallsBackToXHTMLTitle(t *testing.T) {
	// 使用普通 EPUB（无 NCX）
	data := createTestEPUB(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>内容</p>", id: "ch1", title: "第一章 开始"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(doc.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(doc.Segments))
	}

	// 无 NCX 时应回退到 XHTML <title>
	ct, ok := doc.Segments[0].Meta["epub_chapter_title"].(string)
	if !ok || ct != "第一章 开始" {
		t.Errorf("epub_chapter_title = %v, want %q", ct, "第一章 开始")
	}
}

// testNCXWithAnchorInSrc 验证 NCX content src 中的锚点（#fragment）被正确处理。
func testNCXWithAnchorInSrc(t *testing.T) {
	data := createTestEPUBWithNCX(t, []testChapter{
		{filename: "OEBPS/ch1.xhtml", content: "<p>内容</p>", id: "ch1", title: "XHTML 标题"},
	}, []struct{ Src, Label string }{
		{"ch1.xhtml#section1", "带锚点的章节"},
	})

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(doc.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(doc.Segments))
	}

	// 应该能正确匹配（去掉 #section1 后匹配 ch1.xhtml）
	ct, ok := doc.Segments[0].Meta["epub_chapter_title"].(string)
	if !ok || ct != "带锚点的章节" {
		t.Errorf("epub_chapter_title = %v, want %q", ct, "带锚点的章节")
	}
}

// TestIsTOCFile 验证 isTOCFile 函数的正确性。
func TestIsTOCFile(t *testing.T) {
	tests := []struct {
		filename string
		expected bool
	}{
		{"OEBPS/p-toc-001.xhtml", true},
		{"OEBPS/toc.xhtml", true},
		{"OEBPS/TOC.xhtml", true},
		{"OEBPS/my-toc-file.xhtml", true},
		{"OEBPS/chapter1.xhtml", false},
		{"OEBPS/nav.xhtml", false},
		{"OEBPS/content.opf", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := isTOCFile(tt.filename)
			if got != tt.expected {
				t.Errorf("isTOCFile(%q) = %v, want %v", tt.filename, got, tt.expected)
			}
		})
	}
}

// TestExtractXHTMLTOCTitles 验证 extractXHTMLTOCTitles 函数的正确性。
func TestExtractXHTMLTOCTitles(t *testing.T) {
	tests := []struct {
		name     string
		xhtml    string
		tocHref  string
		expected map[string]string
	}{
		{
			name: "标准 XHTML 目录",
			xhtml: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>目次</title></head>
<body>
<nav epub:type="toc" xmlns:epub="http://www.idpf.org/2007/ops">
<h1>目次</h1>
<p><a href="p-003.xhtml#toc-001">プロローグ</a></p>
<p><a href="p-004.xhtml#toc-002">一章 一年次の春に</a></p>
<p><a href="p-005.xhtml#toc-003">二章 獅子聖庁の長官</a></p>
</nav>
</body>
</html>`,
			tocHref: "OEBPS/p-toc-001.xhtml",
			expected: map[string]string{
				"OEBPS/p-003.xhtml": "プロローグ",
				"OEBPS/p-004.xhtml": "一章 一年次の春に",
				"OEBPS/p-005.xhtml": "二章 獅子聖庁の長官",
			},
		},
		{
			name: "无 nav 标签的目录",
			xhtml: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>TOC</title></head>
<body>
<p><a href="chapter1.xhtml">Chapter 1</a></p>
<p><a href="chapter2.xhtml">Chapter 2</a></p>
</body>
</html>`,
			tocHref: "OEBPS/toc.xhtml",
			expected: map[string]string{
				"OEBPS/chapter1.xhtml": "Chapter 1",
				"OEBPS/chapter2.xhtml": "Chapter 2",
			},
		},
		{
			name: "带子目录路径的 TOC",
			xhtml: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body>
<p><a href="../Text/ch1.xhtml">第一章</a></p>
</body>
</html>`,
			tocHref: "OEBPS/nav/toc.xhtml",
			expected: map[string]string{
				"OEBPS/Text/ch1.xhtml": "第一章",
			},
		},
		{
			name: "忽略外部链接和纯锚点",
			xhtml: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body>
<p><a href="http://example.com">External</a></p>
<p><a href="#local-anchor">Anchor</a></p>
<p><a href="real.xhtml">Real Chapter</a></p>
</body>
</html>`,
			tocHref: "OEBPS/toc.xhtml",
			expected: map[string]string{
				"OEBPS/real.xhtml": "Real Chapter",
			},
		},
		{
			name:     "空内容",
			xhtml:    `<html><body></body></html>`,
			tocHref:  "OEBPS/toc.xhtml",
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractXHTMLTOCTitles([]byte(tt.xhtml), tt.tocHref)
			if len(got) != len(tt.expected) {
				t.Errorf("extractXHTMLTOCTitles returned %d entries, want %d\ngot: %v\nwant: %v",
					len(got), len(tt.expected), got, tt.expected)
				return
			}
			for k, wantTitle := range tt.expected {
				gotTitle, ok := got[k]
				if !ok {
					t.Errorf("missing key %q in result", k)
					continue
				}
				if gotTitle != wantTitle {
					t.Errorf("titles[%q] = %q, want %q", k, gotTitle, wantTitle)
				}
			}
		})
	}
}

// TestXHTMLTOCIntegration 端到端测试：验证 XHTML 目录文件中的章节标题
// 能正确传递给其他章节文件。
func TestXHTMLTOCIntegration(t *testing.T) {
	// 构建一个 EPUB，其中包含一个 XHTML TOC 文件（p-toc-001.xhtml），
	// TOC 文件中的 <a> 链接指向其他章节文件。
	// 验证：TOC 文件本身应获得 "Contents" 标题，
	// 其他章节文件应从 TOC 的 <a> 链接中提取标题。
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	// mimetype
	mw, _ := w.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	io.WriteString(mw, "application/epub+zip")

	// container.xml
	cw, _ := w.Create("META-INF/container.xml")
	io.WriteString(cw, `<?xml version="1.0" encoding="UTF-8"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`)

	// content.opf — TOC 在 spine 的第一个位置
	ow, _ := w.Create("OEBPS/content.opf")
	io.WriteString(ow, `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Test Book</dc:title>
    <dc:identifier id="uid">urn:uuid:12345</dc:identifier>
  </metadata>
  <manifest>
    <item id="toc" href="p-toc-001.xhtml" media-type="application/xhtml+xml"/>
    <item id="ch1" href="p-003.xhtml" media-type="application/xhtml+xml"/>
    <item id="ch2" href="p-004.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="toc"/>
    <itemref idref="ch1"/>
    <itemref idref="ch2"/>
  </spine>
</package>`)

	// TOC 文件 — 包含指向章节的 <a> 链接
	tocW, _ := w.Create("OEBPS/p-toc-001.xhtml")
	io.WriteString(tocW, `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>目次</title></head>
<body>
<nav epub:type="toc">
<h1>目次</h1>
<p><a href="p-003.xhtml#toc-001">プロローグ</a></p>
<p><a href="p-004.xhtml#toc-002">一章 一年次の春に</a></p>
</nav>
</body>
</html>`)

	// 章节文件 1 — 没有 h1 标题
	ch1W, _ := w.Create("OEBPS/p-003.xhtml")
	io.WriteString(ch1W, `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body>
<p>本文内容</p>
</body>
</html>`)

	// 章节文件 2 — 也没有 h1 标题
	ch2W, _ := w.Create("OEBPS/p-004.xhtml")
	io.WriteString(ch2W, `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body>
<p>第二章内容</p>
</body>
</html>`)

	w.Close()

	p := newParser()
	doc, err := p.Parse(context.Background(), bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// 应该有 3 个 segment：TOC 的 h1 + 两个章节的 p
	if len(doc.Segments) < 3 {
		t.Fatalf("expected at least 3 segments, got %d", len(doc.Segments))
	}

	// 验证 TOC 文件的章节标题是 "Contents"
	tocSegFound := false
	for _, seg := range doc.Segments {
		if ep, ok := seg.Meta["epub_file"].(string); ok && ep == "OEBPS/p-toc-001.xhtml" {
			ct, ok := seg.Meta["epub_chapter_title"].(string)
			if !ok || ct != "Contents" {
				t.Errorf("TOC segment epub_chapter_title = %v, want %q", ct, "Contents")
			}
			tocSegFound = true
			break
		}
	}
	if !tocSegFound {
		t.Error("no segment found for TOC file")
	}

	// 验证章节文件从 XHTML TOC 中提取到正确标题
	ch1Title := ""
	ch2Title := ""
	for _, seg := range doc.Segments {
		ep, _ := seg.Meta["epub_file"].(string)
		ct, _ := seg.Meta["epub_chapter_title"].(string)
		switch ep {
		case "OEBPS/p-003.xhtml":
			ch1Title = ct
		case "OEBPS/p-004.xhtml":
			ch2Title = ct
		}
	}

	if ch1Title != "プロローグ" {
		t.Errorf("ch1 epub_chapter_title = %q, want %q", ch1Title, "プロローグ")
	}
	if ch2Title != "一章 一年次の春に" {
		t.Errorf("ch2 epub_chapter_title = %q, want %q", ch2Title, "一章　一年次の春に")
	}
}
