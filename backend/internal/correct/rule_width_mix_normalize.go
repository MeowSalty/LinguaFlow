package correct

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// WidthMixNormalizeRule 机械修复 width_mix 报出的全半角混用。
//
// CJK 译文：把零歧义半角标点集合（! ? , ; : ( ) [ ]，刻意排除小数歧义的 . 等）
// 转全角（+0xFEE0）；数字双侧的 , : 与数字前缀的 !? run 豁免（保留 1,000 / 12:30 /
// 5! 原样）。拉丁译文：U+FF01–U+FF5E 全角字符（含全角字母数字，NFKC 一致）转回
// 半角（-0xFEE0），排除 FF5F/FF60，无条件转换、无守卫。
//
// 方向由 pending issue 的 Span.MatchedText 首 rune 反推（半角安全集→CJK；
// FF01-FF5E→拉丁），不注入 lang、不扩 Rule 接口。
//
// 守卫判定基准统一为 clean 文本（InlineMarkupRegions 屏蔽后的 rune 序列）：
// checker 在 clean 上扫、本规则在 clean 上判，杜绝保护区邻接歧义导致幂等回滚；
// 命中转换经平行 origIdx 回写原文副本。
//
// 守卫谓词与转换集合与 qa.WidthMixChecker 同口径镜像（本地显式维护，避免依赖
// qa 私有集合而随之漂移）。
//
// 残留风险：字母邻接数学记号 n! 仍会转换——散文 OK!/GO! 占主导且局部不可分，
// 属局部检测不可再压的地板；未受保护的代码片段归保护区层。
type WidthMixNormalizeRule struct{}

func (*WidthMixNormalizeRule) Name() string { return RuleWidthMixNormalize }

func (*WidthMixNormalizeRule) ResolvedCodes() []string {
	return []string{qa.CheckWidthMix}
}

func (r *WidthMixNormalizeRule) Apply(seg *model.Segment) CorrectionResult {
	// 1) 触发门：必须存在 pending（未 dismissed）的 width_mix issue。
	if !hasPendingIssueCode(seg.Issues, qa.CheckWidthMix) {
		return CorrectionResult{Reason: "no width_mix issue"}
	}
	// 2) 方向反推：取首条 pending issue 的 Span.MatchedText 首 rune；无法识别时绝不猜。
	cjk, ok := inferWidthMixDirection(seg.Issues)
	if !ok {
		return CorrectionResult{Reason: "width_mix issue span unreadable, cannot infer direction"}
	}
	// 3) 改写：构建 clean rune 切片（保护区外）+ 平行 origIdx（clean 位置→原文 rune
	//    位置），在 clean 上判守卫与命中，收集改写记录后统一回写原文副本。
	runes := []rune(seg.Target)
	regions := qa.InlineMarkupRegions(seg.Target, seg.Protected)
	clean := make([]rune, 0, len(runes))
	origIdx := make([]int, 0, len(runes))
	for i, c := range runes {
		if runeInRegions(regions, i) {
			continue
		}
		clean = append(clean, c)
		origIdx = append(origIdx, i)
	}
	rewrites := make(map[int]rune)
	if cjk {
		rewriteCJKHalfPunct(clean, origIdx, rewrites)
	} else {
		for i, c := range clean {
			if half, hit := fullToHalf(c); hit {
				rewrites[origIdx[i]] = half
			}
		}
	}
	if len(rewrites) == 0 {
		return CorrectionResult{Reason: "no convertible width char outside protected regions"}
	}
	out := make([]rune, len(runes))
	copy(out, runes)
	for idx, nr := range rewrites {
		out[idx] = nr
	}
	return CorrectionResult{
		Changed:       true,
		NewTarget:     string(out),
		Op:            "width_mix.normalize",
		ResolvedCodes: []string{qa.CheckWidthMix},
	}
}

// inferWidthMixDirection 从首条 pending width_mix issue 反推改写方向：
// Span.MatchedText 首 rune 属半角安全集→CJK 译文（cjk=true）；属 FF01-FF5E→
// 拉丁译文（cjk=false）。span 缺失、MatchedText 为空或首 rune 不属任一集合时
// 返回 ok=false——宁可放弃修复也绝不猜方向。调用方需先以 hasPendingIssueCode
// 保证至少存在一条 pending issue。
func inferWidthMixDirection(issues []qa.QualityIssue) (cjk bool, ok bool) {
	for _, iss := range issues {
		if iss.Code != qa.CheckWidthMix || iss.Dismissed() {
			continue
		}
		if iss.Span == nil || iss.Span.MatchedText == "" {
			return false, false
		}
		first, _ := firstRune(iss.Span.MatchedText)
		if _, isHalf := halfToFull(first); isHalf {
			return true, true
		}
		if _, isFull := fullToHalf(first); isFull {
			return false, true
		}
		return false, false
	}
	return false, false
}

// halfToFull 将零歧义半角标点集合（! ? , ; : ( ) [ ]）映射为全角对应（+0xFEE0）。
// 与 qa.WidthMixChecker 的 CJK 检出/转换子集同口径镜像，本地显式维护，
// 避免 correct 依赖 qa 私有集合而随之漂移。集合外字符一律不转。
func halfToFull(r rune) (rune, bool) {
	switch r {
	case '!', '?', ',', ';', ':', '(', ')', '[', ']':
		return r + 0xFEE0, true
	}
	return r, false
}

// fullToHalf 将 U+FF01–U+FF5E 全角字符映射回半角（-0xFEE0，NFKC 一致，含全角
// 字母数字），排除 FF5F/FF60。与 qa.WidthMixChecker 的拉丁检出区间同口径镜像。
func fullToHalf(r rune) (rune, bool) {
	if r >= 0xFF01 && r <= 0xFF5E {
		return r - 0xFEE0, true
	}
	return r, false
}

// isASCIIDigit 报告 r 是否为 ASCII 十进制数字（守卫只认 0-9，不认全角数字）。
func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

// digitFlankedBoth 数字双侧守卫：clean[i] 左右紧邻均为 ASCII 数字时豁免
// （保留 1,000 / 12:30 原样）。仅适用于 , : 两字符。
func digitFlankedBoth(clean []rune, i int) bool {
	return i > 0 && i < len(clean)-1 && isASCIIDigit(clean[i-1]) && isASCIIDigit(clean[i+1])
}

// digitPrefixedRunExempt 数字前缀 run 守卫：取 clean[i]（必为 ! 或 ?）起的最大连续
// !? run [i, j)，若 run 前紧邻 ASCII 数字则整个 run 豁免，返回 (true, j)；
// 否则当前字符正常转换，返回 (false, i+1)。run 内后续字符的前邻必为 !?（非数字），
// 逐字符推进即可全部转换，无需调用方单独处理 run 内部。
func digitPrefixedRunExempt(clean []rune, i int) (exempt bool, skipTo int) {
	j := i + 1
	for j < len(clean) && (clean[j] == '!' || clean[j] == '?') {
		j++
	}
	if i > 0 && isASCIIDigit(clean[i-1]) {
		return true, j
	}
	return false, i + 1
}

// runeInRegions 报告 rune 偏移 i 是否落在任一区域内。regions 需为升序且已合并
// （qa.InlineMarkupRegions 的输出契约），据此用起点提前终止扫描。
func runeInRegions(regions [][2]int, i int) bool {
	for _, rg := range regions {
		if rg[0] > i {
			return false
		}
		if i < rg[1] {
			return true
		}
	}
	return false
}

// rewriteCJKHalfPunct 在 clean 切片上扫描零歧义半角标点并施加两类数字守卫，
// 命中转换以原文 rune 位置（origIdx）为键写入 rewrites。
func rewriteCJKHalfPunct(clean []rune, origIdx []int, rewrites map[int]rune) {
	for i := 0; i < len(clean); {
		r := clean[i]
		full, hit := halfToFull(r)
		if !hit {
			i++
			continue
		}
		switch {
		case (r == ',' || r == ':') && digitFlankedBoth(clean, i):
			i++ // 数字双侧守卫：1,000 / 12:30 原样保留
		case r == '!' || r == '?':
			exempt, skipTo := digitPrefixedRunExempt(clean, i)
			if exempt {
				i = skipTo // 数字前缀 run 守卫：整个 !? run 豁免（5! / 100!?）
				continue
			}
			rewrites[origIdx[i]] = full
			i++
		default:
			rewrites[origIdx[i]] = full // ; ( ) [ ] 无守卫
			i++
		}
	}
}
