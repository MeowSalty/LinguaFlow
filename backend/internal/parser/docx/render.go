package docx

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log/slog"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// renderDocumentXML 按 element_path 替换段落 runs，保留 <w:p> 外壳与 <w:pPr>。
func renderDocumentXML(raw []byte, segmentsByPath map[string][]pipeline.Segment) ([]byte, error) {
	pathReplacements := make(map[string]string)
	pathExtras := make(map[string]string)
	for pth, segs := range segmentsByPath {
		if len(segs) == 0 {
			continue
		}
		seg := segs[0]
		target := seg.Target
		if target == "" {
			target = seg.Source
		}
		pathReplacements[pth] = target
		if ex, ok := seg.Meta["docx_rpr_extras"].(string); ok {
			pathExtras[pth] = ex
		}
	}

	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Strict = false

	var buf bytes.Buffer
	pt := newPathTracker()

	var (
		inReplaceP    bool
		replacePath   string
		replaceDepth  int
		phase         int // 0 前置, 1 runs 区, 2 尾部
		wroteNewRuns  bool
		suppressDepth int
		inPPr         bool
		pPrDepth      int
	)

	prevOffset := int64(0)

	for {
		tok, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("docx: render xml token: %w", err)
		}
		currentOffset := decoder.InputOffset()
		tokenBytes := raw[prevOffset:currentOffset]
		prevOffset = currentOffset

		switch t := tok.(type) {
		case xml.StartElement:
			local := t.Name.Local

			if inReplaceP {
				replaceDepth++

				if suppressDepth > 0 {
					suppressDepth++
					continue
				}

				if inPPr {
					pPrDepth++
					buf.Write(tokenBytes)
					continue
				}

				if local == "pPr" && phase == 0 {
					inPPr = true
					pPrDepth = 1
					buf.Write(tokenBytes)
					continue
				}

				// 容器元素（hyperlink/smartTag/sdt）：保留开标签与属性（如 r:id），
				// 内部 run 由下方 r 分支替换。
				if isPreserveWrapper(local) {
					buf.Write(tokenBytes)
					continue
				}

				if local == "r" {
					if !wroteNewRuns {
						extras := pathExtras[replacePath]
						runsXML, err := htmlToOOXML(pathReplacements[replacePath], extras)
						if err != nil {
							slog.Debug("[docx:render] htmlToOOXML failed, pure text fallback",
								"path", replacePath, "error", err)
							runsXML = pureTextRun(stripAllTags(pathReplacements[replacePath]), extras)
						}
						buf.Write(runsXML)
						wroteNewRuns = true
						phase = 1
					}
					suppressDepth = 1
					continue
				}

				if wroteNewRuns {
					phase = 2
				}
				buf.Write(tokenBytes)
				continue
			}

			pt.push(local)
			currentPath := pt.path()

			if local == "p" && isWordNS(t.Name.Space) {
				if _, ok := pathReplacements[currentPath]; ok {
					inReplaceP = true
					replacePath = currentPath
					replaceDepth = 1
					phase = 0
					wroteNewRuns = false
					suppressDepth = 0
					inPPr = false
					buf.Write(tokenBytes)
					continue
				}
			}
			buf.Write(tokenBytes)

		case xml.EndElement:
			if inReplaceP {
				if suppressDepth > 0 {
					suppressDepth--
					replaceDepth--
					continue
				}

				if inPPr {
					pPrDepth--
					buf.Write(tokenBytes)
					if pPrDepth <= 0 {
						inPPr = false
					}
					replaceDepth--
					continue
				}

				if t.Name.Local == "p" && replaceDepth == 1 {
					if !wroteNewRuns {
						extras := pathExtras[replacePath]
						runsXML, err := htmlToOOXML(pathReplacements[replacePath], extras)
						if err != nil {
							runsXML = pureTextRun(stripAllTags(pathReplacements[replacePath]), extras)
						}
						buf.Write(runsXML)
						wroteNewRuns = true
					}
					buf.Write(tokenBytes)
					inReplaceP = false
					replacePath = ""
					replaceDepth = 0
					phase = 0
					pt.pop()
					continue
				}

				buf.Write(tokenBytes)
				replaceDepth--
				continue
			}

			buf.Write(tokenBytes)
			pt.pop()

		default:
			if inReplaceP {
				if suppressDepth > 0 {
					continue
				}
				if inPPr || phase == 0 || phase == 2 {
					buf.Write(tokenBytes)
				}
				continue
			}
			buf.Write(tokenBytes)
		}
	}

	verifier := xml.NewDecoder(bytes.NewReader(buf.Bytes()))
	for {
		_, err := verifier.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("docx: rendered document.xml is not well-formed: %w", err)
		}
	}

	return buf.Bytes(), nil
}

// htmlToOOXML 将语义 HTML 片段转为 <w:r> 序列。
// 解析失败或可见文本明显丢失时降级为纯文本 run。
func htmlToOOXML(htmlStr, extras string) ([]byte, error) {
	plain := stripAllTags(htmlStr)
	wrapped := "<root>" + htmlStr + "</root>"
	decoder := xml.NewDecoder(strings.NewReader(wrapped))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity

	type tagFrame struct {
		tag   string
		class string
	}
	var stack []tagFrame
	var out bytes.Buffer
	hasContent := false

	writeTextRun := func(text string) {
		if text == "" {
			return
		}
		hasContent = true
		var rPrParts []string
		var classVal string
		for _, f := range stack {
			if f.tag == "span" && f.class != "" {
				classVal = f.class
			}
		}
		if classVal != "" {
			if xmlFrag, ok := htmlTagToRPrXML("span", classVal); ok {
				rPrParts = append(rPrParts, xmlFrag)
			}
		}
		seen := map[string]bool{}
		order := []string{"b", "strong", "i", "em", "u", "s", "del", "strike", "sup", "sub"}
		present := map[string]bool{}
		for _, f := range stack {
			present[f.tag] = true
		}
		for _, tag := range order {
			if !present[tag] || seen[tag] {
				continue
			}
			if xmlFrag, ok := htmlTagToRPrXML(tag, ""); ok {
				rPrParts = append(rPrParts, xmlFrag)
				seen[tag] = true
				if tag == "strong" {
					seen["b"] = true
				}
				if tag == "b" {
					seen["strong"] = true
				}
				if tag == "em" {
					seen["i"] = true
				}
				if tag == "i" {
					seen["em"] = true
				}
			}
		}
		if extras != "" {
			rPrParts = append(rPrParts, extras)
		}

		out.WriteString("<w:r>")
		if len(rPrParts) > 0 {
			out.WriteString("<w:rPr>")
			for _, p := range rPrParts {
				out.WriteString(p)
			}
			out.WriteString("</w:rPr>")
		}
		out.WriteString(`<w:t xml:space="preserve">`)
		_ = xml.EscapeText(&out, []byte(text))
		out.WriteString("</w:t></w:r>")
	}

	for {
		tok, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return pureTextRun(plain, extras), nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := strings.ToLower(t.Name.Local)
			if local == "root" {
				continue
			}
			if local == "br" {
				out.WriteString("<w:r><w:br/></w:r>")
				hasContent = true
				continue
			}
			classVal := ""
			for _, a := range t.Attr {
				if a.Name.Local == "class" {
					classVal = a.Value
				}
			}
			stack = append(stack, tagFrame{tag: local, class: classVal})

		case xml.EndElement:
			local := strings.ToLower(t.Name.Local)
			if local == "root" || local == "br" {
				continue
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}

		case xml.CharData:
			text := string(t)
			if text == "" {
				continue
			}
			parts := strings.Split(text, "\t")
			for i, part := range parts {
				if part != "" {
					writeTextRun(part)
				}
				if i < len(parts)-1 {
					out.WriteString("<w:r><w:tab/></w:r>")
					hasContent = true
				}
			}
		}
	}

	if !hasContent {
		return pureTextRun(plain, extras), nil
	}
	result := out.Bytes()
	got := collectRunText(result)
	if strings.TrimSpace(plain) != "" && !textCovers(got, plain) {
		return pureTextRun(plain, extras), nil
	}
	return result, nil
}

func pureTextRun(text, extras string) []byte {
	var out bytes.Buffer
	out.WriteString("<w:r>")
	if extras != "" {
		out.WriteString("<w:rPr>")
		out.WriteString(extras)
		out.WriteString("</w:rPr>")
	}
	out.WriteString(`<w:t xml:space="preserve">`)
	_ = xml.EscapeText(&out, []byte(text))
	out.WriteString("</w:t></w:r>")
	return out.Bytes()
}

// stripAllTags strips well-formed HTML tags; bare "<" is kept as text.
func stripAllTags(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '<' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i + 1
		if j < len(s) && s[j] == '/' {
			j++
		}
		if j >= len(s) || !isTagNameStart(s[j]) {
			b.WriteByte('<')
			i++
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			b.WriteString(s[i:])
			break
		}
		i += end + 1
	}
	return html.UnescapeString(b.String())
}

func isTagNameStart(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// collectRunText extracts <w:t> plain text from generated OOXML runs.
func collectRunText(runs []byte) string {
	decoder := xml.NewDecoder(bytes.NewReader(runs))
	decoder.Strict = false
	var b strings.Builder
	inT := false
	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inT = true
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inT = false
			}
		case xml.CharData:
			if inT {
				b.Write(t)
			}
		}
	}
	return b.String()
}

// textCovers reports whether got covers plain content.
func textCovers(got, plain string) bool {
	g := strings.Join(strings.Fields(got), "")
	p := strings.Join(strings.Fields(plain), "")
	if p == "" {
		return true
	}
	return g == p || strings.Contains(g, p)
}
