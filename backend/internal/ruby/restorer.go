package ruby

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// Restorer 负责将 LLM 输出的注音信息还原为 <ruby> 标签。
// 它不是 Protector 接口的实现，而是作为 unprotect 之后的额外 stage。
type Restorer struct{}

// NewRestorer 创建 Restorer 实例。
func NewRestorer() *Restorer {
	return &Restorer{}
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

// RestoreItems 从统一 Item 结构还原 <ruby> 标签。
// 仅对 TargetBase 非空的条目尝试还原；第一优先用条目自身的 TargetBase
// 在译文中定位，未命中且 SourceBase 非空且不同于 TargetBase 时，
// 第二优先回退到该条目自身的 SourceBase——这是对旧路径（按位置取
// originals[i]）的核心修复。还原成功的条目置 Aligned = true（按索引
// 就地修改，调用方可观察）。
// total = 需要还原的条目数（TargetBase 或 SourceBase 非空）。
func (r *Restorer) RestoreItems(seg *model.Segment, items []Item) (RestoreResult, error) {
	if seg == nil || seg.Target == "" || len(items) == 0 {
		return RestoreResult{}, nil
	}

	assigned := make(map[int]bool)
	var inserts []insertInfo
	total := 0

	for i := range items {
		it := &items[i]
		if it.TargetBase == "" && it.SourceBase == "" {
			continue
		}
		total++
		if it.TargetBase == "" {
			continue
		}
		found := r.findAndInsert(seg.Target, it.TargetBase, it.TargetText, assigned, &inserts)
		if !found && it.SourceBase != "" && it.SourceBase != it.TargetBase {
			found = r.findAndInsert(seg.Target, it.SourceBase, it.TargetText, assigned, &inserts)
		}
		if found {
			it.Aligned = true
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
	target := seg.Target
	for _, ins := range inserts {
		rubyTag := fmt.Sprintf("<ruby>%s<rt>%s</rt></ruby>", ins.base, ins.text)
		target = target[:ins.pos] + rubyTag + target[ins.end:]
	}

	seg.Target = target
	return RestoreResult{Matched: len(inserts), Total: total}, nil
}

// OutputEntry 是 LLM 返回的单条标注输出。
type OutputEntry struct {
	Base string `json:"base"`
	Text string `json:"text"`
	Kind string `json:"kind"`         // "phonetic" | "semantic" | "creative"
	ID   string `json:"id,omitempty"` // 段内条目 id（可选：旧后端/LLM 漏返为空 → 回退位置）
}

// ValidKinds 是所有合法的注音 kind 值。
var ValidKinds = []string{"phonetic", "semantic", "creative"}

// NormalizeOutputEntries 对解析出的 OutputEntry 切片做 trim 与合法性过滤：
// trim Base/Text/Kind，丢弃 Base 为空的条目。供 repair（JSON 路径）与
// pipeline.parseAlignmentResponseText（text 协议路径）共用，避免过滤逻辑漂移。
// 以 `entries[:0]` 就地复用底层数组（与 prompt.Normalize* 语义一致）。
func NormalizeOutputEntries(entries []OutputEntry) []OutputEntry {
	out := entries[:0]
	for _, e := range entries {
		e.Base = strings.TrimSpace(e.Base)
		e.Text = strings.TrimSpace(e.Text)
		e.Kind = strings.TrimSpace(e.Kind)
		if e.Base == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// insertInfo 记录一次注音插入的位置和内容。
type insertInfo struct {
	pos  int
	end  int
	base string
	text string
}

// findAndInsert 在 target 中查找 base 的第一个未分配出现位置，
// 找到后记录到 inserts 并标记 assigned，返回 true；未找到返回 false。
func (r *Restorer) findAndInsert(target, base, text string, assigned map[int]bool, inserts *[]insertInfo) bool {
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

// sectionRubyLineRe 匹配 section 模式的 ruby 输出行：编号: content
// content 部分再通过 ParseSectionLine 从右解析 "base | text | kind [| id]"。
var sectionRubyLineRe = regexp.MustCompile(`^(\d+):\s*(.+)`)

// ParseInlineMarkers 从译文中提取所有内联标记，转换为 []OutputEntry。
func ParseInlineMarkers(text string) []OutputEntry {
	matches := inlineMarkerRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	entries := make([]OutputEntry, 0, len(matches))
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		e := OutputEntry{Base: m[1], Text: m[2]}
		if len(m) > 3 {
			e.Kind = m[3]
		}
		entries = append(entries, e)
	}
	return entries
}

// ParseSectionLine 从右解析单行 ruby 标注 "base | text | kind [| id]"。
// 3 字段（最右侧为合法 kind）或 4 字段（最右侧为 id，其左为 kind）。
// 返回 base/text/kind/id；text 为空、字段不足或无法判定时 ok=false。
// 供 ParseSectionRubyOutput（剥 "N:" 前缀后）与 pipeline.parseAlignmentResponseText
// （每行直接）共用，避免两套解析器的容错与 id 提取规则漂移。
func ParseSectionLine(content string) (base, text, kind, itemID string, ok bool) {
	itemIdx := strings.LastIndex(content, "|")
	if itemIdx < 0 {
		return "", "", "", "", false
	}
	rightmost := strings.TrimSpace(content[itemIdx+1:])
	if rightmost == "" {
		return "", "", "", "", false
	}
	// 最右侧是合法 kind → 3 字段行（无 item_id）
	if isValidKind(rightmost) {
		base, text, kind, ok = parseBaseTextKind(content)
		return base, text, kind, "", ok
	}
	// 否则优先按 4 字段解析：最右侧为 item_id，其余按 3 字段解析
	base, text, kind, ok = parseBaseTextKind(content[:itemIdx])
	if ok {
		return base, text, kind, rightmost, true
	}
	// 4 字段解析失败 → 回退 3 字段整体解析（兼容自定义 kind）
	base, text, kind, ok = parseBaseTextKind(content)
	return base, text, kind, "", ok
}

// parseBaseTextKind 从右解析恰好 3 个字段 "base | text | kind"。
func parseBaseTextKind(content string) (base, text, kind string, ok bool) {
	kindIdx := strings.LastIndex(content, "|")
	if kindIdx < 0 {
		return "", "", "", false
	}
	kind = strings.TrimSpace(content[kindIdx+1:])
	rest := content[:kindIdx]
	textIdx := strings.LastIndex(rest, "|")
	if textIdx < 0 {
		return "", "", "", false
	}
	base = strings.TrimSpace(rest[:textIdx])
	text = strings.TrimSpace(rest[textIdx+1:])
	if base == "" || text == "" {
		return "", "", "", false
	}
	return base, text, kind, true
}

// isValidKind 判断 kind 是否为合法值（phonetic/semantic/creative）。
func isValidKind(kind string) bool {
	for _, k := range ValidKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// ParseSectionRubyOutput 解析 section 模式的 [ruby] 段落。
// 输入为 [ruby] 之后的所有行（不含 [ruby] 标题行本身）。
// 格式：每行一条 "编号: base | text | kind[ | item_id]"。
// 行首的编号是段号（映射 key），不是条目 id；条目 id 仅来自可选的
// 第 4 个字段（4 字段优先，3 字段时为空 → 还原走位置回退）。
// 返回 segment ID → []OutputEntry 的映射。
func ParseSectionRubyOutput(lines []string) map[string][]OutputEntry {
	result := make(map[string][]OutputEntry)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := sectionRubyLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id := m[1] // 段号（map key），非条目 id
		base, text, kind, itemID, ok := ParseSectionLine(m[2])
		if !ok {
			continue
		}
		entry := OutputEntry{
			Base: base,
			Text: text,
			Kind: kind,
			ID:   itemID,
		}
		result[id] = append(result[id], entry)
	}
	return result
}

// RestoreInlineMarkers 通过正则替换将内联标记还原为 <ruby> 标签。
// 字节兼容：标记形态与还原结果保持不变。
// rubyOutput 为过滤后的条目；还原匹配的标记，移除不匹配的标记。
func (r *Restorer) RestoreInlineMarkers(seg *model.Segment, rubyOutput []OutputEntry) (RestoreResult, error) {
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
