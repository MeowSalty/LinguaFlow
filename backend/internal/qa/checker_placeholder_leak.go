package qa

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// LeftoverPlaceholderChecker 检测最终译文中残留的 __LF_* 占位符（Unprotect 泄漏兜底）。
type LeftoverPlaceholderChecker struct{}

// NewLeftoverPlaceholderChecker 创建占位符泄漏检测器。
func NewLeftoverPlaceholderChecker() *LeftoverPlaceholderChecker {
	return &LeftoverPlaceholderChecker{}
}

func (c *LeftoverPlaceholderChecker) Name() string { return CheckLeftoverPlaceholder }

// 与 protect 包占位符形态兼容：__LF_ 后接字母数字下划线。
var leftoverPlaceholderRe = regexp.MustCompile(`__LF_[A-Za-z0-9_]+`)

func (c *LeftoverPlaceholderChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		tgt := seg.TargetText
		if tgt == "" || !strings.Contains(tgt, "__LF_") {
			continue
		}
		seen := make(map[string]struct{})
		for _, m := range leftoverPlaceholderRe.FindAllString(tgt, -1) {
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			span := LocateSpan(tgt, m)
			if span == nil {
				span = &Span{MatchedText: m}
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityError,
				Code:         CheckLeftoverPlaceholder,
				Message:      fmt.Sprintf("译文残留占位符：%s", m),
				Span:         span,
			})
		}
	}
	return issues
}
