package qa

import (
	"fmt"
	"strings"
	"unicode"
)

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
