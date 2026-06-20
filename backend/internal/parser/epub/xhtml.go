// xhtml.go 提供 XHTML 文本提取辅助功能。
//
// 以块级元素为单位，提取其内部 HTML（保留所有内联标签），
// 生成 Segment 列表。特殊处理 <nav epub:type="toc"> 的叶子节点策略。
package epub

import (
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"html"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// blockElements 定义需要提取的块级元素集合。
var blockElements = map[string]bool{
	"p": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true,
	"li": true, "td": true, "th": true,
	"blockquote": true, "figcaption": true, "dt": true, "dd": true,
}

// skipTags 定义需要跳过的标签集合。
var skipTags = map[string]bool{
	"script": true, "style": true, "code": true, "pre": true,
}

// xhtmlVoidElements 是 HTML void（自闭合）元素集合。
// 这些元素没有内容，不应生成闭合标签。
var xhtmlVoidElements = map[string]bool{
	"br": true, "hr": true, "img": true, "input": true,
	"meta": true, "link": true, "area": true, "base": true,
	"col": true, "embed": true, "source": true, "track": true, "wbr": true,
}

// pathEntry 表示 element_path 栈中的一个条目。
type pathEntry struct {
	tag   string
	count int
}

// pathTracker 管理 element_path 栈，确保同级同名元素路径唯一。
type pathTracker struct {
	stack    []pathEntry
	counters []map[string]int // counters[d] 记录深度 d 的兄弟标签计数
}

func newPathTracker() *pathTracker {
	return &pathTracker{}
}

// push 将标签推入路径栈，自动计算同级索引。
func (pt *pathTracker) push(tag string) {
	depth := len(pt.stack)
	for len(pt.counters) <= depth {
		pt.counters = append(pt.counters, make(map[string]int))
	}
	idx := pt.counters[depth][tag]
	pt.counters[depth][tag]++
	pt.stack = append(pt.stack, pathEntry{tag: tag, count: idx})
}

// pop 从路径栈弹出栈顶元素，并重置子级计数器。
func (pt *pathTracker) pop() {
	if len(pt.stack) > 0 {
		depth := len(pt.stack) - 1
		pt.stack = pt.stack[:depth]
		// 重置被弹出元素的子级计数器
		childDepth := depth + 1
		if childDepth < len(pt.counters) {
			pt.counters[childDepth] = make(map[string]int)
		}
	}
}

// path 返回当前 element_path 字符串。
func (pt *pathTracker) path() string {
	return buildElementPath(pt.stack)
}

// extractSegmentsFromXHTML 从 XHTML 数据中提取 Segment 列表。
//
// 以块级元素为单位，提取其内部 HTML（保留内联标签如 <b>、<strong>、
// <i>、<em>、<ruby>、<rt>、<a>、<span> 等）。
//
// 特殊处理：
//   - <nav epub:type="toc">: 叶子节点策略，提取每个 <a> 标签的 textContent
//   - <nav epub:type="page-list"> 和 <nav epub:type="landmarks">: 跳过
//   - <script>, <style>, <code>, <pre>: 跳过
func extractSegmentsFromXHTML(data []byte, epubFilePath string) ([]pipeline.Segment, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity

	var segments []pipeline.Segment

	// element_path 栈
	pt := newPathTracker()

	// 块级元素收集状态
	var (
		collecting  bool   // 是否正在收集块级元素内容
		blockPath   string // 当前块级元素的 element_path
		blockTag    string // 当前块级元素的标签名
		innerHTML   strings.Builder
		skipDepth   int  // 跳过标签嵌套深度
		inTocNav    bool // 是否在 <nav epub:type="toc"> 内
		tocLinkText strings.Builder
		inTocLink   bool // 是否在 toc 内的 <a> 标签内
	)

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}

		switch t := tok.(type) {
		case xml.StartElement:
			tag := t.Name.Local

			// 跳过标签处理
			if skipTags[tag] {
				skipDepth++
				if collecting {
					writeStartTag(&innerHTML, t)
				}
				continue
			}
			if skipDepth > 0 {
				skipDepth++
				if collecting {
					writeStartTag(&innerHTML, t)
				}
				continue
			}

			// 更新路径栈
			pt.push(tag)

			// 条件跳过：检查 <nav epub:type="...">
			if tag == "nav" {
				navType := getEpubType(t)
				switch navType {
				case "page-list", "landmarks":
					skipDepth = 1
					continue
				case "toc":
					inTocNav = true
					continue
				}
			}

			// toc 内的 <a> 标签处理
			if inTocNav && tag == "a" {
				inTocLink = true
				tocLinkText.Reset()
				continue
			}
			if inTocNav {
				continue
			}

			// 块级元素检测
			if blockElements[tag] && !collecting {
				collecting = true
				blockPath = pt.path()
				blockTag = tag
				innerHTML.Reset()
				continue
			}

			// 收集中的内联标签 → 写入 innerHTML
			if collecting {
				writeStartTag(&innerHTML, t)
			}

		case xml.EndElement:
			tag := t.Name.Local

			// 跳过标签处理
			if skipDepth > 0 {
				skipDepth--
				if collecting {
					innerHTML.WriteString("</" + tag + ">")
				}
				if skipDepth == 0 {
					continue
				}
				continue
			}

			// toc 内的 </a> 标签处理
			if inTocLink && tag == "a" {
				text := strings.TrimSpace(tocLinkText.String())
				if text != "" {
					contentHash := shortHash(text)
					segments = append(segments, pipeline.Segment{
						ID:     segmentID(epubFilePath, contentHash),
						Source: text,
						Meta: map[string]any{
							"epub_file":    epubFilePath,
							"element_path": pt.path(),
							"content_hash": contentHash,
							"tag":          "a",
						},
					})
				}
				inTocLink = false
				tocLinkText.Reset()
				// 弹出路径栈中的 <a>
				pt.pop()
				continue
			}
			if inTocNav && tag == "nav" {
				inTocNav = false
				pt.pop()
				continue
			}
			if inTocNav {
				pt.pop()
				continue
			}

			// 块级元素结束
			if collecting && tag == blockTag {
				htm := strings.TrimSpace(innerHTML.String())
				if htm != "" {
					contentHash := shortHash(htm)
					segments = append(segments, pipeline.Segment{
						ID:     segmentID(epubFilePath, contentHash),
						Source: htm,
						Meta: map[string]any{
							"epub_file":    epubFilePath,
							"element_path": blockPath,
							"content_hash": contentHash,
							"tag":          blockTag,
						},
					})
				}
				collecting = false
				blockTag = ""
				blockPath = ""
				innerHTML.Reset()
				pt.pop()
				continue
			}

			// 收集中的内联标签结束
			if collecting {
				if xhtmlVoidElements[tag] {
					pt.pop()
					continue // void 元素不写闭合标签
				}
				innerHTML.WriteString("</" + tag + ">")
			}

			// 弹出路径栈
			pt.pop()

		case xml.CharData:
			text := string(t)
			if collecting {
				innerHTML.WriteString(text)
			}
			if inTocLink {
				tocLinkText.WriteString(text)
			}
		}
	}

	return segments, nil
}

// writeStartTag 将 StartElement 序列化为 HTML 开始标签字符串。
func writeStartTag(b *strings.Builder, el xml.StartElement) {
	b.WriteByte('<')
	b.WriteString(el.Name.Local)
	for _, attr := range el.Attr {
		b.WriteByte(' ')
		if attr.Name.Space != "" {
			b.WriteString(attr.Name.Space)
			b.WriteByte(':')
		}
		b.WriteString(attr.Name.Local)
		b.WriteString(`="`)
		b.WriteString(html.EscapeString(attr.Value))
		b.WriteByte('"')
	}
	if xhtmlVoidElements[el.Name.Local] {
		b.WriteString(" />")
	} else {
		b.WriteByte('>')
	}
}

// getEpubType 从 StartElement 中提取 epub:type 属性值。
func getEpubType(el xml.StartElement) string {
	for _, attr := range el.Attr {
		if attr.Name.Local == "type" &&
			(attr.Name.Space == "http://www.idpf.org/2007/ops" || attr.Name.Space == "") {
			return attr.Value
		}
	}
	return ""
}

// buildElementPath 根据路径栈生成 DOM 节点路径。
// 例如：body/section[0]/p[2]
func buildElementPath(stack []pathEntry) string {
	var b strings.Builder
	for i, entry := range stack {
		if i > 0 {
			b.WriteByte('/')
		}
		b.WriteString(entry.tag)
		if entry.count > 0 {
			fmt.Fprintf(&b, "[%d]", entry.count)
		}
	}
	return b.String()
}

// shortHash 计算字符串的 SHA256 前 12 位十六进制摘要。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:6])
}

// segmentID 生成 Segment 的稳定 ID。
// 基于 epub_file + content_hash 的组合，确保唯一性。
func segmentID(epubFile, contentHash string) string {
	return shortHash(epubFile + ":" + contentHash)
}

// isBlockElement 判断标签是否为块级元素。
func isBlockElement(tag string) bool {
	return blockElements[tag]
}

// shouldSkipTag 判断标签是否应被跳过。
func shouldSkipTag(tag string) bool {
	return skipTags[tag]
}
