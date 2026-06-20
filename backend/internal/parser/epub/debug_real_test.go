package epub

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

// // TestDebugRealAXHTML 使用真实的 A.xhtml 内容执行 Parse → translate → Render 端到端测试。
// func TestDebugRealAXHTML(t *testing.T) {
// 	// 读取 A.xhtml
// 	data, err := os.ReadFile("../../A.xhtml")
// 	if err != nil {
// 		t.Fatalf("读取 A.xhtml 失败: %v", err)
// 	}

// 	xhtml := string(data)

// 	// 1. 提取 segments
// 	segs := extractSegments(t, xhtml, "OEBPS/Text/Chapter0001.xhtml")
// 	t.Logf("提取到 %d 个 segments", len(segs))
// 	for i, seg := range segs {
// 		ep, _ := seg.Meta["element_path"].(string)
// 		t.Logf("  seg[%d]: path=%-35s source=%q", i, ep, truncate(seg.Source, 40))
// 	}

// 	// 2. 模拟翻译：为每个 segment 设置 Target
// 	for i := range segs {
// 		if segs[i].Source == "<br></br>" {
// 			segs[i].Target = segs[i].Source // 保持 <br> 不变
// 		} else {
// 			segs[i].Target = fmt.Sprintf("翻译_%d", i)
// 		}
// 	}

// 	// 3. 创建 ZIP 并调用 renderXHTML
// 	_, f := createTestZipFile(t, "OEBPS/Text/Chapter0001.xhtml", xhtml)
// 	rendered, err := renderXHTML(f, segs)
// 	if err != nil {
// 		t.Fatalf("renderXHTML error: %v", err)
// 	}
// 	output := string(rendered)

// 	// 4. 验证替换
// 	successCount := 0
// 	for i, seg := range segs {
// 		expected := seg.Target
// 		if expected == "" || expected == segs[i].Source {
// 			continue
// 		}
// 		if strings.Contains(output, expected) {
// 			successCount++
// 		} else {
// 			ep, _ := seg.Meta["element_path"].(string)
// 			t.Errorf("seg[%d] 译文 %q 未出现在输出中 (path=%q)", i, expected, ep)
// 		}
// 	}
// 	t.Logf("成功替换 %d/%d 个 segments", successCount, len(segs))

// 	// 5. 检查是否还有原文
// 	originalTexts := []string{
// 		"名前の呼び方と",
// 		"あの日、レンとリシアが",
// 		"父のロイが",
// 	}
// 	for _, txt := range originalTexts {
// 		if strings.Contains(output, txt) {
// 			t.Logf("⚠ 原文 %q 仍在输出中（可能是因为有多个段落包含类似文本）", txt)
// 		}
// 	}
// }

// // TestDebugRealE2E 使用完整 EPUB 流程测试。
// func TestDebugRealE2E(t *testing.T) {
// 	// 读取 A.xhtml 并构建一个最小 EPUB
// 	xhtmlData, err := os.ReadFile("../../A.xhtml")
// 	if err != nil {
// 		t.Fatalf("读取 A.xhtml 失败: %v", err)
// 	}

// 	epubData := createTestEPUB(t, []testChapter{
// 		{filename: "OEBPS/Text/Chapter0001.xhtml", content: extractBody(string(xhtmlData)), id: "ch1"},
// 	})

// 	p := newParser()

// 	// Parse
// 	doc, err := p.Parse(context.Background(), bytes.NewReader(epubData))
// 	if err != nil {
// 		t.Fatalf("Parse error: %v", err)
// 	}
// 	t.Logf("Parse: %d segments", len(doc.Segments))

// 	// 模拟翻译
// 	for i := range doc.Segments {
// 		ep, _ := doc.Segments[i].Meta["element_path"].(string)
// 		if doc.Segments[i].Source == "<br></br>" {
// 			doc.Segments[i].Target = doc.Segments[i].Source
// 		} else {
// 			doc.Segments[i].Target = fmt.Sprintf("翻译_%d", i)
// 		}
// 		t.Logf("  seg[%d]: path=%-35s target=%q", i, ep, doc.Segments[i].Target)
// 	}

// 	// Render
// 	var rendered bytes.Buffer
// 	err = p.Render(context.Background(), doc, bytes.NewReader(epubData), &rendered)
// 	if err != nil {
// 		t.Fatalf("Render error: %v", err)
// 	}

// 	// Re-parse
// 	doc2, err := p.Parse(context.Background(), bytes.NewReader(rendered.Bytes()))
// 	if err != nil {
// 		t.Fatalf("Re-parse error: %v", err)
// 	}

// 	// 验证
// 	t.Logf("Re-parse: %d segments", len(doc2.Segments))
// 	replaced := 0
// 	notReplaced := 0
// 	for i, seg := range doc2.Segments {
// 		expected := fmt.Sprintf("翻译_%d", i)
// 		if seg.Source == "<br></br>" {
// 			continue // 跳过 <br> 段
// 		}
// 		if strings.Contains(seg.Source, expected) {
// 			replaced++
// 		} else {
// 			notReplaced++
// 			if notReplaced <= 5 {
// 				t.Errorf("seg[%d] 未被替换: source=%q, expected to contain %q", i, truncate(seg.Source, 60), expected)
// 			}
// 		}
// 	}
// 	t.Logf("替换结果: %d 成功, %d 失败 (共 %d 个非 br 段)", replaced, notReplaced, replaced+notReplaced)
// 	if notReplaced > 0 {
// 		t.Errorf("有 %d 个段落未被替换！", notReplaced)
// 	}
// }

// TestDebugProcessXMLTokensDirectly 直接测试 processXMLTokens 的替换逻辑。
func TestDebugProcessXMLTokensDirectly(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="ja" class="vrtl">
 <head>
  <meta charset="UTF-8" />
  <title>テスト</title>
  <link rel="stylesheet" type="text/css" href="../style/book-style.css" />
 </head>
 <body class="p-text">
  <div class="main">
   <p><br /></p>
   <p>テスト段落</p>
   <p>別の段落</p>
  </div>
 </body>
</html>`

	raw := []byte(xhtml)

	// 构建 pathReplacements
	pathReplacements := map[string]string{
		"html/body/div/p":    "翻译_0",
		"html/body/div/p[1]": "翻译_1",
		"html/body/div/p[2]": "翻译_2",
	}

	// 调用 processXMLTokens
	var buf bytes.Buffer
	decoder := newDecoder(raw)
	err := processXMLTokens(raw, decoder, pathReplacements, &buf)
	if err != nil {
		t.Fatalf("processXMLTokens error: %v", err)
	}

	output := buf.String()
	t.Logf("Output:\n%s", output)

	for path, target := range pathReplacements {
		if !strings.Contains(output, target) {
			t.Errorf("译文 %q (path=%q) 未出现在输出中", target, path)
		}
	}

	// 验证原文被替换
	if strings.Contains(output, "テスト段落") {
		t.Error("原文 'テスト段落' 未被替换")
	}
	if strings.Contains(output, "別の段落") {
		t.Error("原文 '別の段落' 未被替换")
	}
}

// TestDebugProcessXMLTokensWithLogging 带详细日志的 processXMLTokens 测试。
func TestDebugProcessXMLTokensWithLogging(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body>
<div>
<p>テスト</p>
</div>
</body>
</html>`

	raw := []byte(xhtml)
	pathReplacements := map[string]string{
		"html/body/div/p": "翻译",
	}

	// 手动模拟 processXMLTokens 的逻辑，带详细日志
	decoder := newDecoder(raw)
	pt := newPathTracker()
	var (
		replacing     bool
		replaceTarget string
		replaceDepth  int
	)
	prevOffset := int64(0)

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		currentOffset := decoder.InputOffset()
		tokenBytes := raw[prevOffset:currentOffset]
		prevOffset = currentOffset

		switch tt := tok.(type) {
		case xml.StartElement:
			tag := tt.Name.Local
			t.Logf("Start: %-10s offset=%d..%d bytes=%q replacing=%v depth=%d",
				tag, prevOffset, currentOffset, string(tokenBytes), replacing, replaceDepth)

			if replacing {
				replaceDepth++
				t.Logf("  → skip (depth now %d)", replaceDepth)
				continue
			}

			pt.push(tag)
			currentPath := pt.path()
			t.Logf("  → path=%q", currentPath)

			if target, ok := pathReplacements[currentPath]; ok {
				replacing = true
				replaceTarget = target
				replaceDepth = 1
				t.Logf("  → MATCH! entering replace mode, target=%q", target)
			}

		case xml.EndElement:
			tag := tt.Name.Local
			t.Logf("End:   %-10s offset=%d..%d bytes=%q replacing=%v depth=%d",
				tag, prevOffset, currentOffset, string(tokenBytes), replacing, replaceDepth)

			if replacing {
				replaceDepth--
				if replaceDepth <= 0 {
					t.Logf("  → EXIT replace mode, writing target=%q + closing tag", replaceTarget)
					replacing = false
					replaceTarget = ""
					replaceDepth = 0
					pt.pop()
				}
				continue
			}
			pt.pop()

		default:
			t.Logf("Other: %T offset=%d..%d bytes=%q replacing=%v",
				tok, prevOffset, currentOffset, string(tokenBytes), replacing)
		}
	}
}

// extractBody 从完整 XHTML 中提取 body 内容。
func extractBody(xhtml string) string {
	start := strings.Index(xhtml, "<body")
	if start < 0 {
		return xhtml
	}
	start = strings.Index(xhtml[start:], ">")
	if start < 0 {
		return xhtml
	}
	start += start + 1
	end := strings.LastIndex(xhtml, "</body>")
	if end < 0 {
		return xhtml[start:]
	}
	return xhtml[start:end]
}
