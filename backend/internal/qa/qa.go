package qa

import (
	"context"
	"log/slog"
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

// QualityIssue 是持久化到数据库的质量问题记录。
type QualityIssue struct {
	SegmentIndex int           `json:"segment_index"`
	Severity     IssueSeverity `json:"severity"`
	Code         string        `json:"code"`
	Message      string        `json:"message"`
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
