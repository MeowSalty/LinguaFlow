// Package stages 提供 pipeline 的具体步骤实现。
package stages

import (
	"context"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// Split 在解析后再次切分：把超过 maxChars 的段落按句号/换行切小，
// 并为每段（重新）生成稳定 ID。MVP 仅支持 paragraph 策略。
type Split struct {
	MaxChars int
}

func NewSplit(maxChars int) *Split {
	if maxChars < 1 {
		maxChars = 1200
	}
	return &Split{MaxChars: maxChars}
}

func (*Split) Name() string { return "split" }

func (s *Split) Run(_ context.Context, doc *pipeline.Document) error {
	var out []pipeline.Segment
	for _, seg := range doc.Segments {
		for _, piece := range splitByLimit(seg.Source, s.MaxChars) {
			out = append(out, pipeline.Segment{
				ID:     hash.Short(piece),
				Source: piece,
				Meta:   seg.Meta,
			})
		}
	}
	doc.Segments = out
	return nil
}

// splitByLimit 在不破坏行边界的前提下按 limit 大致切分。
// 优先在换行处切；若单行已超 limit 仍保留整行（避免破坏代码块/长句）。
func splitByLimit(s string, limit int) []string {
	if len(s) <= limit {
		return []string{s}
	}
	var (
		out  []string
		buf  strings.Builder
		size int
	)
	for _, line := range strings.SplitAfter(s, "\n") {
		ll := len(line)
		if size+ll > limit && buf.Len() > 0 {
			out = append(out, strings.TrimRight(buf.String(), "\n"))
			buf.Reset()
			size = 0
		}
		buf.WriteString(line)
		size += ll
	}
	if buf.Len() > 0 {
		out = append(out, strings.TrimRight(buf.String(), "\n"))
	}
	return out
}
