package qa

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"
)

// IssueSeverity 表示质量问题的严重程度。
type IssueSeverity string

const (
	SeverityWarning IssueSeverity = "warning"
	SeverityError   IssueSeverity = "error"
)

// LengthMethod 定义长度计算方式。
type LengthMethod string

const (
	// LengthMethodCharWeight 加权字符计数：CJK 字符×2，拉丁字符×1。
	LengthMethodCharWeight LengthMethod = "char_weight"
	// LengthMethodWordCount 语义单元计数：CJK 每字 1 词，拉丁每词 1 词。
	LengthMethodWordCount LengthMethod = "word_count"
)

// Span 描述质量问题在目标文本中的跨度。
// MatchedText 为触发问题的精确文本；TargetStart/TargetEnd 为可选的字符偏移（按 rune 计）。
type Span struct {
	MatchedText string `json:"matched_text"`
	TargetStart *int   `json:"target_start,omitempty"`
	TargetEnd   *int   `json:"target_end,omitempty"`
}

// QualityIssue 是持久化到数据库的质量问题记录。
type QualityIssue struct {
	SegmentIndex int           `json:"segment_index"`
	Severity     IssueSeverity `json:"severity"`
	Code         string        `json:"code"`
	Message      string        `json:"message"`
	Span         *Span         `json:"span,omitempty"`
}

// Fingerprint 返回问题指纹 (code, matched_text)。无跨度时 matched_text 为空。
func Fingerprint(issue QualityIssue) string {
	matchedText := ""
	if issue.Span != nil {
		matchedText = issue.Span.MatchedText
	}
	return fmt.Sprintf("%s:%s", issue.Code, matchedText)
}

// MatchedText 返回问题的匹配文本；无跨度时为空字符串。
func MatchedText(issue QualityIssue) string {
	if issue.Span == nil {
		return ""
	}
	return issue.Span.MatchedText
}

// DedupIssues 按 (code, matched_text) 去重，保留首次出现。
func DedupIssues(issues []QualityIssue) []QualityIssue {
	if len(issues) == 0 {
		return issues
	}
	seen := make(map[string]struct{}, len(issues))
	out := make([]QualityIssue, 0, len(issues))
	for _, iss := range issues {
		fp := Fingerprint(iss)
		if _, ok := seen[fp]; ok {
			continue
		}
		seen[fp] = struct{}{}
		out = append(out, iss)
	}
	return out
}

// LocateSpan 在 target 中定位 matchedText 的首次出现，返回带偏移的 Span。
// 定位失败时仍返回仅含 MatchedText 的 Span（偏移为 nil）。
func LocateSpan(target, matchedText string) *Span {
	matchedText = strings.TrimSpace(matchedText)
	if matchedText == "" {
		return nil
	}
	span := &Span{MatchedText: matchedText}
	idx := strings.Index(target, matchedText)
	endByte := idx + len(matchedText)
	if idx < 0 {
		// 大小写转换可能改变 UTF-8 字节长度，因此必须在原文 rune 边界上匹配。
		var ok bool
		idx, endByte, ok = equalFoldSpan(target, matchedText)
		if !ok {
			return span
		}
		matchedText = target[idx:endByte]
		span.MatchedText = matchedText
	}
	start := utf8RuneOffset(target, idx)
	end := start + utf8.RuneCountInString(target[idx:endByte])
	span.TargetStart = &start
	span.TargetEnd = &end
	return span
}

func equalFoldSpan(target, matchedText string) (int, int, bool) {
	matchedRunes := utf8.RuneCountInString(matchedText)
	for start := range target {
		end := start
		for range matchedRunes {
			if end >= len(target) {
				end = -1
				break
			}
			_, size := utf8.DecodeRuneInString(target[end:])
			end += size
		}
		if end >= 0 && strings.EqualFold(target[start:end], matchedText) {
			return start, end, true
		}
	}
	return 0, 0, false
}

// utf8RuneOffset 将字节偏移转换为 rune 偏移。
func utf8RuneOffset(s string, byteIdx int) int {
	if byteIdx <= 0 {
		return 0
	}
	if byteIdx >= len(s) {
		return utf8.RuneCountInString(s)
	}
	return utf8.RuneCountInString(s[:byteIdx])
}

// CheckInput 是单个段落的检测输入。
type CheckInput struct {
	Index      int
	SourceText string
	TargetText string
	Meta       map[string]any
}

// Checker 定义单项质量检测规则的接口。
type Checker interface {
	Name() string
	Check(ctx context.Context, segments []CheckInput) []QualityIssue
}

// Config 控制 QA 引擎的行为。
type Config struct {
	Enabled        bool
	AutoReject     bool
	LengthMethod   LengthMethod
	LengthRatioMin float64
	LengthRatioMax float64
	SourceLang     string
	TargetLang     string
}

// DefaultConfig 返回默认的 QA 配置。
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		AutoReject:     false,
		LengthMethod:   LengthMethodCharWeight,
		LengthRatioMin: 0.2,
		LengthRatioMax: 3.0,
	}
}

// Engine 编排多个 Checker 并汇总结果。
type Engine struct {
	checkers []Checker
	config   Config
	logger   *slog.Logger
}

// NewEngine 创建一个新的 QA 引擎。
func NewEngine(cfg Config, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	e := &Engine{
		config: cfg,
		logger: logger,
	}
	e.checkers = []Checker{
		NewUntranslatedChecker(),
		NewLengthRatioChecker(cfg.LengthRatioMin, cfg.LengthRatioMax, cfg.LengthMethod),
		NewDuplicateTranslationChecker(),
		NewSourceResidualChecker(cfg.SourceLang, cfg.TargetLang),
	}
	return e
}

// Run 对所有段落运行全部检测器，返回发现的质量问题。
func (e *Engine) Run(ctx context.Context, segments []CheckInput) []QualityIssue {
	if !e.config.Enabled {
		return nil
	}
	var allIssues []QualityIssue
	for _, c := range e.checkers {
		issues := c.Check(ctx, segments)
		allIssues = append(allIssues, issues...)
	}
	return allIssues
}

// HasErrors 检查问题列表中是否包含 error 级别的问题。
func HasErrors(issues []QualityIssue) bool {
	for _, issue := range issues {
		if issue.Severity == SeverityError {
			return true
		}
	}
	return false
}

// IssuesFor 返回指定段落索引的问题列表。
func IssuesFor(segmentIndex int, issues []QualityIssue) []QualityIssue {
	var result []QualityIssue
	for _, issue := range issues {
		if issue.SegmentIndex == segmentIndex {
			result = append(result, issue)
		}
	}
	return result
}
