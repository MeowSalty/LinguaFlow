package qa

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 可调常量：强档独立脚本默认最小连续长度；kana/hangul 为 1。
const (
	minRunKana    = 1 // 假名在非目标脚本中几乎必为残留
	minRunHangul  = 1 // 谚文同上
	minRunDefault = 2 // 其余独立脚本强档默认，规避单字符噪声
	minRunHanSemi = 2 // 准强档 Han
	minRunHanMed  = 2 // 中等档 Han
	minRunHanWeak = 3 // 弱档 Han
)

// weakTierEnabled 弱档（zh→ja/ko Han）默认关闭；路径保留便于后续开启。
const weakTierEnabled = false

type residualTier int

const (
	tierStrong residualTier = iota
	tierSemiStrong
	tierMedium
	tierWeak
)

type anchorMode int

const (
	anchorOptional anchorMode = iota
	anchorRequired
)

// residualRule 描述一条源语残留检测规则。
type residualRule struct {
	tier   residualTier
	script []*unicode.RangeTable
	minRun int
	anchor anchorMode
}

// SourceResidualChecker 检测译文中夹带的源语脚本片段。
type SourceResidualChecker struct {
	rules   []residualRule
	srcLang string
	tgtLang string
}

// NewSourceResidualChecker 按语言对解析规则并创建检测器。
func NewSourceResidualChecker(srcLang, tgtLang string) *SourceResidualChecker {
	return &SourceResidualChecker{
		rules:   resolveRules(srcLang, tgtLang),
		srcLang: srcLang,
		tgtLang: tgtLang,
	}
}

func (c *SourceResidualChecker) Name() string { return "source_residual" }

func (c *SourceResidualChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	if len(c.rules) == 0 {
		return nil
	}
	var issues []QualityIssue
	for _, seg := range segments {
		src := strings.TrimSpace(seg.SourceText)
		tgt := strings.TrimSpace(seg.TargetText)
		if src == "" || tgt == "" {
			continue
		}
		if src == tgt {
			continue // 整段未译由 untranslated 负责
		}
		cleanedTgt := stripPlaceholders(tgt)
		cleanedSrc := stripPlaceholders(src)
		for _, rule := range c.rules {
			hits := collectResidualHits(cleanedSrc, cleanedTgt, rule)
			if len(hits) == 0 {
				continue
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         "source_residual",
				Message:      fmt.Sprintf("译文残留源语片段：%s", strings.Join(hits, " / ")),
			})
			break // 同段报一次即可
		}
	}
	return issues
}

// resolveRules 按源/目标语言解析适用的残留检测规则。
func resolveRules(sourceLang, targetLang string) []residualRule {
	src := normalizeLang(sourceLang)
	tgt := normalizeLang(targetLang)
	if src == "" || src == "auto" || tgt == "" || tgt == "auto" {
		return nil
	}
	srcS := primaryScripts(src)
	tgtS := primaryScripts(tgt)
	if srcS == nil || tgtS == nil {
		return nil
	}

	var rules []residualRule

	// 非 Han 的源特有脚本 → 强档（kana 合并为一条）
	var distinctive []*unicode.RangeTable
	var kanaTables []*unicode.RangeTable
	for _, t := range srcS {
		if isHanTable(t) {
			continue
		}
		if tableIn(tgtS, t) {
			continue
		}
		if isKanaTable(t) {
			kanaTables = append(kanaTables, t)
			continue
		}
		distinctive = append(distinctive, t)
	}
	if len(kanaTables) > 0 {
		rules = append(rules, residualRule{
			tier:   tierStrong,
			script: kanaTables,
			minRun: minRunKana,
			anchor: anchorOptional,
		})
	}
	for _, t := range distinctive {
		minRun := minRunDefault
		if isHangulTable(t) {
			minRun = minRunHangul
		}
		rules = append(rules, residualRule{
			tier:   tierStrong,
			script: []*unicode.RangeTable{t},
			minRun: minRun,
			anchor: anchorOptional,
		})
	}

	// Han 相关
	srcHasHan := tableIn(srcS, unicode.Han)
	tgtHasHan := tableIn(tgtS, unicode.Han)
	switch {
	case srcHasHan && !tgtHasHan:
		// zh→en/fr/ru…：Han 在目标不应出现 → 准强（锚定）
		rules = append(rules, residualRule{
			tier:   tierSemiStrong,
			script: []*unicode.RangeTable{unicode.Han},
			minRun: minRunHanSemi,
			anchor: anchorRequired,
		})
	case srcHasHan && tgtHasHan && src != tgt:
		switch {
		case (src == "zh" && tgt == "ja") || (src == "zh" && tgt == "ko"):
			if weakTierEnabled {
				rules = append(rules, residualRule{
					tier:   tierWeak,
					script: []*unicode.RangeTable{unicode.Han},
					minRun: minRunHanWeak,
					anchor: anchorRequired,
				})
			}
		case src == "ja" && tgt == "zh":
			rules = append(rules, residualRule{
				tier:   tierMedium,
				script: []*unicode.RangeTable{unicode.Han},
				minRun: minRunHanMed,
				anchor: anchorRequired,
			})
		}
	}

	return rules
}

// collectResidualHits 收集命中的残留片段。
// 无锚定：译文侧脚本 run 直接命中。
// 需锚定：双向匹配——源 run 出现在译文，或译文 run 出现在源（覆盖 CJK 粘连与部分残留）。
func collectResidualHits(cleanedSrc, cleanedTgt string, rule residualRule) []string {
	if rule.anchor != anchorRequired {
		var hits []string
		for _, run := range extractScriptRuns(cleanedTgt, rule.script) {
			if utf8.RuneCountInString(run) < rule.minRun {
				continue
			}
			hits = append(hits, run)
		}
		return hits
	}

	seen := make(map[string]struct{})
	var hits []string
	add := func(run string) {
		if _, ok := seen[run]; ok {
			return
		}
		seen[run] = struct{}{}
		hits = append(hits, run)
	}
	for _, run := range extractScriptRuns(cleanedSrc, rule.script) {
		if utf8.RuneCountInString(run) < rule.minRun {
			continue
		}
		if strings.Contains(cleanedTgt, run) {
			add(run)
		}
	}
	for _, run := range extractScriptRuns(cleanedTgt, rule.script) {
		if utf8.RuneCountInString(run) < rule.minRun {
			continue
		}
		if strings.Contains(cleanedSrc, run) {
			add(run)
		}
	}
	return hits
}

// extractScriptRuns 收集属于给定脚本集合的连续极大段。
func extractScriptRuns(text string, scripts []*unicode.RangeTable) []string {
	if text == "" || len(scripts) == 0 {
		return nil
	}
	var runs []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			runs = append(runs, b.String())
			b.Reset()
		}
	}
	for _, r := range text {
		if ruleBelongs(r, scripts) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return runs
}

func ruleBelongs(r rune, scripts []*unicode.RangeTable) bool {
	for _, t := range scripts {
		if unicode.Is(t, r) {
			return true
		}
	}
	return false
}

var placeholderRe = regexp.MustCompile(`__LF_[A-Za-z0-9_]+`)

// stripPlaceholders 移除 __LF_* 占位符并转小写。
func stripPlaceholders(s string) string {
	if strings.Contains(s, "__LF_") {
		s = placeholderRe.ReplaceAllString(s, "")
	}
	return strings.ToLower(s)
}
