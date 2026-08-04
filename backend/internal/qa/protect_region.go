package qa

import (
	"sort"
	"strings"
	"unicode/utf8"
)

func ProtectedRegions(target string, protected map[string]string) [][2]int {
	if len(protected) == 0 || target == "" {
		return nil
	}
	raw := make([][2]int, 0, len(protected))
	for _, value := range protected {
		if value == "" {
			continue
		}
		// 同一保护值可能在译文中重复出现（如多个 <br/> 或相同 URL），
		// 需找出全部出现位置，否则未屏蔽的副本仍会被 checker 误报。
		searchStart := 0
		for searchStart <= len(target) {
			rel := strings.Index(target[searchStart:], value)
			if rel < 0 {
				break
			}
			idx := searchStart + rel
			start := utf8RuneOffset(target, idx)
			end := start + utf8.RuneCountInString(value)
			raw = append(raw, [2]int{start, end})
			searchStart = idx + len(value)
		}
	}
	if len(raw) == 0 {
		return nil
	}
	sort.Slice(raw, func(i, j int) bool {
		// 同起点时长区域在前：保证前缀关系（如 <br> 与 <br/>）中较长区域被保留，
		// 避免 unstable 排序叠加 map 随机迭代顺序导致非确定性漏屏蔽。
		if raw[i][0] != raw[j][0] {
			return raw[i][0] < raw[j][0]
		}
		return raw[i][1] > raw[j][1]
	})
	out := make([][2]int, 0, len(raw))
	for _, r := range raw {
		if len(out) == 0 {
			out = append(out, r)
			continue
		}
		last := &out[len(out)-1]
		if r[0] >= last[1] {
			// 不重叠：新增独立区域。
			out = append(out, r)
		} else if r[1] > last[1] {
			// 重叠且向后延伸：扩展为并集，吞并被覆盖的尾部内容。
			last[1] = r[1]
		}
		// 否则被既有区域完全包含，丢弃。
	}
	return out
}

func StripRegions(target string, regions [][2]int) string {
	if len(regions) == 0 {
		return target
	}
	runes := []rune(target)
	var result []rune
	pos := 0
	for _, r := range regions {
		start, end := r[0], r[1]
		if start > pos {
			result = append(result, runes[pos:start]...)
		}
		if end > pos {
			pos = end
		}
	}
	if pos < len(runes) {
		result = append(result, runes[pos:]...)
	}
	return string(result)
}

// LocateSpanExcludingRegions 在 target 中定位 matchedText 的首个「落在保护区之外」的出现位置，
// 返回带 rune 偏移的 Span。regions 为 ProtectedRegions 的输出（rune 边界、升序、已合并）。
// 当 matchedText 的所有出现都被保护区覆盖、或 regions 为空时，回退到普通 LocateSpan 语义，
// 保证不会因为无法在保护区外定位而丢弃 issue（宁可偏移不准，也不漏报）。
//
// 用于修复「hit 在 cleanTgt 检出、却用 LocateSpan(tgt) 命中保护区内同字符」导致 span 指错的问题。
func LocateSpanExcludingRegions(target, matchedText string, regions [][2]int) *Span {
	matchedText = strings.TrimSpace(matchedText)
	if matchedText == "" {
		return nil
	}
	span := &Span{MatchedText: matchedText}
	if len(regions) == 0 {
		return locateSpanFull(target, matchedText, span)
	}
	// 在 target 的字节空间逐次查找 matchedText，转换到 rune 偏移后判断是否落在保护区外。
	needle := matchedText
	searchStart := 0
	for searchStart <= len(target) {
		rel := strings.Index(target[searchStart:], needle)
		if rel < 0 {
			break
		}
		idx := searchStart + rel
		start := utf8RuneOffset(target, idx)
		end := start + utf8.RuneCountInString(target[idx:idx+len(needle)])
		if !regionCovers(regions, start, end) {
			span.TargetStart = &start
			span.TargetEnd = &end
			return span
		}
		searchStart = idx + len(needle)
	}
	// 全部出现都在保护区内：回退为首次出现的偏移（含 equalFold 兜底），偏移可能不准但不漏报。
	return locateSpanFull(target, matchedText, span)
}

// regionCovers 判断 [start,end) 是否被任一合并后的保护区覆盖（部分重叠即视为覆盖）。
func regionCovers(regions [][2]int, start, end int) bool {
	for _, r := range regions {
		if start < r[1] && end > r[0] {
			return true
		}
	}
	return false
}

// locateSpanFull 复用 LocateSpan 的等值/大小写不敏感匹配语义填充偏移。
func locateSpanFull(target, matchedText string, span *Span) *Span {
	idx := strings.Index(target, matchedText)
	endByte := idx + len(matchedText)
	if idx < 0 {
		idx2, endByte2, ok := equalFoldSpan(target, matchedText)
		if !ok {
			return span
		}
		idx, endByte = idx2, endByte2
		matchedText = target[idx:endByte]
		span.MatchedText = matchedText
	}
	start := utf8RuneOffset(target, idx)
	end := start + utf8.RuneCountInString(target[idx:endByte])
	span.TargetStart = &start
	span.TargetEnd = &end
	return span
}
