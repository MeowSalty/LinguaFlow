package protect

import (
	"regexp"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// RubyProtector 保护 HTML ruby 注音标签 <rt> 内的文本内容。
//
// 日语 EPUB 中 <ruby> 标签用于在汉字上方显示假名注音（furigana），
// 例如 <ruby>呪<rt>じゅ</rt></ruby>。其中 <rt> 内的假名不应被翻译。
//
// 本保护器仅替换 <rt>...</rt> 中的文本内容为占位符，
// 保留 <rt> 和 </rt> 标签本身，以便后续 XMLProtector 处理标签。
//
// 典型流程：
//
//	输入:  <ruby>呪<rt>じゅ</rt></ruby>
//	保护后: <ruby>呪<rt>__LF_000001__</rt></ruby>
//	XMLProtector 保护标签后: __LF_000002__呪__LF_000003____LF_000001____LF_000004__
//	翻译后还原: <ruby>诅<rt>じゅ</rt></ruby>
type RubyProtector struct{}

func (RubyProtector) Name() string { return "ruby" }

// rubyRtRe 匹配 <rt>content</rt>，捕获纯文本内容（不含嵌套标签）。
var rubyRtRe = regexp.MustCompile(`<rt>([^<]*)</rt>`)

func (p *RubyProtector) Protect(seg *pipeline.Segment) error {
	seg.Source = rubyRtRe.ReplaceAllStringFunc(seg.Source, func(match string) string {
		m := rubyRtRe.FindStringSubmatch(match)
		k := nextKey(seg)
		seg.Protected[k] = m[1]     // 保护注音文本内容
		return "<rt>" + k + "</rt>" // 保留标签，内容替换为占位符
	})
	return nil
}

func (p *RubyProtector) Unprotect(seg *pipeline.Segment) error {
	seg.Target = restoreAll(seg.Target, seg.Protected)
	return nil
}
