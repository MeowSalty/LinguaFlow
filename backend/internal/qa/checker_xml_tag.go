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

// rubyTagFamily 是 HTML ruby 注音标签族。其守恒由 ruby 还原子系统独立负责，
// 与通用 XML 标签守恒正交：preserve_kinds 过滤会合法地从译文移除这些标签，
// 因此在多重集比对中一律排除，避免配置耦合误报。
var rubyTagFamily = map[string]struct{}{
	"ruby": {},
	"rt":   {},
	"rp":   {},
	"rb":   {},
}

// xmlTagNameRe 从标签 token 中提取标签名。
var xmlTagNameRe = regexp.MustCompile(`^</?([A-Za-z][A-Za-z0-9:-]*)`)

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
		nameMatch := xmlTagNameRe.FindStringSubmatch(m)
		if len(nameMatch) >= 2 {
			if _, skip := rubyTagFamily[strings.ToLower(nameMatch[1])]; skip {
				continue
			}
		}
		counts[m]++
	}
	return counts
}
