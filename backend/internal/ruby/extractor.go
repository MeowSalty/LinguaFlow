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
	"unicode"
	"unicode/utf8"
)

// rubyElementRe 匹配 <ruby>BASE<rt>READING</rt>TRAILING</ruby>
// 其中 BASE 可能包含 <rp> 等辅助标签，READING 是注音文本，
// TRAILING 可能包含 </rp> 等辅助标签。
var rubyElementRe = regexp.MustCompile(`<ruby>(.*?)<rt>(.*?)</rt>(.*?)</ruby>`)

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

// Extract 从源文本中提取所有 ruby 注音条目并剥离 ruby 标签。
//
// 行为：
//  1. 提取所有 <ruby> 元素的基底文本和注音
//  2. 合并相邻的 per-kanji ruby 为词级注音
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

// extractRubyMatches 从源文本中提取所有 ruby 元素及其位置。
func extractRubyMatches(source string) []rubyMatch {
	locs := rubyElementRe.FindAllStringSubmatchIndex(source, -1)
	if len(locs) == 0 {
		return nil
	}

	matches := make([]rubyMatch, 0, len(locs))
	for _, loc := range locs {
		// loc[0], loc[1]: full match start/end
		// loc[2], loc[3]: group 1 (base) start/end
		// loc[4], loc[5]: group 2 (text) start/end
		base := source[loc[2]:loc[3]]
		text := source[loc[4]:loc[5]]

		// 从基底文本中清理辅助标签（如 <rp>, <rb>）
		base = htmlTagRe.ReplaceAllString(base, "")

		matches = append(matches, rubyMatch{
			annotation: annotation{
				Base: base,
				Text: text,
			},
			start: loc[0],
			end:   loc[1],
		})
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

// StripRubyTags 剥离 <ruby>/<rt> 标签，只保留基底文本。
// 清理基底文本和尾部文本中的辅助标签（如 <rp>, <rb>；仅删标签本身，
// 其回退文本内容保留）。本函数是全仓注音剥离的单一来源：ruby.Extract、
// pipeline 的定向对齐 prompt 与 qa.LengthRatioChecker 均复用本实现，
// 避免多处正则副本漂移导致源/译剥离语义不一致。
func StripRubyTags(source string) string {
	return rubyElementRe.ReplaceAllStringFunc(source, func(match string) string {
		m := rubyElementRe.FindStringSubmatch(match)
		base := m[1]
		trailing := m[3]
		// 清理基底文本和尾部文本中的辅助标签（如 <rp>, <rb>）
		base = htmlTagRe.ReplaceAllString(base, "")
		trailing = htmlTagRe.ReplaceAllString(trailing, "")
		return base + trailing
	})
}
