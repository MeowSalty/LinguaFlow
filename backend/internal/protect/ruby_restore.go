package protect

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
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
// originalAnnotations 为 Protect 阶段提取的原始注音，用于 ruby_output 模式的双源匹配回退；
// inline_markers 模式忽略此参数。可传 nil。
func (r *RubyRestorer) Restore(seg *model.Segment, rubyOutput []RubyOutputEntry, originalAnnotations []RubyAnnotation) error {
	switch r.OutputFormat {
	case "inline_markers":
		return r.restoreInlineMarkers(seg)
	case "ruby_output":
		fallthrough
	default:
		return r.restoreRubyOutput(seg, rubyOutput, originalAnnotations)
	}
}

// RubyOutputEntry 是 LLM 返回的单条标注输出。
type RubyOutputEntry struct {
	Base string `json:"base"`
	Text string `json:"text"`
	Kind string `json:"kind"` // "phonetic" | "semantic" | "creative"
}

// ValidRubyKinds 是所有合法的注音 kind 值。
var ValidRubyKinds = []string{"phonetic", "semantic", "creative"}

// insertInfo 记录一次注音插入的位置和内容。
type insertInfo struct {
	pos  int
	end  int
	base string
	text string
}

// restoreRubyOutput 通过文本匹配将注音还原为 <ruby> 标签。
// 核心逻辑：在译文中找到基底文本的对应位置，插入注音。
//
// 匹配策略（双源匹配）：
//  1. 按 rubyOutput 的顺序，为每个条目在译文中查找第一个未被分配的基底文本出现位置
//  2. 若 LLM 返回的 base 未匹配，回退到原始 annotations 中对应位置的 base 再试一次
//  3. 从右到左应用替换，避免索引偏移
func (r *RubyRestorer) restoreRubyOutput(seg *model.Segment, rubyOutput []RubyOutputEntry, originalAnnotations []RubyAnnotation) error {
	if len(rubyOutput) == 0 {
		return nil
	}

	target := seg.Target

	// 记录已分配的字节位置
	assigned := make(map[int]bool)

	var inserts []insertInfo

	for i, entry := range rubyOutput {
		if entry.Base == "" {
			continue
		}
		// 第一优先：用 LLM 返回的 base（译文中的对应文本）匹配
		found := r.findAndInsert(target, entry.Base, entry.Text, assigned, &inserts)

		// 第二优先：回退到原始 annotation 的 base（原文基底）匹配
		if !found && i < len(originalAnnotations) {
			origBase := originalAnnotations[i].Base
			if origBase != "" && origBase != entry.Base {
				r.findAndInsert(target, origBase, entry.Text, assigned, &inserts)
			}
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

// findAndInsert 在 target 中查找 base 的第一个未分配出现位置，
// 找到后记录到 inserts 并标记 assigned，返回 true；未找到返回 false。
func (r *RubyRestorer) findAndInsert(target, base, text string, assigned map[int]bool, inserts *[]insertInfo) bool {
	searchFrom := 0
	for {
		idx := strings.Index(target[searchFrom:], base)
		if idx == -1 {
			break
		}
		absIdx := searchFrom + idx
		if !assigned[absIdx] {
			assigned[absIdx] = true
			*inserts = append(*inserts, insertInfo{
				pos:  absIdx,
				end:  absIdx + len(base),
				base: base,
				text: text,
			})
			return true
		}
		searchFrom = absIdx + 1
	}
	return false
}

// inlineMarkerRe 匹配 ⟦ruby:base/text⟧ 格式的内联标记。
var inlineMarkerRe = regexp.MustCompile(`⟦ruby:([^/⟧]+)/([^⟧]+)⟧`)

// restoreInlineMarkers 通过正则替换将内联标记还原为 <ruby> 标签。
//
// ⟦ruby:base/text⟧ → <ruby>base<rt>text</rt></ruby>
func (r *RubyRestorer) restoreInlineMarkers(seg *model.Segment) error {
	seg.Target = inlineMarkerRe.ReplaceAllStringFunc(seg.Target, func(match string) string {
		m := inlineMarkerRe.FindStringSubmatch(match)
		if len(m) < 3 {
			return match
		}
		return fmt.Sprintf("<ruby>%s<rt>%s</rt></ruby>", m[1], m[2])
	})
	return nil
}
