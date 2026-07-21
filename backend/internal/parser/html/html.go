// Package html 实现基于 x/net/html Tokenizer 的 HTML parser。
//
// Parse 按块级元素（p / h1-h6 / li / td / th / blockquote 等）与 <head><title>
// 切段，Meta 记录 html_byte_range 字节区间；Render 按区间做原始字节直通替换。
// script / style / code / pre 与可翻译属性不在 v1 范围内，字节原样直通。
package html

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

const maxHTMLBytes = 16 << 20 // 16 MiB，对齐 markdown

// Parser 解析 .html / .htm 文件。
type Parser struct{}

// New 构造 Parser。
func New() *Parser { return &Parser{} }

// Extensions 返回支持的扩展名。
func (*Parser) Extensions() []string { return []string{".html", ".htm"} }

// Parse 将 HTML 字节流解析为可翻译 Document。
func (*Parser) Parse(_ context.Context, r io.Reader, _ string) (*pipeline.Document, error) {
	raw, err := readNormalized(r)
	if err != nil {
		return nil, err
	}
	return &pipeline.Document{
		Segments: extractSegments(raw),
		Format:   "html",
	}, nil
}

// Render 按 html_byte_range 对原文做字节直通替换。
func (*Parser) Render(_ context.Context, doc *pipeline.Document, original io.Reader, w io.Writer) error {
	raw, err := readNormalized(original)
	if err != nil {
		return err
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
		pos, ok := seg.Meta["html_byte_range"].([]int)
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
		// 防御空段误删原文（对齐 markdown）。
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

	filtered := replacements[:0]
	lastEnd := 0
	for _, rep := range replacements {
		if rep.start < lastEnd {
			slog.Warn("html: overlapping html_byte_range, dropping",
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

// readNormalized 读取并规范化换行，同时校验大小上限。
func readNormalized(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxHTMLBytes+1))
	if err != nil {
		return nil, fmt.Errorf("html: read: %w", err)
	}
	if len(raw) > maxHTMLBytes {
		return nil, fmt.Errorf("html: file exceeds %d byte limit", maxHTMLBytes)
	}
	// 快路径：无 CRLF 时直接返回，避免分配等长副本（16 MiB 上限下峰值翻倍）。
	if !bytes.Contains(raw, []byte("\r\n")) {
		return raw, nil
	}
	return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), nil
}

func init() {
	parser.Register("html", New())
}
