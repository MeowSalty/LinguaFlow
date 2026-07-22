package html

import (
	"bytes"
	"fmt"
	"strings"

	xhtml "golang.org/x/net/html"

	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// blockElements 与 epub 对齐的块级可翻译元素。
var blockElements = map[string]bool{
	"p": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true,
	"li": true, "td": true, "th": true,
	"blockquote": true, "figcaption": true, "dt": true, "dd": true,
}

// skipTags 跳过、不产段的标签（与 epub 对齐）。
var skipTags = map[string]bool{
	"script": true, "style": true, "code": true, "pre": true,
}

// impliedCloseBy 定义块级元素的 HTML 隐式闭合规则。
// key 是「正在收集的块标签」，value 是「会隐式关闭该块的后续开始标签集合」。
// 例：<p> 在遇到下一个 <p>/<address>/<article>... 时应自动闭合（即使省略了 </p>）。
// 依据 HTML 规范 "optional end tag" 规则精简到翻译分段所需的子集。
var impliedCloseBy = map[string]map[string]bool{
	"p": set("p", "address", "article", "aside", "blockquote", "div",
		"dl", "fieldset", "footer", "form", "h1", "h2", "h3", "h4",
		"h5", "h6", "header", "hr", "main", "nav", "ol", "pre",
		"section", "table", "ul"),
	"li": set("li"),
	"td": set("td", "th"),
	"th": set("td", "th"),
	"dt": set("dt", "dd"),
	"dd": set("dt", "dd"),
}

func set(tags ...string) map[string]bool {
	m := make(map[string]bool, len(tags))
	for _, t := range tags {
		m[t] = true
	}
	return m
}

// voidElements 无闭合标签的 void 元素。
var voidElements = map[string]bool{
	"br": true, "hr": true, "img": true, "input": true,
	"meta": true, "link": true, "area": true, "base": true,
	"col": true, "embed": true, "source": true, "track": true, "wbr": true,
}

type pathEntry struct {
	tag   string
	count int
}

// pathTracker 管理 element_path 栈（同级同名元素带索引）。
type pathTracker struct {
	stack    []pathEntry
	counters []map[string]int
}

func newPathTracker() *pathTracker {
	return &pathTracker{}
}

func (pt *pathTracker) push(tag string) {
	depth := len(pt.stack)
	for len(pt.counters) <= depth {
		pt.counters = append(pt.counters, make(map[string]int))
	}
	idx := pt.counters[depth][tag]
	pt.counters[depth][tag]++
	pt.stack = append(pt.stack, pathEntry{tag: tag, count: idx})
}

func (pt *pathTracker) pop() {
	if len(pt.stack) == 0 {
		return
	}
	depth := len(pt.stack) - 1
	pt.stack = pt.stack[:depth]
	childDepth := depth + 1
	if childDepth < len(pt.counters) {
		clear(pt.counters[childDepth]) // 复用已有 map，避免每次 pop 分配
	}
}

// top 返回栈顶标签名，空栈返回空串。
func (pt *pathTracker) top() string {
	if len(pt.stack) == 0 {
		return ""
	}
	return pt.stack[len(pt.stack)-1].tag
}

func (pt *pathTracker) path() string {
	var b strings.Builder
	for i, entry := range pt.stack {
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

// extractSegments 用 Tokenizer 按字节偏移切出可翻译段。
//
// Tokenizer.Raw() 保证 token 原始字节划分字节流、无重叠无间隙，
// 当前 token 偏移 = 之前所有 token Raw() 长度之和。
func extractSegments(raw []byte) []pipeline.Segment {
	z := xhtml.NewTokenizer(bytes.NewReader(raw))
	pt := newPathTracker()

	var (
		segs            []pipeline.Segment
		offset          int
		collecting      bool
		blockTag        string
		blockPath       string
		blockInnerStart int
		blockStackDepth int // 开始收集时 path 栈深度，用于嵌套同名块
		skipDepth       int
		inHead          bool
		inTitle         bool
		titleStart      int
		titlePath       string
	)

	for {
		tt := z.Next()
		tokRaw := z.Raw()
		tokenStart := offset
		tokenEnd := offset + len(tokRaw)

		switch tt {
		case xhtml.ErrorToken:
			// EOF：刷新尚未闭合的块/标题，避免截断或省略闭合标签丢失尾部段落。
			flushInflight(&segs, raw, offset, &collecting, &blockTag, &blockPath,
				&blockInnerStart, &inTitle, &titleStart, &titlePath)
			return segs

		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			name, _ := z.TagName()
			tag := string(name)
			selfClosing := tt == xhtml.SelfClosingTagToken || voidElements[tag]

			if skipDepth > 0 {
				if !selfClosing {
					skipDepth++
				}
				break
			}

			if skipTags[tag] {
				if !selfClosing {
					skipDepth = 1
				}
				break
			}

			if tag == "head" && !selfClosing {
				inHead = true
				pt.push(tag)
				break
			}

			// <title> 仅在 head 内收集为独立段。
			if tag == "title" && inHead && !collecting && !selfClosing {
				inTitle = true
				titleStart = tokenEnd
				pt.push(tag)
				titlePath = pt.path()
				break
			}

			// 隐式闭合：HTML 允许省略 </p></li> 等闭合标签。
			// 若正处于收集且新开始标签会隐式关闭当前块，先收尾当前段再处理新标签。
			if collecting && !inTitle && !selfClosing {
				if closes, ok := impliedCloseBy[blockTag]; ok && closes[tag] {
					finishBlock(&segs, raw, tokenStart, &collecting, &blockTag,
						&blockPath, &blockInnerStart, &blockStackDepth, pt)
				}
			}

			if !selfClosing {
				pt.push(tag)
			}

			if blockElements[tag] && !collecting && !inTitle && !selfClosing {
				collecting = true
				blockTag = tag
				blockPath = pt.path()
				blockInnerStart = tokenEnd
				blockStackDepth = len(pt.stack)
			}

		case xhtml.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)

			if skipDepth > 0 {
				skipDepth--
				break
			}

			if inTitle && tag == "title" {
				appendSegment(&segs, raw, titleStart, tokenStart, "title", "block", titlePath)
				inTitle = false
				titlePath = ""
				titleStart = 0
				pt.pop()
				break
			}

			// 仅在路径栈深度匹配时结束当前块，避免嵌套同名块提前收口。
			if collecting && tag == blockTag && len(pt.stack) == blockStackDepth {
				finishBlock(&segs, raw, tokenStart, &collecting, &blockTag,
					&blockPath, &blockInnerStart, &blockStackDepth, pt)
				break
			}

			if tag == "head" {
				inHead = false
			}
			// 仅当 EndTag 匹配栈顶时才 pop，避免 </br>/孤立 EndTag 错误弹出外层块。
			// x/net/html Tokenizer 不做树构建，孤立 EndTag 直接送达此处。
			if pt.top() == tag {
				pt.pop()
			}
		}

		offset = tokenEnd
	}
}

// finishBlock 收尾当前正在收集的块级段：写入段、重置收集状态、弹出栈顶。
// 在 EndTag 正常闭合、隐式闭合、EOF flush 三种路径下复用。
func finishBlock(segs *[]pipeline.Segment, raw []byte, end int,
	collecting *bool, blockTag, blockPath *string, blockInnerStart, blockStackDepth *int,
	pt *pathTracker) {
	appendSegment(segs, raw, *blockInnerStart, end, *blockTag, "block", *blockPath)
	*collecting = false
	*blockTag = ""
	*blockPath = ""
	*blockInnerStart = 0
	*blockStackDepth = 0
	pt.pop()
}

// flushInflight 在 EOF/解析错误时刷新尚未闭合的块或标题段，避免丢失尾部可翻译文本。
// offset 为已消费字节数（即段末偏移）。
func flushInflight(segs *[]pipeline.Segment, raw []byte, offset int,
	collecting *bool, blockTag, blockPath *string, blockInnerStart *int,
	inTitle *bool, titleStart *int, titlePath *string) {
	if *inTitle {
		appendSegment(segs, raw, *titleStart, offset, "title", "block", *titlePath)
		*inTitle = false
		*titleStart = 0
		*titlePath = ""
	}
	if *collecting {
		appendSegment(segs, raw, *blockInnerStart, offset, *blockTag, "block", *blockPath)
		*collecting = false
		*blockTag = ""
		*blockPath = ""
		*blockInnerStart = 0
	}
}

func appendSegment(segs *[]pipeline.Segment, raw []byte, start, end int, tag, block, path string) {
	if start < 0 || end < start || end > len(raw) {
		return
	}
	inner := raw[start:end]
	if len(bytes.TrimSpace(inner)) == 0 {
		return
	}
	src := string(inner)
	*segs = append(*segs, pipeline.Segment{
		ID:     hash.Short(src),
		Source: src,
		Meta: map[string]any{
			"html_byte_range": []int{start, end},
			"html_tag":        tag,
			"html_block":      block,
			"element_path":    path,
		},
	})
}
