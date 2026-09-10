// Package ruby 提供 HTML ruby 注音标签的提取、剥离与还原的纯变换。
//
// Ruby 标签用于在汉字上方显示注音（如假名 furigana），例如 <ruby>呪<rt>じゅ</rt></ruby>。
// 本包是零状态、零 model 依赖的纯函数域包：单一事实源是文本中的 HTML 标签本身——
// 提取（Extract）从源文读出注音条目并剥离标签，剥离（StripRubyTags）统一全仓的
// 注音移除口径，还原（RestoreItems/RestoreInlineMarkers）把条目重新插回译文。
// 注音元数据的落库（seg.Meta["ruby_items"]）与段落状态由调用方
// （protect.NewRubyProtector / pipeline）负责，本包不感知 Segment。
package ruby

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// rubyElementSpanRe 定位 <ruby>…</ruby> 元素跨度（group 1 = 元素内内容）。
// 元素内可能含多对 <rt>（HTML 规范允许、日文 EPUB 常见的多段注音写法，
// 如 <ruby>何<rt>な</rt>故<rt>ぜ</rt></ruby>），由 rubyRTSpanRe 在元素内逐对扫描。
var rubyElementSpanRe = regexp.MustCompile(`<ruby>(.*?)</ruby>`)

// rubyRTSpanRe 在 ruby 元素内扫描完整 <rt>注音</rt> 跨度（group 1 = 注音文本）。
var rubyRTSpanRe = regexp.MustCompile(`<rt>(.*?)</rt>`)

// htmlTagRe 匹配 HTML/XML 标签，用于从基底文本中清理辅助标签。
var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// annotation 是注音条目（基底文本 + 标注文本）。
type annotation struct {
	Base string // 基底文本（可能跨多个 ruby 元素合并）
	Text string // 标注文本（合并后的完整文本）
}

// rubyMatch 跟踪 ruby 元素在源文本中的位置和内容。
type rubyMatch struct {
	annotation
	start int // 在源文本中的字节偏移
	end   int // 在源文本中的字节偏移结束
}

// rtSpan 是 ruby 元素内一个完整 <rt>…</rt> 注音跨度的绝对字节偏移。
type rtSpan struct {
	start     int // `<rt>` 标签起点
	end       int // `</rt>` 标签终点
	textStart int // 注音文本起点（`<rt>` 之后）
	textEnd   int // 注音文本终点（`</rt>` 之前）
}

// rubyElement 是一个 <ruby>…</ruby> 元素的扫描结果。
// 无任何完整 rt 对的元素（如 <ruby>漢</ruby>、rt 未闭合）在 scanRubyElements
// 内统一过滤，不进入扫描结果。
type rubyElement struct {
	start int      // 元素起点（含 `<ruby>`）
	end   int      // 元素终点（含 `</ruby>`）
	rts   []rtSpan // 元素内全部完整 rt 跨度（升序）
}

// Extract 从源文本中提取所有 ruby 注音条目并剥离 ruby 标签。
//
// 行为：
//  1. 提取所有 <ruby> 元素中每一对 基底+注音（多段 <rt> 的元素逐对产出，
//     与等价的相邻独立元素语义一致）
//  2. 合并相邻的 per-kanji ruby 为词级注音（多段 rt 元素内的相邻对同样合并）
//  3. 剥离 ruby 标签，只保留基底文本
//
// 返回按出现顺序排列的条目（ID 经 AssignIDs 分配为段内稳定序号）与剥离后的文本；
// 无 ruby 元素时 items 为 nil、文本原样返回（正则不命中即返回，快速路径天然高效）。
func Extract(source string) (items []Item, stripped string) {
	// 1. 提取所有 ruby 元素的元数据（含位置信息）
	matches := extractRubyMatches(source)

	// 2. 合并相邻 per-kanji ruby 为词级注音
	merged := mergeAdjacentRuby(matches)

	// 3. 剥离 ruby 标签，只保留基底文本
	stripped = StripRubyTags(source)

	if len(merged) == 0 {
		return nil, stripped
	}
	items = make([]Item, len(merged))
	for i, a := range merged {
		items[i] = Item{SourceBase: a.Base, SourceText: a.Text}
	}
	return AssignIDs(items), stripped
}

// scanRubyElements 定位 source 中所有 <ruby>…</ruby> 元素跨度及其内部全部完整
// <rt>…</rt> 注音跨度（偏移已换算为 source 绝对字节偏移、升序），只返回含至少
// 一对完整 rt 的元素。「哪些元素会被处理」的判定在此单一收口，提取
// （extractRubyMatches）、剥离（StripRubyTags）与区域定位（ElementSpans）直接
// 消费，无需各自重复过滤。
// 不带 (?s)：`.` 不匹配换行，跨行元素不命中（与历史行为一致）。
func scanRubyElements(source string) []rubyElement {
	els := rubyElementSpanRe.FindAllStringSubmatchIndex(source, -1)
	if len(els) == 0 {
		return nil
	}
	out := make([]rubyElement, 0, len(els))
	for _, el := range els {
		// el[0], el[1]: 元素整体跨度；el[2], el[3]: group 1（元素内内容）
		innerStart, innerEnd := el[2], el[3]
		inner := source[innerStart:innerEnd]
		var rts []rtSpan
		for _, loc := range rubyRTSpanRe.FindAllStringSubmatchIndex(inner, -1) {
			// 内层下标相对 inner，须加 innerStart 换算回 source 绝对偏移
			rts = append(rts, rtSpan{
				start:     innerStart + loc[0],
				end:       innerStart + loc[1],
				textStart: innerStart + loc[2],
				textEnd:   innerStart + loc[3],
			})
		}
		if len(rts) == 0 {
			continue // 无完整 rt 对的元素不参与任何后续处理（提取/剥离/区域定位均跳过）
		}
		out = append(out, rubyElement{start: el[0], end: el[1], rts: rts})
	}
	return out
}

// extractRubyMatches 从源文本中提取所有 ruby 元素的每对 基底+注音 及其位置。
//
// 多段 <rt> 的元素（如 <ruby>何<rt>な</rt>故<rt>ぜ</rt></ruby>）逐对产出，
// 语义与相邻独立元素 <ruby>何<rt>な</rt></ruby><ruby>故<rt>ぜ</rt></ruby> 一致：
// 每对的 span 平铺整个元素（首对起点含 <ruby> 前缀，末对终点含 </ruby> 及
// 尾部 <rp> 等辅助内容，中间对衔接上一对 </rt> 的结束位置），保证
// mergeAdjacentRuby 的「紧邻」判定在元素内与跨元素间口径一致。
// 无任何完整 rt 对的元素已在 scanRubyElements 内过滤，不产出匹配。
func extractRubyMatches(source string) []rubyMatch {
	elements := scanRubyElements(source)
	matches := make([]rubyMatch, 0, len(elements))
	for _, el := range elements {
		for i, rt := range el.rts {
			// 本对起点 = 元素起点（首对，含 <ruby> 前缀）或上一对 </rt> 终点
			//（中间对）：基底切片与匹配 span 共用同一边界
			start := el.start
			if i > 0 {
				start = el.rts[i-1].end
			}
			base := htmlTagRe.ReplaceAllString(source[start:rt.start], "")

			// span 覆盖本对的基底 + 注音，并向元素两端延伸：
			// 末对终点 = 元素终点，中间对终点 = 本对 </rt> 终点
			end := el.end
			if i < len(el.rts)-1 {
				end = rt.end
			}

			matches = append(matches, rubyMatch{
				annotation: annotation{
					Base: base,
					Text: source[rt.textStart:rt.textEnd],
				},
				start: start,
				end:   end,
			})
		}
	}
	return matches
}

// mergeAdjacentRuby 合并相邻的 per-kanji ruby 为词级注音。
//
// 合并规则：
//   - 当前 ruby 的基底是单个汉字（per-kanji）
//   - 下一个 ruby 紧邻（无分隔字符）
//   - 下一个 ruby 也是 per-kanji
//
// 不合并的情况：
//   - 基底包含多个字符（如 <ruby>項垂<rt>うなだ</rt></ruby>）
//   - 两个 ruby 之间有文本分隔
//   - 两个 ruby 之间有空白/标点
func mergeAdjacentRuby(matches []rubyMatch) []annotation {
	if len(matches) == 0 {
		return nil
	}

	var result []annotation
	i := 0
	for i < len(matches) {
		if isPerKanji(matches[i].Base) {
			// 尝试向后合并相邻的 per-kanji ruby
			merged := matches[i].annotation
			j := i + 1
			for j < len(matches) {
				// 检查是否紧邻（无分隔字符）
				if matches[j].start != matches[j-1].end {
					break
				}
				// 检查下一个是否也是 per-kanji
				if !isPerKanji(matches[j].Base) {
					break
				}
				// 合并
				merged.Base += matches[j].Base
				merged.Text += matches[j].Text
				j++
			}
			result = append(result, merged)
			i = j
		} else {
			result = append(result, matches[i].annotation)
			i++
		}
	}
	return result
}

// isPerKanji 检查基底文本是否为单个汉字。
func isPerKanji(base string) bool {
	r, size := utf8.DecodeRuneInString(base)
	if r == utf8.RuneError || size != len(base) {
		return false // 不是单个 rune，或包含无效 UTF-8
	}
	return unicode.Is(unicode.Han, r)
}

// ElementSpans 返回 s 中所有 <ruby>…</ruby> 元素的字节跨度 [start, end)
// （升序、不重叠）；无完整 rt 对的元素已在 scanRubyElements 内过滤。qa 区域
// 屏蔽与包内提取/剥离共用本函数，作为「哪些元素会被处理」的单一事实源。
func ElementSpans(s string) [][2]int {
	elements := scanRubyElements(s)
	spans := make([][2]int, 0, len(elements))
	for _, el := range elements {
		spans = append(spans, [2]int{el.start, el.end})
	}
	if len(spans) == 0 {
		return nil
	}
	return spans
}

// StripRubyTags 剥离 <ruby>/<rt> 标签，只保留基底文本。元素内删除全部完整
// <rt>…</rt> 跨度（标签与注音内容一起删），其余辅助标签（如 <rp>, <rb>；
// 仅删标签本身，其回退文本内容保留）一并清理。无任何完整 rt 对的元素已在
// scanRubyElements 内过滤，不经本函数改写、原样保留（含标签）。多段 <rt>
// 元素的结果与等价的相邻独立元素逐字一致。
// 本函数是全仓注音剥离的单一来源：ruby.Extract、pipeline 的定向对齐 prompt
// 与 qa.LengthRatioChecker 均复用本实现，避免多处正则副本漂移导致源/译剥离
// 语义不一致。
func StripRubyTags(source string) string {
	elements := scanRubyElements(source)
	if len(elements) == 0 {
		return source
	}
	var b strings.Builder
	b.Grow(len(source))
	cursor := 0
	for _, el := range elements {
		b.WriteString(source[cursor:el.start])
		// 元素内：删除全部完整 rt 跨度后，对剩余部分剥辅助标签
		pos := el.start
		for _, rt := range el.rts {
			b.WriteString(htmlTagRe.ReplaceAllString(source[pos:rt.start], ""))
			pos = rt.end
		}
		b.WriteString(htmlTagRe.ReplaceAllString(source[pos:el.end], ""))
		cursor = el.end
	}
	b.WriteString(source[cursor:])
	return b.String()
}
