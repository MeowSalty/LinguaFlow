package pipeline

import "github.com/MeowSalty/LinguaFlow/backend/internal/ruby"

// extractRubyItemsFromSeg 从 Segment.Meta 提取统一注音条目（[]ruby.Item）。
func extractRubyItemsFromSeg(seg *Segment) []ruby.Item {
	if raw, ok := seg.Meta["ruby_items"]; ok {
		if items, ok := raw.([]ruby.Item); ok {
			return items
		}
	}
	return nil
}

// kindSet 将 kind 列表转为 set，用于快速查找。
// nil（旧记录/未设置）时返回默认全集，保证向后兼容；
// 空非 nil 切片（用户显式传 []）返回空集，允许用户选择不保留任何注音。
func kindSet(kinds []string) map[string]bool {
	if kinds == nil {
		return map[string]bool{"phonetic": true, "semantic": true, "creative": true}
	}
	s := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		s[k] = true
	}
	return s
}
