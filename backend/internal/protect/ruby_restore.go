package protect

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// RubyRestorer 负责将 LLM 输出的注音信息还原为 <ruby> 标签。
// 它不是 Protector 接口的实现，而是作为 unprotect 之后的额外 stage。
type RubyRestorer struct {
	OutputFormat string // "ruby_output" | "inline_markers"
}

// NewRubyRestorer 创建 RubyRestorer 实例。
func NewRubyRestorer(outputFormat string) *RubyRestorer {
	return &RubyRestorer{OutputFormat: outputFormat}
}

// Restore 根据输出模式还原注音标签。
func (r *RubyRestorer) Restore(seg *pipeline.Segment, rubyOutput []RubyOutputEntry) error {
	switch r.OutputFormat {
	case "inline_markers":
		return r.restoreInlineMarkers(seg)
	case "ruby_output":
		fallthrough
	default:
		return r.restoreRubyOutput(seg, rubyOutput)
	}
}

// RubyOutputEntry 是 LLM 返回的单条标注输出。
type RubyOutputEntry struct {
	Base string `json:"base"`
	Text string `json:"text"`
}

// restoreRubyOutput 通过文本匹配将注音还原为 <ruby> 标签。
// 核心逻辑：在译文中找到基底文本的对应位置，插入注音。
//
// 匹配策略：
//  1. 按 rubyOutput 的顺序，为每个条目在译文中查找第一个未被分配的基底文本出现位置
//  2. 从右到左应用替换，避免索引偏移
func (r *RubyRestorer) restoreRubyOutput(seg *pipeline.Segment, rubyOutput []RubyOutputEntry) error {
	if len(rubyOutput) == 0 {
		return nil
	}

	target := seg.Target

	// 记录已分配的字节位置
	assigned := make(map[int]bool)

	type insertInfo struct {
		pos  int
		end  int
		base string
		text string
	}

	var inserts []insertInfo

	for _, entry := range rubyOutput {
		if entry.Base == "" {
			continue
		}
		// 在译文中查找第一个未被分配的基底文本出现位置
		searchFrom := 0
		for {
			idx := strings.Index(target[searchFrom:], entry.Base)
			if idx == -1 {
				break
			}
			absIdx := searchFrom + idx
			if !assigned[absIdx] {
				assigned[absIdx] = true
				inserts = append(inserts, insertInfo{
					pos:  absIdx,
					end:  absIdx + len(entry.Base),
					base: entry.Base,
					text: entry.Text,
				})
				break
			}
			searchFrom = absIdx + 1
		}
	}

	if len(inserts) == 0 {
		return nil
	}

	// 按位置从右到左排序，避免替换时索引偏移
	sort.Slice(inserts, func(i, j int) bool {
		return inserts[i].pos > inserts[j].pos
	})

	// 从右到左应用替换
	for _, ins := range inserts {
		rubyTag := fmt.Sprintf("<ruby>%s<rt>%s</rt></ruby>", ins.base, ins.text)
		target = target[:ins.pos] + rubyTag + target[ins.end:]
	}

	seg.Target = target
	return nil
}

// inlineMarkerRe 匹配 ⟦ruby:base/text⟧ 格式的内联标记。
var inlineMarkerRe = regexp.MustCompile(`⟦ruby:([^/⟧]+)/([^⟧]+)⟧`)

// restoreInlineMarkers 通过正则替换将内联标记还原为 <ruby> 标签。
//
// ⟦ruby:base/text⟧ → <ruby>base<rt>text</rt></ruby>
func (r *RubyRestorer) restoreInlineMarkers(seg *pipeline.Segment) error {
	seg.Target = inlineMarkerRe.ReplaceAllStringFunc(seg.Target, func(match string) string {
		m := inlineMarkerRe.FindStringSubmatch(match)
		if len(m) < 3 {
			return match
		}
		return fmt.Sprintf("<ruby>%s<rt>%s</rt></ruby>", m[1], m[2])
	})
	return nil
}
