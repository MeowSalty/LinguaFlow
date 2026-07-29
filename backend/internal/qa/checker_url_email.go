package qa

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// URLEmailMismatchChecker 比对还原后源/译 URL 与邮箱多重集合（兜底校验）。
type URLEmailMismatchChecker struct{}

// NewURLEmailMismatchChecker 创建 URL/邮箱守恒检测器。
func NewURLEmailMismatchChecker() *URLEmailMismatchChecker {
	return &URLEmailMismatchChecker{}
}

func (c *URLEmailMismatchChecker) Name() string { return CheckURLEmailMismatch }

// 宽松匹配 http(s)/ftp URL 与常见邮箱；比对时统一小写。
var (
	urlTokenRe   = regexp.MustCompile(`(?i)\b(?:https?|ftp)://[^\s<>"']+`)
	emailTokenRe = regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b`)
)

func (c *URLEmailMismatchChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		src := seg.SourceText
		tgt := seg.TargetText
		if strings.TrimSpace(src) == "" || strings.TrimSpace(tgt) == "" {
			continue
		}
		srcSet := extractURLEmailMultiset(src)
		tgtSet := extractURLEmailMultiset(tgt)
		if len(srcSet) == 0 && len(tgtSet) == 0 {
			continue
		}
		if multisetEqual(srcSet, tgtSet) {
			continue
		}
		issues = append(issues, QualityIssue{
			SegmentIndex: seg.Index,
			Severity:     SeverityWarning,
			Code:         CheckURLEmailMismatch,
			Message:      fmt.Sprintf("URL/邮箱不一致：原文 %v，译文 %v", keysOf(srcSet), keysOf(tgtSet)),
		})
	}
	return issues
}

func extractURLEmailMultiset(text string) map[string]int {
	counts := make(map[string]int)
	for _, m := range urlTokenRe.FindAllString(text, -1) {
		// 去掉尾部常见标点
		m = strings.TrimRight(m, ".,;:!?)]}\"'>")
		key := strings.ToLower(m)
		counts[key]++
	}
	for _, m := range emailTokenRe.FindAllString(text, -1) {
		m = strings.TrimRight(m, ".,;:!?)]}\"'>")
		key := strings.ToLower(m)
		counts[key]++
	}
	return counts
}

func keysOf(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(m))
	for _, k := range keys {
		n := m[k]
		for i := 0; i < n; i++ {
			out = append(out, k)
		}
	}
	return out
}
