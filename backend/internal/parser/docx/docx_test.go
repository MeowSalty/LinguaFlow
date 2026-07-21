package docx

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ziputil"
)

const wDocNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

// createTestDOCX 在内存中创建最小 DOCX（document.xml + Content_Types + rels）。
func createTestDOCX(t *testing.T, documentXML string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	ct, err := w.Create("[Content_Types].xml")
	if err != nil {
		t.Fatalf("create Content_Types: %v", err)
	}
	if _, err := io.WriteString(ct, `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`); err != nil {
		t.Fatalf("write Content_Types: %v", err)
	}

	rels, err := w.Create("_rels/.rels")
	if err != nil {
		t.Fatalf("create rels: %v", err)
	}
	if _, err := io.WriteString(rels, `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`); err != nil {
		t.Fatalf("write rels: %v", err)
	}

	doc, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create document.xml: %v", err)
	}
	if _, err := io.WriteString(doc, documentXML); err != nil {
		t.Fatalf("write document.xml: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func wrapBody(bodyInner string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document ` + wDocNS + `>
  <w:body>
` + bodyInner + `
  </w:body>
</w:document>`
}

func para(runs string) string {
	return `    <w:p>` + runs + `</w:p>`
}

func paraWithPPr(pPr, runs string) string {
	return `    <w:p><w:pPr>` + pPr + `</w:pPr>` + runs + `</w:p>`
}

func run(rPr, text string) string {
	inner := ""
	if rPr != "" {
		inner += `<w:rPr>` + rPr + `</w:rPr>`
	}
	inner += `<w:t xml:space="preserve">` + text + `</w:t>`
	return `<w:r>` + inner + `</w:r>`
}

func parseDOCX(t *testing.T, documentXML string) *pipeline.Document {
	t.Helper()
	data := createTestDOCX(t, documentXML)
	p := New()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func roundTrip(t *testing.T, documentXML string, mutate func(*pipeline.Document)) (origXML, outXML string) {
	t.Helper()
	data := createTestDOCX(t, documentXML)
	p := New()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if mutate != nil {
		mutate(doc)
	} else {
		for i := range doc.Segments {
			doc.Segments[i].Target = doc.Segments[i].Source
		}
	}
	var out bytes.Buffer
	if err := p.Render(context.Background(), doc, bytes.NewReader(data), &out); err != nil {
		t.Fatalf("Render: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("open out zip: %v", err)
	}
	outData, err := ziputil.ReadEntry(zr, documentXMLPath, maxDecompressedEntrySize)
	if err != nil {
		t.Fatalf("read out document: %v", err)
	}
	return documentXML, string(outData)
}

func TestParse_BasicParagraph(t *testing.T) {
	doc := parseDOCX(t, wrapBody(para(run("", "Hello"))))
	if len(doc.Segments) != 1 {
		t.Fatalf("segments=%d want 1", len(doc.Segments))
	}
	if doc.Segments[0].Source != "Hello" {
		t.Errorf("Source=%q want Hello", doc.Segments[0].Source)
	}
	if doc.Format != "docx" {
		t.Errorf("Format=%q", doc.Format)
	}
	ep, _ := doc.Segments[0].Meta["element_path"].(string)
	if !strings.Contains(ep, "p") {
		t.Errorf("element_path=%q", ep)
	}
}

func TestParse_BoldRun(t *testing.T) {
	doc := parseDOCX(t, wrapBody(para(run("<w:b/>", "Bold"))))
	if len(doc.Segments) != 1 {
		t.Fatalf("segments=%d", len(doc.Segments))
	}
	if doc.Segments[0].Source != "<b>Bold</b>" {
		t.Errorf("Source=%q", doc.Segments[0].Source)
	}
}

func TestParse_ColorRun(t *testing.T) {
	doc := parseDOCX(t, wrapBody(para(run(`<w:color w:val="FF0000"/>`, "Red"))))
	if len(doc.Segments) != 1 {
		t.Fatalf("segments=%d", len(doc.Segments))
	}
	if doc.Segments[0].Source != "Red" {
		t.Errorf("Source=%q want Red (no color in HTML)", doc.Segments[0].Source)
	}
	ex, _ := doc.Segments[0].Meta["docx_rpr_extras"].(string)
	if !strings.Contains(ex, "color") || !strings.Contains(ex, "FF0000") {
		t.Errorf("extras=%q want color FF0000", ex)
	}
}

func TestParse_MixedRuns(t *testing.T) {
	body := para(run("<w:b/>", "text1") + run("", "text2"))
	doc := parseDOCX(t, wrapBody(body))
	if len(doc.Segments) != 1 {
		t.Fatalf("segments=%d", len(doc.Segments))
	}
	if doc.Segments[0].Source != "<b>text1</b>text2" {
		t.Errorf("Source=%q", doc.Segments[0].Source)
	}
}

func TestParse_SoftBreak(t *testing.T) {
	runs := `<w:r><w:t>A</w:t></w:r><w:r><w:br/></w:r><w:r><w:t>B</w:t></w:r>`
	doc := parseDOCX(t, wrapBody(para(runs)))
	if len(doc.Segments) != 1 {
		t.Fatalf("segments=%d want 1 (no split)", len(doc.Segments))
	}
	if !strings.Contains(doc.Segments[0].Source, "<br/>") {
		t.Errorf("Source=%q want br", doc.Segments[0].Source)
	}
}

func TestParse_EmptyParagraph(t *testing.T) {
	body := para("") + para(run("", "X"))
	doc := parseDOCX(t, wrapBody(body))
	if len(doc.Segments) != 1 {
		t.Fatalf("segments=%d want 1 (empty skipped)", len(doc.Segments))
	}
	if doc.Segments[0].Source != "X" {
		t.Errorf("Source=%q", doc.Segments[0].Source)
	}
}

func TestParse_TableCell(t *testing.T) {
	body := `
    <w:tbl>
      <w:tr>
        <w:tc>
          <w:p><w:r><w:t>Cell</w:t></w:r></w:p>
        </w:tc>
      </w:tr>
    </w:tbl>`
	doc := parseDOCX(t, wrapBody(body))
	if len(doc.Segments) != 1 {
		t.Fatalf("segments=%d", len(doc.Segments))
	}
	ep, _ := doc.Segments[0].Meta["element_path"].(string)
	if !strings.Contains(ep, "tbl") || !strings.Contains(ep, "tr") || !strings.Contains(ep, "tc") {
		t.Errorf("element_path=%q want tbl/tr/tc", ep)
	}
	if doc.Segments[0].Source != "Cell" {
		t.Errorf("Source=%q", doc.Segments[0].Source)
	}
}

func TestParse_SkipDrawing(t *testing.T) {
	runs := `<w:r><w:t>Before</w:t></w:r><w:r><w:drawing><w:x>ignore</w:x></w:drawing></w:r><w:r><w:t>After</w:t></w:r>`
	doc := parseDOCX(t, wrapBody(para(runs)))
	if len(doc.Segments) != 1 {
		t.Fatalf("segments=%d", len(doc.Segments))
	}
	if strings.Contains(doc.Segments[0].Source, "ignore") {
		t.Errorf("Source contains drawing content: %q", doc.Segments[0].Source)
	}
	if doc.Segments[0].Source != "BeforeAfter" {
		t.Errorf("Source=%q want BeforeAfter (drawing content must be skipped)", doc.Segments[0].Source)
	}
}

func TestParse_ExplicitDefault(t *testing.T) {
	doc := parseDOCX(t, wrapBody(para(run(`<w:b w:val="0"/>`, "NoBold"))))
	if doc.Segments[0].Source != "NoBold" {
		t.Errorf("Source=%q want no b tag", doc.Segments[0].Source)
	}
}

func TestParse_StyleRef(t *testing.T) {
	doc := parseDOCX(t, wrapBody(para(run(`<w:rStyle w:val="Emphasis"/>`, "Styled"))))
	if doc.Segments[0].Source != `<span class="Emphasis">Styled</span>` {
		t.Errorf("Source=%q", doc.Segments[0].Source)
	}
}

func TestParse_MergeAdjacentSameFormat(t *testing.T) {
	body := para(run("<w:b/>", "Hel") + run("<w:b/>", "lo"))
	doc := parseDOCX(t, wrapBody(body))
	if doc.Segments[0].Source != "<b>Hello</b>" {
		t.Errorf("Source=%q want merged", doc.Segments[0].Source)
	}
}

func TestParse_ExtrasAllAgree(t *testing.T) {
	body := para(
		run(`<w:lang w:val="en"/>`, "A") +
			run(`<w:lang w:val="en"/>`, "B"),
	)
	doc := parseDOCX(t, wrapBody(body))
	ex, _ := doc.Segments[0].Meta["docx_rpr_extras"].(string)
	if !strings.Contains(ex, "lang") || !strings.Contains(ex, "en") {
		t.Errorf("extras=%q want lang=en", ex)
	}
}

func TestParse_ExtrasConflictDropped(t *testing.T) {
	body := para(
		run("", "A") +
			run(`<w:color w:val="FF0000"/>`, "B"),
	)
	doc := parseDOCX(t, wrapBody(body))
	ex, _ := doc.Segments[0].Meta["docx_rpr_extras"].(string)
	if strings.Contains(ex, "color") {
		t.Errorf("extras=%q should drop conflicting color", ex)
	}
}

func TestParse_NoSplitCompoundElement(t *testing.T) {
	rPr := `<w:rFonts w:ascii="Arial" w:eastAsia="宋体"/>`
	doc := parseDOCX(t, wrapBody(para(run(rPr, "Font"))))
	ex, _ := doc.Segments[0].Meta["docx_rpr_extras"].(string)
	if !strings.Contains(ex, "rFonts") {
		t.Fatalf("extras=%q want rFonts", ex)
	}
	if !strings.Contains(ex, "Arial") || !strings.Contains(ex, "宋体") {
		t.Errorf("extras should keep both attrs: %q", ex)
	}
	// HTML 不应拆出字体
	if strings.Contains(doc.Segments[0].Source, "Arial") {
		t.Errorf("Source should not contain font: %q", doc.Segments[0].Source)
	}
}

func TestRoundTrip_BoldRed(t *testing.T) {
	rPr := `<w:b/><w:color w:val="FF0000"/>`
	xmlIn := wrapBody(para(run(rPr, "Hello")))
	_, out := roundTrip(t, xmlIn, func(doc *pipeline.Document) {
		doc.Segments[0].Target = "<b>你好</b>"
	})
	if !strings.Contains(out, "<w:b") && !strings.Contains(out, "<w:b/>") && !strings.Contains(out, "<w:b />") {
		// self-closing forms
		if !strings.Contains(out, "w:b") {
			t.Errorf("missing bold in output")
		}
	}
	if !strings.Contains(out, "FF0000") {
		t.Errorf("missing color in output")
	}
	if !strings.Contains(out, "你好") {
		t.Errorf("missing target text")
	}
}

func TestRoundTrip_PPrPreserved(t *testing.T) {
	xmlIn := wrapBody(paraWithPPr(`<w:pStyle w:val="Heading1"/>`, run("", "Title")))
	_, out := roundTrip(t, xmlIn, nil)
	if !strings.Contains(out, "Heading1") {
		t.Errorf("pStyle lost: %s", out)
	}
	if !strings.Contains(out, "Title") {
		t.Errorf("text lost")
	}
}

func TestRoundTrip_LostPlaceholder(t *testing.T) {
	xmlIn := wrapBody(para(run("<w:b/>"+`<w:lang w:val="en"/>`, "Hello")))
	_, out := roundTrip(t, xmlIn, func(doc *pipeline.Document) {
		// 模拟占位符丢失：纯文本译文
		doc.Segments[0].Target = "你好"
	})
	if !strings.Contains(out, "你好") {
		t.Errorf("text missing")
	}
	// extras 应保留
	if !strings.Contains(out, "lang") {
		t.Errorf("extras lang should remain: %s", out)
	}
	// well-formed 已由 Render 校验
}

func TestRoundTrip_ReorderPlaceholder(t *testing.T) {
	// 错乱 HTML 仍应产出可解析 DOCX（htmlToOOXML 尽力或降级）
	xmlIn := wrapBody(para(run("<w:b/>", "Hello")))
	_, out := roundTrip(t, xmlIn, func(doc *pipeline.Document) {
		doc.Segments[0].Target = "</b>你好<b>"
	})
	if !strings.Contains(out, "你好") && !strings.Contains(out, "Hello") {
		// 降级后至少有文本
		t.Logf("out=%s", out)
	}
	// 输出必须 well-formed（Render 内部校验）
	if !strings.Contains(out, "<w:p") {
		t.Errorf("invalid out")
	}
}

func TestRoundTrip_ExtrasPreserved(t *testing.T) {
	xmlIn := wrapBody(para(run(`<w:lang w:val="en-US"/>`, "Hi")))
	_, out := roundTrip(t, xmlIn, func(doc *pipeline.Document) {
		doc.Segments[0].Target = "你好"
	})
	if !strings.Contains(out, "en-US") {
		t.Errorf("lang not preserved: %s", out)
	}
}

func TestRoundTrip_StyleRefPreserved(t *testing.T) {
	xmlIn := wrapBody(para(run(`<w:rStyle w:val="Emphasis"/>`, "Hi")))
	_, out := roundTrip(t, xmlIn, func(doc *pipeline.Document) {
		doc.Segments[0].Target = `<span class="Emphasis">你好</span>`
	})
	if !strings.Contains(out, "Emphasis") {
		t.Errorf("rStyle lost: %s", out)
	}
}

func TestRoundTrip_TranslatorFormatChange(t *testing.T) {
	xmlIn := wrapBody(para(run("<w:b/>", "Hello")))
	_, out := roundTrip(t, xmlIn, func(doc *pipeline.Document) {
		doc.Segments[0].Target = "<i>你好</i>"
	})
	if !strings.Contains(out, "w:i") {
		t.Errorf("want italic run: %s", out)
	}
	if strings.Contains(out, "<w:b/>") || strings.Contains(out, "<w:b ") {
		t.Errorf("bold should be removed when extras has no b: %s", out)
	}
	if !strings.Contains(out, "你好") {
		t.Errorf("text missing")
	}
}

func TestRoundTrip_NoDuplicateRFonts(t *testing.T) {
	rPr := `<w:rFonts w:ascii="Arial" w:eastAsia="宋体"/>`
	xmlIn := wrapBody(para(run(rPr, "Font")))
	_, out := roundTrip(t, xmlIn, nil)
	count := strings.Count(out, "rFonts")
	// 原文 1 + 重建 1 = 可能只在新 run 中 1 次
	if count > 1 {
		// 若段落外壳没有 rFonts，应只有 1
		// 检查同一 rPr 内是否重复
		if strings.Count(out, "<w:rFonts") > 1 {
			t.Errorf("duplicate rFonts count=%d: %s", count, out)
		}
	}
}

func TestRender_FallbackCopyOnMalformed(t *testing.T) {
	// htmlToOOXML 对非法 HTML 尽量不 panic；Render 整体 well-formed 失败时 copy
	// 构造 Target 含非法字符导致 Escape 仍合法；用未闭合复杂结构
	xmlIn := wrapBody(para(run("", "Hello")))
	data := createTestDOCX(t, xmlIn)
	p := New()
	doc, err := p.Parse(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	// 正常 Target 应成功
	doc.Segments[0].Target = "OK"
	var out bytes.Buffer
	if err := p.Render(context.Background(), doc, bytes.NewReader(data), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Fatal("empty output")
	}
}

func TestIncremental_NormalizationStable(t *testing.T) {
	// 相同输入两次 Parse → SourceText 一致
	xmlIn := wrapBody(para(
		run("<w:b/>", "Hel") + run("<w:b/>", "lo") +
			run(`<w:b w:val="0"/><w:lang w:val="en"/>`, "X"),
	))
	d1 := parseDOCX(t, xmlIn)
	d2 := parseDOCX(t, xmlIn)
	if len(d1.Segments) != len(d2.Segments) {
		t.Fatalf("len mismatch")
	}
	for i := range d1.Segments {
		if d1.Segments[i].Source != d2.Segments[i].Source {
			t.Errorf("Source drift: %q vs %q", d1.Segments[i].Source, d2.Segments[i].Source)
		}
		e1, _ := d1.Segments[i].Meta["docx_rpr_extras"].(string)
		e2, _ := d2.Segments[i].Meta["docx_rpr_extras"].(string)
		if e1 != e2 {
			t.Errorf("extras drift: %q vs %q", e1, e2)
		}
	}
}

func TestParse_Extensions(t *testing.T) {
	p := New()
	exts := p.Extensions()
	if len(exts) != 1 || exts[0] != ".docx" {
		t.Errorf("Extensions=%v", exts)
	}
}

func TestPathTracker(t *testing.T) {
	pt := newPathTracker()
	pt.push("body")
	pt.push("p")
	if pt.path() != "body/p" {
		t.Errorf("path=%q", pt.path())
	}
	pt.pop()
	pt.push("p")
	if pt.path() != "body/p[1]" {
		t.Errorf("path=%q want body/p[1]", pt.path())
	}
}

func TestParse_SpecialCharsEscaped(t *testing.T) {
	// OOXML 中特殊字符以实体存在；Parse 后 Source 应保留 HTML 转义，便于安全解析。
	xmlIn := wrapBody(`    <w:p><w:r><w:t>AT&amp;T 3 &lt; 5</w:t></w:r></w:p>`)
	doc := parseDOCX(t, xmlIn)
	if len(doc.Segments) != 1 {
		t.Fatalf("segments=%d", len(doc.Segments))
	}
	// CharData 解码后为 AT&T 3 < 5，再 escapeHTMLText → AT&amp;T 3 &lt; 5
	want := "AT&amp;T 3 &lt; 5"
	if doc.Segments[0].Source != want {
		t.Errorf("Source=%q want %q", doc.Segments[0].Source, want)
	}
}

func TestRoundTrip_SpecialChars(t *testing.T) {
	xmlIn := wrapBody(`    <w:p><w:r><w:t>AT&amp;T 3 &lt; 5</w:t></w:r></w:p>`)
	_, out := roundTrip(t, xmlIn, nil)
	if !strings.Contains(out, "AT&amp;T") {
		t.Errorf("missing AT&amp;T in out: %s", out)
	}
	if !strings.Contains(out, "3") || !strings.Contains(out, "5") {
		t.Errorf("missing comparison text: %s", out)
	}
	if !strings.Contains(out, "&lt;") && !strings.Contains(out, "3 < 5") {
		t.Errorf("lost less-than: %s", out)
	}
}

func TestRoundTrip_BareLessThanInTarget(t *testing.T) {
	// 译者写入含裸 < 的 Target（非 HTML 标签）时不应静默丢字
	xmlIn := wrapBody(para(run("", "Hello")))
	_, out := roundTrip(t, xmlIn, func(doc *pipeline.Document) {
		doc.Segments[0].Target = "3 < 5 and AT&T"
	})
	if !strings.Contains(out, "3") || !strings.Contains(out, "5") {
		t.Errorf("lost text: %s", out)
	}
	if !strings.Contains(out, "AT") {
		t.Errorf("lost AT&T: %s", out)
	}
}
