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
	return mergeRegions(protectedOccurrences(target, protected))
}

// protectedOccurrences 在 target 中找出每个 protected 值的全部出现位置（字节→rune 偏移），
// 返回尚未排序合并的原始区域。同一保护值可能在译文中重复出现（如多个 <br/> 或相同 URL），
// 必须全部记录，否则未屏蔽的副本仍会被 checker 误报。
func protectedOccurrences(target string, protected map[string]string) [][2]int {
	raw := make([][2]int, 0, len(protected))
	for _, value := range protected {
		if value == "" {
			continue
		}
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
	return raw
}

// rubyRegions 在 target 中找出所有 <ruby>BASE<rt>READ</rt>TRAILING</ruby> 元素的位置（字节→rune 偏移），
// 返回尚未排序合并的原始区域。每个 ruby 元素整体作为一个区域（含基底文本），以便后续
// StripRegions 整段删去，避免 ruby 标签的 <> 被当半角标点误报。
//
// 复用包级 rubyElementRe（与 StripRubyTags 同一正则，由 TestStripRubyTagsMatchesRubyPackage 守护）。
// 因 model → qa 依赖成环，qa 不能反向 import ruby，故直接复用同包正则，不做跨包复制。
func rubyRegions(target string) [][2]int {
	matches := rubyElementRe.FindAllStringSubmatchIndex(target, -1)
	if len(matches) == 0 {
		return nil
	}
	raw := make([][2]int, 0, len(matches))
	for _, loc := range matches {
		startByte, endByte := loc[0], loc[1]
		start := utf8RuneOffset(target, startByte)
		end := start + utf8.RuneCountInString(target[startByte:endByte])
		raw = append(raw, [2]int{start, end})
	}
	return raw
}

// tagRegions 是通用内联标签屏蔽通道：在无 Protected 映射（如手动编辑从 DB 重载）时
// 兜底屏蔽 `<a href="x">`、`<b>` 等裸标签，使标记字符（`<`/`>`/`"`/`=`）不被 width_mix
// 等标点类 checker 当成正文误报。复用同包 htmlTagRe（与 StripRubyTags 同正则，语义一致）。
// 返回尚未排序合并的原始区域（字节→rune 偏移），无匹配返回 nil。
func tagRegions(target string) [][2]int {
	matches := htmlTagRe.FindAllStringIndex(target, -1)
	if len(matches) == 0 {
		return nil
	}
	raw := make([][2]int, 0, len(matches))
	for _, loc := range matches {
		startByte, endByte := loc[0], loc[1]
		start := utf8RuneOffset(target, startByte)
		end := start + utf8.RuneCountInString(target[startByte:endByte])
		raw = append(raw, [2]int{start, end})
	}
	return raw
}

// InlineMarkupRegions 返回 target 中「Protected 区 ∪ ruby 元素区 ∪ 内联标签区」的 rune 偏移并集
// （升序、已合并），作为 QA 层统一的内联标记屏蔽出口。
//
// 覆盖三条保护通道：
//   - protect 通道（XMLProtector）把 span 等保护片段写入 seg.Protected 映射；
//   - ruby 通道（ruby.Restorer 在 Unprotect 之后把 <ruby> 插回 seg.Target），不进 seg.Protected；
//   - 标签通道（htmlTagRe）兜底屏蔽裸 `<tag>` 标签，覆盖 Protected 映射缺失的手动编辑等场景。
//
// 消费者一律 regions := InlineMarkupRegions(text, seg.Protected); clean := StripRegions(text, regions)，
// span 定位继续用 LocateSpanExcludingRegions(原文, hit, regions)（regions 基于原文，偏移正确）。
// 空输入返回 nil（与 ProtectedRegions 一致），StripRegions(text, nil) == text。
func InlineMarkupRegions(target string, protected map[string]string) [][2]int {
	raw := protectedOccurrences(target, protected)
	raw = append(raw, rubyRegions(target)...)
	raw = append(raw, tagRegions(target)...)
	if len(raw) == 0 {
		return nil
	}
	return mergeRegions(raw)
}

// StripProtectedRegions returns text with inline markup regions (protected spans
// ∪ ruby elements) and __LF_* placeholders removed, then lower-cased — i.e. the
// "clean" text the QA checkers operate on. It is the single shared helper that
// both the punctuation_missing checker and the correct rules use, so the rule's
// notion of "source/target without protected regions" can never drift from the
// checker's. Mirrors stripPlaceholders(StripRegions(t, InlineMarkupRegions(t, p))).
func StripProtectedRegions(text string, protected map[string]string) string {
	return stripPlaceholders(StripRegions(text, InlineMarkupRegions(text, protected)))
}

// StripProtectedRegionsWithRegions is the same as StripProtectedRegions but
// accepts pre-computed regions (from InlineMarkupRegions), avoiding a redundant
// re-scan on hot paths where the caller already computed regions for span
// locating (e.g. PunctuationMissingChecker).
func StripProtectedRegionsWithRegions(text string, regions [][2]int) string {
	return stripPlaceholders(StripRegions(text, regions))
}

// mergeRegions 对 rune 偏移区域排序并合并重叠区域，返回升序、已合并的并集。
// 同起点时长区域在前：保证前缀关系（如 <br> 与 <br/>）中较长区域被保留，
// 避免 unstable 排序叠加调用方输入顺序导致非确定性漏屏蔽。
// 注意：本函数会就地排序入参切片，调用方应传入自有切片。
func mergeRegions(in [][2]int) [][2]int {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i][0] != in[j][0] {
			return in[i][0] < in[j][0]
		}
		return in[i][1] > in[j][1]
	})
	out := make([][2]int, 0, len(in))
	for _, r := range in {
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
