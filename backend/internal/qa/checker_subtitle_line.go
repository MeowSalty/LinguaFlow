package qa

import (
	"context"
	"fmt"
	"strings"
)

// SubtitleLineCountChecker 仅在 format ∈ {srt, ass, vtt} 时启用，检测源/译行数不一致。
type SubtitleLineCountChecker struct {
	enabled bool
}

// NewSubtitleLineCountChecker 创建字幕行数检测器。
func NewSubtitleLineCountChecker(format string) *SubtitleLineCountChecker {
	f := strings.ToLower(strings.TrimSpace(format))
	enabled := f == "srt" || f == "ass" || f == "vtt"
	return &SubtitleLineCountChecker{enabled: enabled}
}

func (c *SubtitleLineCountChecker) Name() string { return CheckSubtitleLineCount }

func (c *SubtitleLineCountChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	if !c.enabled {
		return nil
	}
	var issues []QualityIssue
	for _, seg := range segments {
		src := seg.SourceText
		tgt := seg.TargetText
		if strings.TrimSpace(src) == "" || strings.TrimSpace(tgt) == "" {
			continue
		}
		srcLines := countContentLines(src)
		tgtLines := countContentLines(tgt)
		if srcLines == tgtLines {
			continue
		}
		issues = append(issues, QualityIssue{
			SegmentIndex: seg.Index,
			Severity:     SeverityWarning,
			Code:         CheckSubtitleLineCount,
			Message:      fmt.Sprintf("字幕行数不一致：原文 %d 行，译文 %d 行", srcLines, tgtLines),
		})
	}
	return issues
}

// countContentLines 按换行计行；末尾单独空行不额外计（与 split 行为一致）。
func countContentLines(s string) int {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
