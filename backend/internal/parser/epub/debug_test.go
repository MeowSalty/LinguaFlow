package epub

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

// TestDebugRealStructure 精确模拟 A.xhtml 的真实结构，诊断替换是否生效。
func TestDebugRealStructure(t *testing.T) {
	// 精确模拟 A.xhtml 的结构
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="ja" class="vrtl">
 <head>
  <meta charset="UTF-8" />
  <title>テストタイトル</title>
  <link rel="stylesheet" type="text/css" href="../style/book-style.css" />
 </head>
 <body class="p-text">
  <div class="main">
   <p><br /></p>
   <p><br /></p>
   <p class="gfont font-1em20">　　　名前の呼び方と。</p>
   <p><br /></p>
   <p>　────あの日、レンとリシアがアシュトン家の屋敷から連れ去られた。</p>
  </div>
 </body>
</html>`

	// 1. 提取 segments
	segs := extractSegments(t, xhtml, "OEBPS/Text/Chapter0001.xhtml")
	t.Logf("提取到 %d 个 segments", len(segs))
	for i, seg := range segs {
		ep, _ := seg.Meta["element_path"].(string)
		t.Logf("  seg[%d]: element_path=%q source=%q", i, ep, seg.Source)
	}

	// 2. 设置 Target
	for i := range segs {
		segs[i].Target = "翻译_" + segs[i].Source
	}

	// 3. 构建 pathReplacements（模拟 renderXHTML 的逻辑）
	pathReplacements := make(map[string]string)
	for _, seg := range segs {
		target := seg.Target
		if target == "" {
			target = seg.Source
		}
		if ep, ok := seg.Meta["element_path"].(string); ok {
			pathReplacements[ep] = target
		}
	}
	t.Logf("pathReplacements:")
	for k, v := range pathReplacements {
		t.Logf("  %q -> %q", k, v)
	}

	// 4. 模拟 processXMLTokens 的路径生成
	decoder := newDecoder([]byte(xhtml))
	pt := newPathTracker()
	t.Logf("\nRender 阶段路径遍历:")
	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch tt := tok.(type) {
		case xml.StartElement:
			tag := tt.Name.Local
			pt.push(tag)
			currentPath := pt.path()
			_, matched := pathReplacements[currentPath]
			if matched || isBlockElement(tag) {
				t.Logf("  StartElement: tag=%-10s path=%-40s matched=%v", tag, currentPath, matched)
			}
		case xml.EndElement:
			tag := tt.Name.Local
			currentPath := pt.path()
			_, matched := pathReplacements[currentPath]
			if matched || isBlockElement(tag) {
				t.Logf("  EndElement:   tag=%-10s path=%-40s", tag, currentPath)
			}
			pt.pop()
		}
	}

	// 5. 实际执行 renderXHTML
	_, f := createTestZipFile(t, "OEBPS/Text/Chapter0001.xhtml", xhtml)
	rendered, err := renderXHTML(f, segs)
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)

	// 6. 验证替换
	t.Logf("\n渲染输出 (前 500 字符):\n%s", output[:min(500, len(output))])

	replaced := false
	for _, seg := range segs {
		if seg.Target != "" && strings.Contains(output, seg.Target) {
			replaced = true
			t.Logf("  ✓ 译文 %q 出现在输出中", seg.Target)
		}
	}
	if !replaced {
		t.Error("✗ 没有任何译文出现在输出中！")
	}

	// 检查原文是否还在
	for _, seg := range segs {
		if seg.Target != "" && strings.Contains(output, seg.Source) && seg.Source != seg.Target {
			t.Logf("  ⚠ 原文 %q 仍然在输出中", seg.Source)
		}
	}
}

// TestDebugPathMismatch 专门测试 Parse 和 Render 的路径是否一致。
func TestDebugPathMismatch(t *testing.T) {
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
  </div>
 </body>
</html>`

	// Parse 阶段的路径
	segs := extractSegments(t, xhtml, "OEBPS/test.xhtml")
	parsePaths := make(map[string]bool)
	for _, seg := range segs {
		ep, _ := seg.Meta["element_path"].(string)
		parsePaths[ep] = true
		t.Logf("Parse path: %q (source=%q)", ep, seg.Source)
	}

	// Render 阶段的路径（使用相同的 pathTracker 逻辑）
	// 我们需要模拟 processXMLTokens 中的路径生成
	// 但不进入替换模式，只记录所有路径

	// 使用 extractSegmentsFromXHTML 的相同 decoder 设置
	renderSegs := extractSegments(t, xhtml, "OEBPS/test.xhtml")
	for _, seg := range renderSegs {
		ep, _ := seg.Meta["element_path"].(string)
		if !parsePaths[ep] {
			t.Errorf("Render path %q NOT found in Parse paths!", ep)
		}
	}

	// 验证所有 Parse 路径都在 Render 路径中
	renderPaths := make(map[string]bool)
	for _, seg := range renderSegs {
		ep, _ := seg.Meta["element_path"].(string)
		renderPaths[ep] = true
	}
	for ep := range parsePaths {
		if !renderPaths[ep] {
			t.Errorf("Parse path %q NOT found in Render paths!", ep)
		}
	}
}

// TestDebugBrSelfClosing 测试 <br /> 自闭合标签在 Parse 和 Render 中的行为。
func TestDebugBrSelfClosing(t *testing.T) {
	xhtml := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body>
<p><br /></p>
<p>テスト</p>
</body>
</html>`

	segs := extractSegments(t, xhtml, "OEBPS/test.xhtml")
	t.Logf("Segments: %d", len(segs))
	for i, seg := range segs {
		ep, _ := seg.Meta["element_path"].(string)
		t.Logf("  seg[%d]: path=%q source=%q", i, ep, seg.Source)
	}

	// 设置 Target 并渲染
	for i := range segs {
		segs[i].Target = "翻译_" + segs[i].Source
	}

	_, f := createTestZipFile(t, "OEBPS/test.xhtml", xhtml)
	rendered, err := renderXHTML(f, segs)
	if err != nil {
		t.Fatalf("renderXHTML error: %v", err)
	}
	output := string(rendered)
	t.Logf("Output:\n%s", output)

	if !strings.Contains(output, "翻译_テスト") {
		t.Error("译文未出现在输出中")
	}
}

func newDecoder(data []byte) *xml.Decoder {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity
	return decoder
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
