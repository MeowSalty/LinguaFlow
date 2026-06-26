package protect

import (
	"regexp"
	"unicode"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// RubyProtector 保护 HTML ruby 注音标签，将注音元数据提取到 seg.Meta 中。
//
// 日语 EPUB 中 <ruby> 标签用于在汉字上方显示假名注音（furigana），
// 例如 <ruby>呪<rt>じゅ</rt></ruby>。
//
// Protect 阶段的行为：
//  1. 提取所有 <ruby> 元素的基底文本和注音
//  2. 合并相邻的 per-kanji ruby 为词级注音
//  3. 剥离 ruby 标签，只保留基底文本
//  4. 将注音元数据存入 seg.Meta["ruby_annotations"]
//
// Unprotect 阶段为空操作，注音还原委托给 RubyRestorer。
type RubyProtector struct{}

func (RubyProtector) Name() string { return "ruby" }

// rubyElementRe 匹配 <ruby>BASE<rt>READING</rt>TRAILING</ruby>
// 其中 BASE 可能包含 <rp> 等辅助标签，READING 是注音文本，
// TRAILING 可能包含 </rp> 等辅助标签。
var rubyElementRe = regexp.MustCompile(`<ruby>(.*?)<rt>(.*?)</rt>(.*?)</ruby>`)

// htmlTagRe 匹配 HTML/XML 标签，用于从基底文本中清理辅助标签。
var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// RubyAnnotation 是 protect 包内部的注音条目。
type RubyAnnotation struct {
	Base string // 基底文本（可能跨多个 ruby 元素合并）
	Text string // 标注文本（合并后的完整文本）
}

// rubyMatch 跟踪 ruby 元素在源文本中的位置和内容。
type rubyMatch struct {
	RubyAnnotation
	start int // 在源文本中的字节偏移
	end   int // 在源文本中的字节偏移结束
}

func (p *RubyProtector) Protect(seg *model.Segment) error {
	// 1. 提取所有 ruby 元素的元数据（含位置信息）
	matches := extractRubyMatches(seg.Source)

	// 2. 合并相邻 per-kanji ruby 为词级注音
	merged := mergeAdjacentRuby(matches)

	// 3. 剥离 ruby 标签，只保留基底文本
	seg.Source = stripRubyTags(seg.Source)

	// 4. 存入 seg.Meta
	if len(merged) > 0 {
		if seg.Meta == nil {
			seg.Meta = make(map[string]any)
		}
		seg.Meta["ruby_annotations"] = merged
	}

	return nil
}

func (p *RubyProtector) Unprotect(seg *model.Segment) error {
	// 不再需要还原占位符（Protect 阶段未使用占位符）
	// 注音还原委托给 RubyRestorer，在 unprotect stage 之后执行
	return nil
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
			RubyAnnotation: RubyAnnotation{
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
func mergeAdjacentRuby(matches []rubyMatch) []RubyAnnotation {
	if len(matches) == 0 {
		return nil
	}

	var result []RubyAnnotation
	i := 0
	for i < len(matches) {
		if isPerKanji(matches[i].Base) {
			// 尝试向后合并相邻的 per-kanji ruby
			merged := matches[i].RubyAnnotation
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
			result = append(result, matches[i].RubyAnnotation)
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

// stripRubyTags 剥离 <ruby>/<rt> 标签，只保留基底文本。
func stripRubyTags(source string) string {
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
