package ruby

import (
	"strconv"
	"strings"
)

// Item 是统一注音条目结构，取代 ruby_annotations 与 ruby_output 两个数组。
// 提取阶段填 ID/SourceBase/SourceText；LLM 回填 TargetBase/TargetText/Kind；
// 还原阶段按 ID 显式关联（无 ID 回退位置，向后兼容旧后端/漏返）。
type Item struct {
	ID         string `json:"id"`                    // 段内稳定序号，如 "1","2"（提取时分配）
	SourceBase string `json:"source_base,omitempty"` // 原文基底（ruby 标签剥离后的原文）
	SourceText string `json:"source_text,omitempty"` // 原文注音
	TargetBase string `json:"target_base,omitempty"` // LLM 回填：译文中的对应文本
	TargetText string `json:"target_text,omitempty"` // LLM 回填：译文注音
	Kind       string `json:"kind,omitempty"`        // "phonetic" | "semantic" | "creative"
	Aligned    bool   `json:"-"`                     // 运行态：是否已对齐（不外发/不持久化）
}

// Restorable 报告条目是否具备还原前提：TargetBase 或 SourceBase 非空。
// RestoreItems 的 total 计数与 revise 轮守卫的 want 口径共用本谓词，
// 保证「永不可还原条目」的判定单源，两侧口径不漂移。
func (it Item) Restorable() bool {
	return it.TargetBase != "" || it.SourceBase != ""
}

// ItemsByID 返回 item ID → *Item 的映射。ID 为空的条目以 "" 为键
// （罕见；键冲突时后写覆盖先写）。
func ItemsByID(items []Item) map[string]*Item {
	m := make(map[string]*Item, len(items))
	for i := range items {
		m[items[i].ID] = &items[i]
	}
	return m
}

// AssignIDs 为所有 ID 为空的条目按顺序分配段内序号 "1".."N"
// （strconv.Itoa(n)）。已有 ID 的条目保持不变。返回同一切片。
func AssignIDs(items []Item) []Item {
	next := 1
	for i := range items {
		if items[i].ID == "" {
			items[i].ID = strconv.Itoa(next)
			next++
		}
	}
	return items
}

// MergeByOutput 用 LLM 返回的 OutputEntry 回填 TargetBase/TargetText/Kind，
// 并置 Aligned = true。匹配优先级：
//  1. ID 匹配：非空 ID（trim 后）的条目匹配同 ID 的 item（首次出现优先）。
//  2. 位置回退：仅当 ID 为空（含纯空白，旧后端/LLM 漏返）时，按原顺序消费下一个未对齐 item。
//
// 重复/未知 ID 绝不崩溃，也绝不消费其它 item：重复 ID 首条生效，余者丢弃；
// 未知 ID 直接丢弃。被丢弃条目对应的 item 保持未对齐 → 进入定向重试候选。
// Base 为空的条目忽略。就地修改 items（按索引操作，切片元素可寻址）。
func MergeByOutput(items []Item, output []OutputEntry) {
	// item ID → 索引（重复 ID 取首个 item，保证确定性）
	idxByID := make(map[string]int, len(items))
	for i := range items {
		if items[i].ID == "" {
			continue
		}
		if _, dup := idxByID[items[i].ID]; dup {
			continue
		}
		idxByID[items[i].ID] = i
	}

	next := 0 // 位置回退游标：下一个未对齐 item 的索引
	for _, e := range output {
		if e.Base == "" {
			continue // 空 base 的条目忽略，不参与任何匹配
		}
		id := strings.TrimSpace(e.ID)
		if id != "" {
			// ID 匹配：仅当该 item 尚未对齐时生效（重复 ID 首条胜出）。
			// 命中已对齐 item（重复 ID）或未命中（未知 ID）时丢弃该条目，
			// 不走位置回退——否则会把它错配到不相关 item 并掩盖为已对齐，
			// 违背计划失败模式契约（未知/重复 id 应记 unaligned 而非消费 item）。
			if i, ok := idxByID[id]; ok && !items[i].Aligned {
				items[i].TargetBase = e.Base
				items[i].TargetText = e.Text
				items[i].Kind = e.Kind
				items[i].Aligned = true
			}
			continue
		}
		// 无 ID（含纯空白）→ 位置回退（向后兼容旧后端/漏返）
		for next < len(items) && items[next].Aligned {
			next++
		}
		if next >= len(items) {
			break // 无空闲 item，丢弃该条目
		}
		items[next].TargetBase = e.Base
		items[next].TargetText = e.Text
		items[next].Kind = e.Kind
		items[next].Aligned = true
		next++
	}
}

// Unaligned 返回所有 Aligned == false 的条目（定向重试的候选集合）。
func Unaligned(items []Item) []Item {
	var out []Item
	for _, it := range items {
		if !it.Aligned {
			out = append(out, it)
		}
	}
	return out
}
