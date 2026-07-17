package qa

import (
	"context"
	"fmt"
)

// DuplicateTranslationChecker 检测同一批次中不同原文映射到相同译文的情况。
type DuplicateTranslationChecker struct{}

// NewDuplicateTranslationChecker 创建一个重复译文检测器。
func NewDuplicateTranslationChecker() *DuplicateTranslationChecker {
	return &DuplicateTranslationChecker{}
}

func (c *DuplicateTranslationChecker) Name() string { return "duplicate" }

func (c *DuplicateTranslationChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	type entry struct {
		sourceIndex int
		sourceText  string
	}
	targetMap := make(map[string]entry)

	var issues []QualityIssue
	for _, seg := range segments {
		tgt := seg.TargetText
		if tgt == "" {
			continue
		}
		if prev, exists := targetMap[tgt]; exists {
			if prev.sourceText != seg.SourceText {
				issues = append(issues, QualityIssue{
					SegmentIndex: seg.Index,
					Severity:     SeverityWarning,
					Code:         "duplicate",
					Message:      fmt.Sprintf("译文与段落 %d 重复（原文不同）", prev.sourceIndex),
				})
			}
		} else {
			targetMap[tgt] = entry{sourceIndex: seg.Index, sourceText: seg.SourceText}
		}
	}
	return issues
}
