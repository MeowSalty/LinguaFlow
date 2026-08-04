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
	"log/slog"
	"path"
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

// isNavFile 检测文件是否为 EPUB3 导航文件。
//
// 检查策略：
//  1. 文件名包含 "navigation-documents" 或为 "nav.xhtml"/"nav.htm"
//  2. 内容中包含 <nav epub:type="toc"> 元素
func isNavFile(href string, xhtmlData []byte) bool {
	base := strings.ToLower(path.Base(href))
	if strings.Contains(base, "navigation-documents") || base == "nav.xhtml" || base == "nav.htm" {
		return true
	}
	// 检查内容中是否包含 <nav epub:type="toc">
	return containsTocNav(xhtmlData)
}

// containsTocNav 检查 XHTML 数据中是否包含 <nav epub:type="toc"> 元素。
func containsTocNav(data []byte) bool {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		if el, ok := tok.(xml.StartElement); ok {
			if el.Name.Local == "nav" && getEpubType(el) == "toc" {
				return true
			}
		}
	}
	return false
}

// shouldSkipTag 判断标签是否应被跳过。
func shouldSkipTag(tag string) bool {
	return skipTags[tag]
}

// extractXHTMLTOCTitles 从 XHTML 目录文件中提取章节标题映射。
//
// 解析 XHTML 内容，提取所有 <a href="file.xhtml#anchor">标题</a> 链接，
// 返回 map[resolvedHref]title。
//
// 例如：{"OEBPS/p-003.xhtml": "プロローグ", "OEBPS/p-004.xhtml": "一章 一年次の春に"}
//
// tocHref 是 TOC 文件在 ZIP 内的路径，用于将相对 href 解析为绝对路径。
func extractXHTMLTOCTitles(data []byte, tocHref string) map[string]string {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity

	titles := make(map[string]string)
	inTocLink := false
	linkHref := ""
	var linkText strings.Builder

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}

		switch t := tok.(type) {
		case xml.StartElement:
			tag := t.Name.Local

			// 收集所有 <a href="..."> 标签
			if tag == "a" {
				for _, attr := range t.Attr {
					if attr.Name.Local == "href" && attr.Name.Space == "" {
						href := attr.Value
						// 只处理指向 XHTML 文件的链接（排除外部链接和纯锚点链接）
						if href != "" && !strings.HasPrefix(href, "http") && !strings.HasPrefix(href, "#") {
							inTocLink = true
							linkHref = href
							linkText.Reset()
						}
						break
					}
				}
			}

		case xml.EndElement:
			if inTocLink && t.Name.Local == "a" {
				text := strings.TrimSpace(linkText.String())
				if text != "" && linkHref != "" {
					// 解析 href：去除 fragment（#锚点）
					href := linkHref
					if idx := strings.IndexByte(href, '#'); idx >= 0 {
						href = href[:idx]
					}

					// 将相对路径解析为 ZIP 内绝对路径
					tocDir := path.Dir(tocHref)
					fullHref := path.Clean(path.Join(tocDir, href))

					// 仅在尚未有标题映射时设置（保留第一个匹配的标题）
					if _, exists := titles[fullHref]; !exists {
						titles[fullHref] = text
					}
				}
				inTocLink = false
				linkHref = ""
				linkText.Reset()
			}

		case xml.CharData:
			if inTocLink {
				linkText.WriteString(string(t))
			}
		}
	}

	slog.Debug("[epub:extractXHTMLTOCTitles] extracted titles", "count", len(titles), "tocHref", tocHref)
	for src, title := range titles {
		slog.Debug("[epub:extractXHTMLTOCTitles] title mapping", "src", src, "title", title)
	}

	return titles
}

// resolveChapterTitle 按优先级解析章节标题。
//
// 提取优先级（从高到低）：
//  1. 目录文件（TOC 或 nav）→ 使用固定名称
//  2. XHTML TOC 文件中的标题（最可靠，从 <a> 链接提取）
//  3. NCX 目录中的标题
//  4. XHTML <head> 中的 <title> 标签
//  5. 正文中第一个 <h1>/<h2>/<h3> 标题
//  6. 文件名（最终回退）
func resolveChapterTitle(href string, xhtmlData []byte, xhtmlTOCTitles, ncxTitles map[string]string, bookTitle string) string {
	// 优先级 1: 目录文件使用固定名称
	if isTOCFile(href) {
		slog.Debug("[epub:resolveChapterTitle] TOC file detected", "href", href)
		return "Contents"
	}

	// 优先级 1b: EPUB3 导航文件使用固定名称
	if isNavFile(href, xhtmlData) {
		slog.Debug("[epub:resolveChapterTitle] nav file detected", "href", href)
		return "Contents"
	}

	// 优先级 2: XHTML TOC 文件中的标题
	if title, ok := xhtmlTOCTitles[path.Clean(href)]; ok {
		slog.Debug("[epub:resolveChapterTitle] XHTML TOC title found", "href", href, "title", title)
		return title
	}

	// 优先级 3: NCX 目录中的标题
	if title, ok := ncxTitles[path.Clean(href)]; ok {
		slog.Debug("[epub:resolveChapterTitle] NCX title found", "href", href, "title", title)
		return title
	}

	// 优先级 4 & 5: 从 XHTML 内容中提取 <title> 或 <h1>/<h2>/<h3>
	if title := extractChapterTitle(xhtmlData); title != "" {
		slog.Debug("[epub:resolveChapterTitle] XHTML title found", "href", href, "title", title)
		return title
	}

	// 优先级 6: 文件名作为最终回退
	return path.Base(href)
}

// extractChapterTitle 从 XHTML 数据中提取章节标题。
//
// 提取优先级：
//  1. <head> 中的 <title> 标签内容
//  2. 正文中第一个 <h1>/<h2>/<h3> 标题文本
//  3. 返回空字符串（调用方应回退到文件名）
func extractChapterTitle(data []byte) string {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity

	headingTags := map[string]bool{
		"h1": true, "h2": true, "h3": true,
	}

	inHead := false

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}

		switch t := tok.(type) {
		case xml.StartElement:
			tag := t.Name.Local
			switch {
			case tag == "head":
				inHead = true
			case tag == "title" && inHead:
				if title := extractTextUntilClose(decoder, "title"); title != "" {
					return title
				}
			case headingTags[tag]:
				if heading := extractTextUntilClose(decoder, tag); heading != "" {
					return heading
				}
			}
		case xml.EndElement:
			if t.Name.Local == "head" {
				inHead = false
			}
		}
	}

	return ""
}

// extractTextUntilClose 从当前位置提取到匹配闭合标签之间的纯文本内容。
// 支持嵌套子标签，正确跟踪深度。
func extractTextUntilClose(decoder *xml.Decoder, endTag string) string {
	var text strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth > 0 {
				text.WriteString(string(t))
			}
		}
	}
	return strings.TrimSpace(text.String())
}
