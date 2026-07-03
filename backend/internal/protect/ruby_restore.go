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

// RestoreResult 记录注音还原的匹配统计。
type RestoreResult struct {
	Matched int // 成功匹配并还原的注音条目数
	Total   int // 需要还原的注音条目总数（不含空 base 的条目）
}

// IsFull 返回是否全部匹配成功。
func (r RestoreResult) IsFull() bool {
	return r.Total > 0 && r.Matched == r.Total
}

// Restore 根据输出模式还原注音标签。
// rubyOutput 为过滤后的注音条目（由调用方负责提取和过滤）。
// originalAnnotations 为 Protect 阶段提取的原始注音，用于 ruby_output 模式的双源匹配回退；
// inline_markers 模式忽略此参数。可传 nil。
func (r *RubyRestorer) Restore(seg *model.Segment, rubyOutput []RubyOutputEntry, originalAnnotations []RubyAnnotation) (RestoreResult, error) {
	switch r.OutputFormat {
	case "inline_markers":
		return r.restoreInlineMarkers(seg, rubyOutput)
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
func (r *RubyRestorer) restoreRubyOutput(seg *model.Segment, rubyOutput []RubyOutputEntry, originalAnnotations []RubyAnnotation) (RestoreResult, error) {
	if len(rubyOutput) == 0 {
		return RestoreResult{}, nil
	}

	target := seg.Target

	// 记录已分配的字节位置
	assigned := make(map[int]bool)

	var inserts []insertInfo
	total := 0

	for i, entry := range rubyOutput {
		if entry.Base == "" {
			continue
		}
		total++
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
		return RestoreResult{Matched: 0, Total: total}, nil
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
	return RestoreResult{Matched: len(inserts), Total: total}, nil
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

// inlineMarkerRe 匹配 ⟦ruby:base/text⟧ 或 ⟦ruby:base/text/kind⟧ 格式的内联标记。
var inlineMarkerRe = regexp.MustCompile(`⟦ruby:([^/⟧]+)/([^/⟧]+)(?:/([^⟧]+))?⟧`)

// ParseInlineMarkers 从译文中提取所有内联标记，转换为 []RubyOutputEntry。
func ParseInlineMarkers(text string) []RubyOutputEntry {
	matches := inlineMarkerRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	entries := make([]RubyOutputEntry, 0, len(matches))
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		e := RubyOutputEntry{Base: m[1], Text: m[2]}
		if len(m) > 3 {
			e.Kind = m[3]
		}
		entries = append(entries, e)
	}
	return entries
}

// restoreInlineMarkers 通过正则替换将内联标记还原为 <ruby> 标签。
// rubyOutput 为过滤后的条目；还原匹配的标记，移除不匹配的标记。
func (r *RubyRestorer) restoreInlineMarkers(seg *model.Segment, rubyOutput []RubyOutputEntry) (RestoreResult, error) {
	// 先统计所有标记数
	allMatches := inlineMarkerRe.FindAllString(seg.Target, -1)
	if len(allMatches) == 0 {
		return RestoreResult{}, nil
	}
	if len(rubyOutput) == 0 {
		// 全部过滤掉，移除标记但保留基底
		seg.Target = inlineMarkerRe.ReplaceAllStringFunc(seg.Target, func(match string) string {
			m := inlineMarkerRe.FindStringSubmatch(match)
			if len(m) >= 3 {
				return m[1] // 保留基底
			}
			return match
		})
		return RestoreResult{Matched: 0, Total: len(allMatches)}, nil
	}
	// 构建匹配集合：base+text → 存在
	type pair struct{ base, text string }
	matchSet := make(map[pair]bool, len(rubyOutput))
	for _, e := range rubyOutput {
		if e.Base != "" {
			matchSet[pair{e.Base, e.Text}] = true
		}
	}
	total := len(allMatches)
	matched := 0
	seg.Target = inlineMarkerRe.ReplaceAllStringFunc(seg.Target, func(match string) string {
		m := inlineMarkerRe.FindStringSubmatch(match)
		if len(m) < 3 {
			return match
		}
		base, text := m[1], m[2]
		if matchSet[pair{base, text}] {
			matched++
			return fmt.Sprintf("<ruby>%s<rt>%s</rt></ruby>", base, text)
		}
		return base // 不在保留集合中，移除标记但保留基底
	})
	return RestoreResult{Matched: matched, Total: total}, nil
}
