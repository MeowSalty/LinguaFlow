package qa

import (
	"fmt"
	"strings"
	"unicode"
)

// DuplicateSourceDivergenceEnabled 报告给定 checks 配置下文档级同文异译检查是否启用。
// 该检查不走 Engine 注册表，Config.Checks 的注册表过滤对它无效，故调用方需在调用点
// 借助本判定自行把关；判定逻辑收拢在 qa 包（worker 的 job/preview runner 与 service
// 的 preview claims 路径均委托本函数），避免各处持有副本漂移。
// 注意 Enabled 与本判定相互独立，由调用方一并检查。
func DuplicateSourceDivergenceEnabled(checks []string) bool {
	return CheckerEnabled(checks, CodeDuplicateSourceDivergence)
}

// CheckDuplicateSourceDivergence 文档级检查：规范化后完全相同的源文段出现不同译文。
// 按规范化源文分组，以组内首条译文为规范译法，对目标不同的后续段发出 warning。
// 不走 Engine，由 job_runner 在最后 translate 轮后直接调用。
func CheckDuplicateSourceDivergence(segments []CheckInput) []QualityIssue {
	type groupEntry struct {
		index      int
		targetNorm string
		targetRaw  string
	}
	groups := make(map[string][]groupEntry)
	order := make([]string, 0)

	for _, seg := range segments {
		src := strings.TrimSpace(seg.SourceText)
		tgt := strings.TrimSpace(seg.TargetText)
		if src == "" || tgt == "" {
			continue
		}
		srcKey := normalizeSourceForDivergence(src)
		if srcKey == "" {
			continue
		}
		if _, ok := groups[srcKey]; !ok {
			order = append(order, srcKey)
		}
		groups[srcKey] = append(groups[srcKey], groupEntry{
			index:      seg.Index,
			targetNorm: normalizeSourceForDivergence(tgt),
			targetRaw:  tgt,
		})
	}

	var issues []QualityIssue
	for _, key := range order {
		ents := groups[key]
		if len(ents) < 2 {
			continue
		}
		canonical := ents[0].targetNorm
		for i := 1; i < len(ents); i++ {
			if ents[i].targetNorm == canonical {
				continue
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: ents[i].index,
				Severity:     SeverityWarning,
				Code:         CodeDuplicateSourceDivergence,
				Message:      fmt.Sprintf("相同原文译文不一致（与段落 %d 不同）", ents[0].index),
			})
		}
	}
	return issues
}

// normalizeSourceForDivergence 折叠空白并去掉首尾空格；保留大小写与标点。
func normalizeSourceForDivergence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if prevSpace {
				continue
			}
			b.WriteByte(' ')
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
