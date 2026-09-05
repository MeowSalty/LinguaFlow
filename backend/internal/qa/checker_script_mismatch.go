package qa

import (
	"context"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/script"
)

// ScriptMismatchChecker 检测译文文字系统与目标语言不符：
// 同语言多文字系统的语言（如 zh-Hans/zh-Hant、sr-Latn/sr-Cyrl）中，
// 译文出现兄弟文字系统专属字符（≥1 即报，与 width_mix 的"出现即报"口径一致）。
//
// 仅对多文字系统语言（Resolve 成功）活跃；单文字系统语言（en/ja）、
// "auto" 与无法解析的目标语一律静默不活跃，不产生任何 issue。
type ScriptMismatchChecker struct {
	targetLang string // 原始 tag，用于 message 展示
	profile    script.Profile
	active     bool // Resolve 失败（单文字系统语言/auto/无法解析）时静默不活跃
}

// NewScriptMismatchChecker 创建文字系统不符检测器。
// targetLang 为原始 BCP-47 tag（如 "zh-Hans"），不截断、不改写，
// 仅用于 message 展示；是否活跃由 script.Resolve 决定。
func NewScriptMismatchChecker(targetLang string) *ScriptMismatchChecker {
	profile, ok := script.Resolve(targetLang)
	return &ScriptMismatchChecker{
		targetLang: targetLang,
		profile:    profile,
		active:     ok,
	}
}

func (c *ScriptMismatchChecker) Name() string { return CheckScriptMismatch }

func (c *ScriptMismatchChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	if !c.active {
		return nil
	}
	var issues []QualityIssue
	for _, seg := range segments {
		tgt := seg.TargetText
		if strings.TrimSpace(tgt) == "" {
			continue
		}
		// 占位符/内联标记保护区不参与判别（与 width_mix 同一屏蔽出口）。
		regions := InlineMarkupRegions(tgt, seg.Protected)
		cleanTgt := StripRegions(tgt, regions)
		ev, ok := c.profile.Scan(cleanTgt)
		if !ok {
			continue
		}
		sample := ev.Sample
		if !strings.Contains(tgt, sample) {
			// sample 来自 strip 后的文本，可能横跨被保护区隔开的两个原文片段
			// （原文中并不连续）；退化为首字符，保证 span 与 message 均可在原文定位。
			rs := []rune(sample)
			sample = string(rs[0])
		}
		span := LocateSpanExcludingRegions(tgt, sample, regions)
		if span == nil {
			span = &Span{MatchedText: sample}
		}
		issues = append(issues, QualityIssue{
			SegmentIndex: seg.Index,
			Severity:     SeverityWarning,
			Code:         CheckScriptMismatch,
			Message: fmt.Sprintf("译文使用了%s（如「%s」），目标语言 %s 期望%s",
				ev.Script.DisplayName(), sample, c.targetLang, c.profile.Expected.DisplayName()),
			Span: span,
		})
	}
	return issues
}
