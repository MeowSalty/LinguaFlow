// Package markdown 实现基于 goldmark AST 的 Markdown parser。
//
// Parse 按叶子块（heading / paragraph / list item / table cell / blockquote）切段，
// Meta 记录 md_byte_range 字节区间；Render 按区间做原始字节直通替换。
package markdown

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

type Parser struct{}

func New() *Parser { return &Parser{} }

func (*Parser) Extensions() []string { return []string{".md", ".markdown", ".mdx"} }

func (*Parser) Parse(_ context.Context, r io.Reader, _ string) (*pipeline.Document, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxMarkdownBytes+1))
	if err != nil {
		return nil, fmt.Errorf("markdown: read: %w", err)
	}
	if len(raw) > maxMarkdownBytes {
		return nil, fmt.Errorf("markdown: file exceeds %d byte limit", maxMarkdownBytes)
	}

	return &pipeline.Document{
		Segments: extractLeafSegments(raw),
		Format:   "markdown",
	}, nil
}

func (*Parser) Render(_ context.Context, doc *pipeline.Document, original io.Reader, w io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(original, maxMarkdownBytes+1))
	if err != nil {
		return fmt.Errorf("markdown: read original: %w", err)
	}
	if len(raw) > maxMarkdownBytes {
		return fmt.Errorf("markdown: original exceeds %d byte limit", maxMarkdownBytes)
	}

	type replacement struct {
		start int
		end   int
		text  string
	}
	var replacements []replacement
	for _, seg := range doc.Segments {
		if seg.Skip {
			continue
		}
		pos, ok := seg.Meta["md_byte_range"].([]int)
		if !ok || len(pos) < 2 {
			continue
		}
		start, end := pos[0], pos[1]
		if start < 0 || end < start || end > len(raw) {
			continue
		}
		target := seg.Target
		if target == "" {
			target = seg.Source
		}
		// 防御性检查：本解析器产出的 Segment.Source 必非空（见 ast.go 的 src=="" 跳过），
		// 但 Render 接受任意 *pipeline.Document；此处守卫可避免外部构造的空 Source/Target
		// 段落把原始字节区间静默替换为空（即静默删除原文）。
		if target == "" {
			continue
		}
		replacements = append(replacements, replacement{
			start: start,
			end:   end,
			text:  target,
		})
	}

	sort.SliceStable(replacements, func(i, j int) bool {
		return replacements[i].start < replacements[j].start
	})

	// 丢弃重叠区间（按排序顺序保留靠前的）。
	filtered := replacements[:0]
	lastEnd := 0
	for _, rep := range replacements {
		if rep.start < lastEnd {
			slog.Warn("markdown: overlapping md_byte_range, dropping",
				"start", rep.start, "end", rep.end, "lastEnd", lastEnd)
			continue
		}
		filtered = append(filtered, rep)
		lastEnd = rep.end
	}
	replacements = filtered

	bw := bufio.NewWriter(w)
	cursor := 0
	for _, rep := range replacements {
		if _, err := bw.Write(raw[cursor:rep.start]); err != nil {
			return err
		}
		if _, err := bw.WriteString(rep.text); err != nil {
			return err
		}
		cursor = rep.end
	}
	if _, err := bw.Write(raw[cursor:]); err != nil {
		return err
	}
	return bw.Flush()
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

func init() {
	parser.Register("markdown", New())
}
