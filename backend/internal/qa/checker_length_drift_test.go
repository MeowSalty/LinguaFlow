package qa_test

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// TestStripRubyTagsMatchesRubyPackage 是跨包漂移守护测试。
//
// qa.LengthRatioChecker 因架构依赖（model → qa：model.Segment.Issues 用
// qa.QualityIssue）无法反向导入 ruby 包，故在本地复制了 StripRubyTags
// 实现。本测试直接对比 qa.StripRubyTags 与权威实现 ruby.StripRubyTags
// 的输出：一旦 ruby 包的正则演进而 qa 副本未同步，此测试即失败，避免
// 源/译剥离语义不对称重新引入长度比误报。
func TestStripRubyTagsMatchesRubyPackage(t *testing.T) {
	// 覆盖 ruby.StripRubyTags 注释中声明的全部形态：单/多 ruby、
	// 尾部文本、辅助标签 <rp>/<rb>、无 ruby、空串、混合正文、长译文。
	cases := []string{
		"",
		"呪術廻戦",
		"<ruby>呪<rt>じゅ</rt></ruby>",
		"<ruby>呪<rt>じゅ</rt></ruby>術",
		"<ruby>呪<rt>じゅ</rt></ruby><ruby>術<rt>じゅつ</rt></ruby>",
		"<ruby>呪<rp>(</rp><rt>じゅ</rt><rp>)</rp></ruby>",
		"<ruby><rb>呪</rb><rt>じゅ</rt></ruby>",
		"前文<ruby>呪<rt>じゅ</rt></ruby>后文",
		"<ruby>咒术回战<rt>じゅじゅつかいせん</rt></ruby>动画",
	}

	for _, in := range cases {
		got := qa.StripRubyTags(in)
		want := ruby.StripRubyTags(in)
		if got != want {
			t.Errorf("qa.StripRubyTags(%q) = %q, ruby.StripRubyTags = %q (drift detected)", in, got, want)
		}
	}
}
