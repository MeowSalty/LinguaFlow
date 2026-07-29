package qa

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// XMLTagMismatchChecker 比对源/译 XML 标签多重集合；仅当源文含标签时检测。
type XMLTagMismatchChecker struct{}

// NewXMLTagMismatchChecker 创建 XML 标签守恒检测器。
func NewXMLTagMismatchChecker() *XMLTagMismatchChecker {
	return &XMLTagMismatchChecker{}
}

func (c *XMLTagMismatchChecker) Name() string { return CheckXMLTagMismatch }

// 与 protect.XMLProtector 一致：匹配 <tag> / </tag> / <tag attr> / <tag/>。
var xmlTagTokenRe = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9:-]*(?:\s+[^<>]*)?/?>`)

func (c *XMLTagMismatchChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		src := seg.SourceText
		tgt := seg.TargetText
		if strings.TrimSpace(src) == "" {
			continue
		}
		srcTags := extractXMLTagMultiset(src)
		if len(srcTags) == 0 {
			continue
		}
		tgtTags := extractXMLTagMultiset(tgt)
		if multisetEqual(srcTags, tgtTags) {
			continue
		}
		issues = append(issues, QualityIssue{
			SegmentIndex: seg.Index,
			Severity:     SeverityError,
			Code:         CheckXMLTagMismatch,
			Message:      fmt.Sprintf("XML 标签不一致：原文 %v，译文 %v", keysOf(srcTags), keysOf(tgtTags)),
		})
	}
	return issues
}

func extractXMLTagMultiset(text string) map[string]int {
	counts := make(map[string]int)
	for _, m := range xmlTagTokenRe.FindAllString(text, -1) {
		counts[m]++
	}
	return counts
}
