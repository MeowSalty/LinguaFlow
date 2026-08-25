package correct

import (
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// PunctuationWrapLossWrapRule 机械恢复译文丢失的源文外层配对引号。仅在段落
// 携带 punctuation_wrap_loss issue 且源文为单一引号包裹 span 时生效。
// 译文边缘的引号 rune 保持不动，以防误伤。
type PunctuationWrapLossWrapRule struct{}

func (*PunctuationWrapLossWrapRule) Name() string { return RulePunctuationWrapLossWrap }

func (*PunctuationWrapLossWrapRule) ResolvedCodes() []string {
	return []string{qa.CheckPunctuationWrapLoss}
}

// edgeQuoteRunes 镜像 qa.quoteRunes（后者刻意未导出），本地显式维护，
// 避免 correct 依赖 qa 内部集合而随之漂移。改写后的译文边缘含引号 rune，
// 该检查同时使规则无需额外状态即天然幂等。
var edgeQuoteRunes = map[rune]struct{}{
	'「': {}, '」': {}, '『': {}, '』': {},
	'“': {}, '”': {}, '‘': {}, '’': {},
	'«': {}, '»': {}, '"': {},
}

func (r *PunctuationWrapLossWrapRule) Apply(seg *model.Segment) CorrectionResult {
	// 1) 触发条件：必须存在 pending（未 dismissed）的 punctuation_wrap_loss issue。
	if !hasPendingIssueCode(seg.Issues, qa.CheckPunctuationWrapLoss) {
		return CorrectionResult{Reason: "no punctuation_wrap_loss issue"}
	}
	// 2) 源文：剥离保护区与占位符并 TrimSpace 后，首 rune 必须是有向配对开引号。
	cleanSrc := strings.TrimSpace(qa.StripProtectedRegions(seg.Source, seg.Protected))
	open, ok := firstRune(cleanSrc)
	close, isPair := closingRuneFor(open)
	if !ok || !isPair {
		return CorrectionResult{Reason: "source does not start with a paired opening quote"}
	}
	// 3) 源文必须以配对的闭引号结尾。
	closeActual, ok := lastRune(cleanSrc)
	if !ok || closeActual != close {
		return CorrectionResult{Reason: "source does not end with the matching closing quote"}
	}
	// 4) 开闭引号之间必须有内容。
	if runeLen(cleanSrc) <= 2 {
		return CorrectionResult{Reason: "source has no content between the quotes"}
	}
	// 5) 单 span 守卫：清理后的源文中开、闭引号各恰好出现一次。
	if countRune(cleanSrc, open) != 1 || countRune(cleanSrc, close) != 1 {
		return CorrectionResult{Reason: "multi-span source, cannot safely whole-wrap"}
	}
	// 6) 目标防御：仅译文真实外缘的引号 rune 阻断包裹。内层引号是合法译文
	//    内容，不妨碍恢复外层包裹；尾部闭引号若与内部开引号配对，则不算
	//    外缘引号。
	cleanTgt := strings.TrimSpace(qa.StripProtectedRegions(seg.Target, seg.Protected))
	if targetHasEdgeQuoteRune(cleanTgt) {
		return CorrectionResult{Reason: "target edge already has quote runes"}
	}
	// 7) 源文边界守卫：源文引号不在原始最外缘时拒绝执行，保留 issue 可见
	//    （详见 sourceQuotesAtRawEdges）。
	if !sourceQuotesAtRawEdges(seg.Source, open, close) {
		return CorrectionResult{Reason: reasonSourceQuotesInsideMarkup}
	}
	// 8) 内部引号平衡：译文已带失衡有向引号（如 他说“这样、他「说）时，
	//    外层包裹会加剧嵌套混乱（「他说“这样」、「他「说」）。边缘已由上面
	//    守卫处理；此处要求每对引号计数相等，失衡段交给 punctuation_pairing。
	if !interiorQuotesBalanced(cleanTgt) {
		return CorrectionResult{Reason: "target has unbalanced interior quotes"}
	}
	newTarget := string(open) + seg.Target + string(close)
	return CorrectionResult{
		Changed:       true,
		NewTarget:     newTarget,
		Op:            "punctuation_wrap_loss.wrap",
		ResolvedCodes: []string{qa.CheckPunctuationWrapLoss},
	}
}

func targetHasEdgeQuoteRune(text string) bool {
	first, firstOK := firstRune(text)
	if firstOK && hasEdgeQuoteRune(first) {
		return true
	}
	last, lastOK := lastRune(text)
	if !lastOK || !hasEdgeQuoteRune(last) {
		return false
	}
	// 末尾的闭引号，若其配对开引号在译文前部出现过，属内层引号；
	// 否则视为外缘引号，包裹会产生不安全的混合边界。
	for open, close := range quotePairs {
		if close != last {
			continue
		}
		if countRune(text, open) > 0 {
			return false
		}
	}
	return true
}

func hasEdgeQuoteRune(r rune) bool {
	_, ok := edgeQuoteRunes[r]
	return ok
}

// interiorQuotesBalanced 报告 text 中每对有向引号的开/闭计数是否相等，
// 以及对称 ASCII 引号 " 是否成偶数次出现。边缘 rune 已被调用方守卫排除，
// 此处只检查内部；刻意不校验嵌套顺序——那归 punctuation_pairing 管。
func interiorQuotesBalanced(text string) bool {
	for open, close := range quotePairs {
		if countRune(text, open) != countRune(text, close) {
			return false
		}
	}
	return countRune(text, '"')%2 == 0
}
