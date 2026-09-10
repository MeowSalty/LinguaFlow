package qa

import (
	"context"
	"strings"
	"unicode"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// UntranslatedChecker 检测未翻译的段落（source == target）。
// severity 按语言对三分：源语独有脚本出现于译文时为 error，否则降为 warning
// （源语与目标语共用文字系统时，同形文本可能是有意保留的专有名词/原文片段，
// 交由裁决轮/语义轮降噪）。语言对无法解析时保守报 error。
type UntranslatedChecker struct {
	sourceOnly    []*unicode.RangeTable
	langsResolved bool
}

// NewUntranslatedChecker 创建一个未翻译检测器。语言对解析失败时不报错，
// Check 内按保守策略（error）处理——source_lang 默认即 "auto"，降级等于
// 默认关掉整段回传的防线。
func NewUntranslatedChecker(srcLang, tgtLang string) *UntranslatedChecker {
	sourceOnly, ok := sourceOnlyScripts(srcLang, tgtLang)
	return &UntranslatedChecker{
		sourceOnly:    sourceOnly,
		langsResolved: ok,
	}
}

func (c *UntranslatedChecker) Name() string { return CheckUntranslated }

func (c *UntranslatedChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		rawTgt := strings.TrimSpace(seg.TargetText)
		// 相等比较用基底形态：LLM 原样回传 ruby 剥离形态时 译文 == strip(原文)，
		// 但与含标签的原文精确比较永不相等，检测会被绕过。复用 ruby.StripRubyTags
		// 单一来源（与 LengthRatioChecker 同口径）；span 定位仍用原始译文形态，
		// MatchedText 必须是用户可见的真实译文，不能泄漏剥离出的标签形态。
		src := strings.TrimSpace(ruby.StripRubyTags(seg.SourceText))
		tgt := strings.TrimSpace(ruby.StripRubyTags(seg.TargetText))
		if src == "" || tgt == "" {
			continue
		}
		if src != tgt {
			continue
		}
		// 豁免判定剥离占位符 token：整段只有一个被保护的 URL/标签时，剩余文本
		// 无字母即无需翻译。相等判定仍用完整文本（相同则占位符必然也相同）。
		if isExempt(stripPlaceholderTokens(src)) {
			continue
		}
		severity, message := untranslatedVerdict(c, tgt)
		span := LocateSpan(seg.TargetText, rawTgt)
		if span == nil {
			span = &Span{MatchedText: rawTgt}
		}
		issues = append(issues, QualityIssue{
			SegmentIndex: seg.Index,
			Severity:     severity,
			Code:         CheckUntranslated,
			Message:      message,
			Span:         span,
		})
	}
	return issues
}

// untranslatedVerdict 对确认 src==tgt 且未豁免的段落给出 severity 与 message。
//
// 三分的理由：ja→zh 的纯汉字标题（如「北海道大学」）在确定性层面与正确翻译
// 不可区分——逐字保留日文汉字本就是常见且合理的译法，报 error 会误伤自动
// reject；嵌入的英文 token 同理。这类段落只能交给裁决轮/语义轮判断是否有意
// 保留。而源语独有脚本（假名、谚文、西里尔等）出现在回传文本中则是真回传的
// 强证据，必须 error。语言对不可知（auto/未知）时同样报 error：source_lang
// 默认值就是 "auto"，此时若降为 warning 等于默认关掉整段回传的防线。
func untranslatedVerdict(c *UntranslatedChecker, tgt string) (IssueSeverity, string) {
	if !c.langsResolved || containsAnyScript(tgt, c.sourceOnly) {
		return SeverityError, "译文与原文相同"
	}
	return SeverityWarning, "译文与原文相同（源语与目标语共用文字系统，可能为有意保留的专有名词或原文片段）"
}

// isExempt 检查文本是否属于豁免类型（纯数字、纯标点、纯占位符）。
// 调用方应先剥离占位符 token：__LF_ 本身含字母，不剥离会令"占位符+纯数字"
// 这类无需翻译的内容漏判。
func isExempt(text string) bool {
	if text == "" {
		return true
	}
	for _, r := range text {
		if unicode.IsLetter(r) {
			return false
		}
	}
	return true
}
