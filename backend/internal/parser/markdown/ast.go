package markdown

import (
	"github.com/yuin/goldmark"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

const maxMarkdownBytes = 16 << 20 // 16 MiB

// leafSpan 表示叶子段落的字节区间 [start, end)。
type leafSpan struct {
	start int
	end   int
	block string
	level int // 仅标题使用
}

// md 是包级共享的 goldmark 实例。goldmark 的扩展（GFM、meta.Meta）本身无每文档可变
// 状态，per-parse 状态都通过 parser.Context 隔离，因此可并发复用，避免每次 Parse 都
// 重建解析器。
var md = newMarkdown()

func newMarkdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM, meta.Meta),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
}

func extractLeafSegments(raw []byte) []pipeline.Segment {
	doc := md.Parser().Parse(text.NewReader(raw), parser.WithContext(parser.NewContext()))

	var leaves []leafSpan
	walkBlocks(doc, "paragraph", &leaves)

	segs := make([]pipeline.Segment, 0, len(leaves))
	for _, leaf := range leaves {
		if leaf.start < 0 || leaf.end <= leaf.start || leaf.end > len(raw) {
			continue
		}
		src := string(raw[leaf.start:leaf.end])
		if src == "" {
			continue
		}
		m := map[string]any{
			"md_block":      leaf.block,
			"md_byte_range": []int{leaf.start, leaf.end},
		}
		if leaf.block == "heading" {
			m["md_level"] = leaf.level
		}
		segs = append(segs, pipeline.Segment{
			ID:     shortHash(src),
			Source: src,
			Meta:   m,
		})
	}
	return segs
}

// walkBlocks 遍历块节点并产出叶子区间。
// parentBlock 由嵌套容器（blockquote / list_item）继承。
func walkBlocks(n ast.Node, parentBlock string, out *[]leafSpan) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {
		case *ast.Document:
			walkBlocks(node, parentBlock, out)

		case *ast.Heading:
			start, end, ok := blockLinesSpan(node)
			if !ok {
				start, end, ok = inlineSpan(node)
			}
			if ok {
				*out = append(*out, leafSpan{
					start: start,
					end:   end,
					block: "heading",
					level: node.Level,
				})
			}

		case *ast.Paragraph:
			start, end, ok := blockLinesSpan(node)
			if !ok {
				continue
			}
			block := "paragraph"
			switch parentBlock {
			case "list_item", "blockquote":
				block = parentBlock
			}
			*out = append(*out, leafSpan{start: start, end: end, block: block})

		case *ast.TextBlock:
			// 紧凑列表项使用 TextBlock 而非 Paragraph。
			// 跳过文档级 TextBlock（例如 goldmark-meta 留下的无效 YAML frontmatter）。
			if parentBlock != "list_item" {
				continue
			}
			start, end, ok := blockLinesSpan(node)
			if !ok {
				continue
			}
			*out = append(*out, leafSpan{start: start, end: end, block: "list_item"})

		case *ast.List:
			walkBlocks(node, parentBlock, out)

		case *ast.ListItem:
			walkBlocks(node, "list_item", out)

		case *ast.Blockquote:
			walkBlocks(node, "blockquote", out)

		case *east.Table:
			walkBlocks(node, parentBlock, out)

		case *east.TableHeader, *east.TableRow:
			walkBlocks(node, parentBlock, out)

		case *east.TableCell:
			start, end, ok := blockLinesSpan(node)
			if !ok {
				start, end, ok = inlineSpan(node)
			}
			if ok {
				*out = append(*out, leafSpan{start: start, end: end, block: "table_cell"})
			}

		case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.HTMLBlock, *ast.ThematicBreak:
			// 不切段；渲染时字节原样透传。

		default:
			// 未知容器（如 definition list）：递归处理。
			if c.HasChildren() && c.Type() == ast.TypeBlock {
				walkBlocks(c, parentBlock, out)
			}
		}
	}
}

// blockLinesSpan 返回块节点 Lines() 各段的并集区间。
func blockLinesSpan(n ast.Node) (start, end int, ok bool) {
	lines := n.Lines()
	if lines == nil || lines.Len() == 0 {
		return 0, 0, false
	}
	start = lines.At(0).Start
	end = lines.At(lines.Len() - 1).Stop
	if start < end {
		return start, end, true
	}
	return 0, 0, false
}

// inlineSpan 从内联子节点收集源码位置。
func inlineSpan(n ast.Node) (start, end int, ok bool) {
	start = -1
	end = -1
	_ = ast.Walk(n, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := node.(type) {
		case *ast.Text:
			updateSpan(&start, &end, t.Segment.Start, t.Segment.Stop)
		case *ast.CodeSpan:
			// CodeSpan 子节点为带 segment 的 Text。
		case *ast.RawHTML:
			if t.Segments != nil {
				for i := 0; i < t.Segments.Len(); i++ {
					s := t.Segments.At(i)
					updateSpan(&start, &end, s.Start, s.Stop)
				}
			}
			// AutoLink / Link / Image / CodeSpan：由 Text 子节点覆盖源码区间。
		}
		return ast.WalkContinue, nil
	})
	if start < 0 || end <= start {
		return 0, 0, false
	}
	return start, end, true
}

func updateSpan(start, end *int, s, e int) {
	if s < 0 || e <= s {
		return
	}
	if *start < 0 || s < *start {
		*start = s
	}
	if e > *end {
		*end = e
	}
}
