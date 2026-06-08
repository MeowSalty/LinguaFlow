// Package markdown 实现按段落分割的 Markdown parser。
//
// 位置替换策略：Parse 时记录每个 Segment 的行号范围（1-based），
// Render 时读取原始文件行，按行号范围替换为译文。
// 这样可以完美保留原始文件的所有格式细节（缩进、空行、换行符等）。
package markdown

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

type Parser struct{}

func New() *Parser { return &Parser{} }

func (*Parser) Extensions() []string { return []string{".md", ".markdown", ".mdx"} }

func (*Parser) Parse(_ context.Context, r io.Reader) (*pipeline.Document, error) {
	scanner := bufio.NewScanner(r)
	// 单行最长 1 MiB
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var (
		segments []pipeline.Segment
		buf      strings.Builder
		inFence  bool
		segStart int // 当前段落起始行号（1-based）
		lineNo   int // 当前行号（1-based）
	)
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		text := strings.TrimRight(buf.String(), "\n")
		seg := pipeline.Segment{
			ID:     shortHash(text),
			Source: text,
			Meta: map[string]any{
				"pos_lines": []int{segStart, lineNo},
			},
		}
		segments = append(segments, seg)
		buf.Reset()
	}

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		if !inFence && strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if buf.Len() == 0 {
			segStart = lineNo
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()

	return &pipeline.Document{
		Segments: segments,
		Format:   "markdown",
	}, nil
}

func (*Parser) Render(_ context.Context, doc *pipeline.Document, original io.Reader, w io.Writer) error {
	// 读取原始文件所有行
	scanner := bufio.NewScanner(original)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("markdown: read original: %w", err)
	}

	// 构建行号 → 译文的替换区间
	// pos_lines 是 [startLine, endLine]，1-based，inclusive
	// 一个 segment 可能对应多行原文（如段落），替换时用译文的行替换这些行
	type replacement struct {
		startLine int    // 1-based inclusive
		endLine   int    // 1-based inclusive
		text      string // 译文（可能是多行）
	}
	var replacements []replacement
	for _, seg := range doc.Segments {
		pos, ok := seg.Meta["pos_lines"].([]int)
		if !ok || len(pos) < 2 {
			continue
		}
		target := seg.Target
		if target == "" {
			target = seg.Source
		}
		replacements = append(replacements, replacement{
			startLine: pos[0],
			endLine:   pos[1],
			text:      target,
		})
	}

	// 按行号范围替换
	bw := bufio.NewWriter(w)
	lineIdx := 0 // 0-based index into lines
	for _, rep := range replacements {
		// 写入替换区间之前的行（原样）
		for lineIdx < rep.startLine-1 && lineIdx < len(lines) {
			if _, err := bw.WriteString(lines[lineIdx]); err != nil {
				return err
			}
			if err := bw.WriteByte('\n'); err != nil {
				return err
			}
			lineIdx++
		}
		// 写入译文
		if _, err := bw.WriteString(rep.text); err != nil {
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
		// 跳过原始行（已替换）
		lineIdx = rep.endLine // endLine 是 1-based，所以 lineIdx 现在指向 endLine-1+1
	}
	// 写入剩余行
	for lineIdx < len(lines) {
		if _, err := bw.WriteString(lines[lineIdx]); err != nil {
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
		lineIdx++
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
