package docx

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// extractSegmentsFromDocumentXML 从 word/document.xml 提取段落级 Segment。
func extractSegmentsFromDocumentXML(data []byte) ([]pipeline.Segment, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false

	pt := newPathTracker()
	var segments []pipeline.Segment

	var (
		inParagraph bool
		paraPath    string
		paraDepth   int // path stack depth at <w:p>
		runs        []runFragment

		inRun     bool
		inRPr     bool
		rPrProps  []xml.StartElement
		runExtras map[string]string

		capturingExtra bool
		extraDepth     int
		extraLocal     string
		extraAttrs     []xml.Attr

		inT     bool
		runText strings.Builder

		skipDepth int
	)

	for {
		tok, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("docx: xml token: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			local := t.Name.Local

			if skipDepth > 0 {
				skipDepth++
				continue
			}
			if isSkipEmbedded(local) {
				skipDepth = 1
				continue
			}

			if capturingExtra {
				extraDepth++
				continue
			}

			if inRPr {
				if visualExtrasProps[local] {
					capturingExtra = true
					extraDepth = 1
					extraLocal = local
					extraAttrs = append([]xml.Attr(nil), t.Attr...)
					continue
				}
				rPrProps = append(rPrProps, t)
				continue
			}

			if inRun && local == "rPr" {
				inRPr = true
				rPrProps = nil
				continue
			}

			if inParagraph && local == "r" && !inRun {
				inRun = true
				rPrProps = nil
				runExtras = make(map[string]string)
				runText.Reset()
				pt.push(local)
				continue
			}

			if inRun {
				switch local {
				case "t":
					inT = true
				case "br", "cr":
					runText.WriteString("<br/>")
				case "tab":
					runText.WriteByte('\t')
				}
				continue
			}

			if local == "p" && isWordNS(t.Name.Space) && !inParagraph {
				pt.push(local)
				inParagraph = true
				paraPath = pt.path()
				paraDepth = len(pt.stack)
				runs = nil
				continue
			}

			pt.push(local)

		case xml.EndElement:
			local := t.Name.Local

			if skipDepth > 0 {
				skipDepth--
				continue
			}

			if capturingExtra {
				extraDepth--
				if extraDepth <= 0 {
					xmlFrag := serializeEmptyElement("w", extraLocal, extraAttrs)
					if runExtras == nil {
						runExtras = make(map[string]string)
					}
					if _, exists := runExtras[extraLocal]; !exists {
						runExtras[extraLocal] = xmlFrag
					}
					capturingExtra = false
					extraLocal = ""
					extraAttrs = nil
				}
				continue
			}

			if inRPr {
				if local == "rPr" {
					inRPr = false
				}
				continue
			}

			if inRun {
				if inT && local == "t" {
					inT = false
					continue
				}
				if local == "r" {
					finishRun(&runs, rPrProps, runExtras, runText.String())
					inRun = false
					rPrProps = nil
					runExtras = nil
					runText.Reset()
					pt.pop()
					continue
				}
				// 其他 run 内闭标签（br/tab 等）忽略
				continue
			}

			if inParagraph && local == "p" && len(pt.stack) == paraDepth {
				source, extrasXML := assembleParagraph(runs)
				if strings.TrimSpace(source) != "" {
					seg := pipeline.Segment{
						ID:     segmentID(paraPath, source),
						Source: source,
						Meta: map[string]any{
							"element_path": paraPath,
						},
					}
					if extrasXML != "" {
						seg.Meta["docx_rpr_extras"] = extrasXML
					}
					segments = append(segments, seg)
				}
				inParagraph = false
				paraPath = ""
				paraDepth = 0
				runs = nil
				pt.pop()
				continue
			}

			pt.pop()

		case xml.CharData:
			if inT && inRun {
				runText.WriteString(escapeHTMLText(string(t)))
			}
		}
	}

	return segments, nil
}

// runFragment 是段落内一个（合并后）run 的中间表示。
type runFragment struct {
	openTags  []string
	closeTags []string
	text      string
	key       string
	extras    map[string]string
}

func tagsKey(open, close []string) string {
	return strings.Join(open, "") + "|" + strings.Join(close, "")
}

func finishRun(runs *[]runFragment, rPrProps []xml.StartElement, runExtras map[string]string, text string) {
	openTags, closeTags := semanticTagsFromRPr(rPrProps)
	key := tagsKey(openTags, closeTags)
	extrasCopy := make(map[string]string, len(runExtras))
	for k, v := range runExtras {
		extrasCopy[k] = v
	}
	if len(*runs) > 0 && (*runs)[len(*runs)-1].key == key {
		prev := &(*runs)[len(*runs)-1]
		prev.text += text
		// extras 取交集：仅双方都有且值相同的属性保留（避免合并掩盖冲突）
		for k, prevV := range prev.extras {
			if v, ok := extrasCopy[k]; !ok || v != prevV {
				delete(prev.extras, k)
			}
		}
		return
	}
	*runs = append(*runs, runFragment{
		openTags:  openTags,
		closeTags: closeTags,
		text:      text,
		key:       key,
		extras:    extrasCopy,
	})
}

var knownExtrasOrder = extrasOrder

func assembleParagraph(runs []runFragment) (source string, extrasXML string) {
	var b strings.Builder
	for _, r := range runs {
		for _, o := range r.openTags {
			b.WriteString(o)
		}
		b.WriteString(r.text)
		for _, c := range r.closeTags {
			b.WriteString(c)
		}
	}

	contentRuns := make([]runFragment, 0, len(runs))
	for _, r := range runs {
		if r.text != "" || len(r.extras) > 0 {
			contentRuns = append(contentRuns, r)
		}
	}
	if len(contentRuns) == 0 {
		return b.String(), ""
	}

	allKeys := make(map[string]struct{})
	for _, r := range contentRuns {
		for k := range r.extras {
			allKeys[k] = struct{}{}
		}
	}

	var parts []string
	for _, k := range knownExtrasOrder {
		if _, ok := allKeys[k]; !ok {
			continue
		}
		common, agree := "", true
		for i, r := range contentRuns {
			v, ok := r.extras[k]
			if !ok {
				agree = false
				break
			}
			if i == 0 {
				common = v
			} else if v != common {
				agree = false
				break
			}
		}
		if agree && common != "" {
			parts = append(parts, common)
		}
	}
	return b.String(), strings.Join(parts, "")
}

func serializeEmptyElement(prefix, local string, attrs []xml.Attr) string {
	var b strings.Builder
	b.WriteByte('<')
	if prefix != "" {
		b.WriteString(prefix)
		b.WriteByte(':')
	}
	b.WriteString(local)
	for _, a := range attrs {
		if a.Name.Local == "xmlns" || a.Name.Space == "xmlns" {
			continue
		}
		b.WriteByte(' ')
		switch {
		case a.Name.Space == wNS || a.Name.Space == "w":
			b.WriteString("w:")
		case a.Name.Space == "http://www.w3.org/XML/1998/namespace":
			b.WriteString("xml:")
		case a.Name.Space == "http://schemas.openxmlformats.org/officeDocument/2006/relationships":
			b.WriteString("r:")
		}
		b.WriteString(a.Name.Local)
		b.WriteString(`="`)
		b.WriteString(xmlEscapeAttr(a.Value))
		b.WriteByte('"')
	}
	b.WriteString("/>")
	return b.String()
}

func xmlEscapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// escapeHTMLText 将 OOXML 文本安全嵌入 HTML Source，避免 < & 被当作标签/实体破坏结构。
func escapeHTMLText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:6])
}

func segmentID(elementPath, source string) string {
	return shortHash(elementPath + ":" + source)
}
