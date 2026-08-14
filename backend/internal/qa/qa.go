package qa

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
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

// 全部确定性 checker 名称常量（与 Checker.Name() / issue code 对齐）。
const (
	CheckUntranslated             = "untranslated"
	CheckLengthRatio              = "length_ratio"
	CheckDuplicate                = "duplicate"
	CheckSourceResidual           = "source_residual"
	CheckPunctuationPairing       = "punctuation_pairing"
	CheckPunctuationMissing       = "punctuation_missing"
	CheckWhitespaceIrregular      = "whitespace_irregular"
	CheckRepeatedSpace            = "repeated_space"
	CheckWidthMix                 = "width_mix"
	CheckNumberMismatch           = "number_mismatch"
	CheckURLEmailMismatch         = "url_email_mismatch"
	CheckSubtitleLineCount        = "subtitle_line_count"
	CheckForbiddenTerm            = "forbidden_term"
	CheckTermInconsistency        = "term_inconsistency"
	CheckLeftoverPlaceholder      = "leftover_placeholder"
	CheckXMLTagMismatch           = "xml_tag_mismatch"
	CodeDuplicateSourceDivergence = "duplicate_source_divergence"
)

// 语义质检（semantic QA）维护的 issue code 权威清单。
// 是 prompt 解析白名单、JSON schema enum、执行计划校验与段落列表过滤的单一来源。
const (
	IssueCodeCalque         = "calque"
	IssueCodeTermFidelity   = "term_fidelity"
	IssueCodeNaturalness    = "naturalness"
	IssueCodeMistranslation = "mistranslation"
	IssueCodeOmission       = "omission"
	IssueCodeAddition       = "addition"
	IssueCodeGrammar        = "grammar"
	IssueCodeRegister       = "register"
)

// SemanticQACodes 返回语义质检维护的全部 issue code。
// 新增语义 code 时只需在此处追加，下游所有枚举点（解析白名单、schema enum、
// 执行计划校验、段落列表过滤）将自动同步，避免多点硬编码漂移。
func SemanticQACodes() []string {
	return []string{
		IssueCodeCalque,
		IssueCodeTermFidelity,
		IssueCodeNaturalness,
		IssueCodeMistranslation,
		IssueCodeOmission,
		IssueCodeAddition,
		IssueCodeGrammar,
		IssueCodeRegister,
	}
}

// semanticQACodeSet 是 SemanticQACodes 的 set 视图，供高频判定复用。
var semanticQACodeSet = func() map[string]struct{} {
	set := make(map[string]struct{}, 8)
	for _, c := range SemanticQACodes() {
		set[c] = struct{}{}
	}
	return set
}()

// IsSemanticQACode 报告 code 是否由语义质检轮次维护。
func IsSemanticQACode(code string) bool {
	_, ok := semanticQACodeSet[code]
	return ok
}

// DocumentCheckerCodes 返回文档级（非 per-batch）检查产出的 issue code。
// 这些检查不走 Engine 注册表（AllCheckerNames），由 worker/preview 直接调用，
// 但同样持久化到 quality_issues，故需纳入筛选键。新增文档级检查时在此追加。
func DocumentCheckerCodes() []string {
	return []string{
		CodeDuplicateSourceDivergence,
	}
}

// FilterableIssueCodes 返回执行计划 issue_codes 与段落列表 quality_code
// 接受的全部 issue code（全部 per-batch checker code + 文档级 checker code + 全部语义 code）。
// 由 AllCheckerNames()、DocumentCheckerCodes() 与 SemanticQACodes() 合并派生，
// 三者其一新增 code 时自动同步，避免多点硬编码漂移。
func FilterableIssueCodes() []string {
	return mergeUnique(mergeUnique(AllCheckerNames(), DocumentCheckerCodes()), SemanticQACodes())
}

// mergeUnique 合并两个切片并去重，保留首次出现的顺序。
func mergeUnique(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// filterableIssueCodeSet 是 FilterableIssueCodes 的 set 视图，供高频判定复用。
var filterableIssueCodeSet = func() map[string]struct{} {
	codes := FilterableIssueCodes()
	set := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		set[c] = struct{}{}
	}
	return set
}()

// IsFilterableIssueCode 报告 code 是否可作为执行计划 issue_codes 或
// 段落列表 quality_code 的合法筛选值。
func IsFilterableIssueCode(code string) bool {
	_, ok := filterableIssueCodeSet[code]
	return ok
}

// AllCheckerNames 返回全部可配置的 per-batch checker 名称（不含文档级）。
func AllCheckerNames() []string {
	return []string{
		CheckUntranslated,
		CheckLengthRatio,
		CheckDuplicate,
		CheckSourceResidual,
		CheckPunctuationPairing,
		CheckPunctuationMissing,
		CheckWhitespaceIrregular,
		CheckRepeatedSpace,
		CheckWidthMix,
		CheckNumberMismatch,
		CheckURLEmailMismatch,
		CheckSubtitleLineCount,
		CheckForbiddenTerm,
		CheckTermInconsistency,
		CheckLeftoverPlaceholder,
		CheckXMLTagMismatch,
	}
}

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
	Protected  map[string]string // 占位符→保护区原文；供标点/空白类 checker 屏蔽
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
	// Checks 为 nil 时启用全部 per-batch checker；非 nil 时按 Checker.Name() 精确过滤。
	Checks []string
	// Format 为资源格式（如 srt/ass/vtt/txt），供字幕行数等格式相关检测使用。
	Format string
	// Glossary 由 worker 注入；nil 时术语类 checker 跳过。
	Glossary glossary.Glossary
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
// Checks 为 nil 时注册全部 checker；非 nil 时仅注册名单中的 checker（按 Name 精确匹配）。
func NewEngine(cfg Config, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	e := &Engine{
		config: cfg,
		logger: logger,
	}
	all := buildAllCheckers(cfg)
	if cfg.Checks == nil {
		e.checkers = all
		return e
	}
	allow := make(map[string]struct{}, len(cfg.Checks))
	for _, name := range cfg.Checks {
		allow[name] = struct{}{}
	}
	e.checkers = make([]Checker, 0, len(cfg.Checks))
	for _, c := range all {
		if _, ok := allow[c.Name()]; ok {
			e.checkers = append(e.checkers, c)
		}
	}
	return e
}

func buildAllCheckers(cfg Config) []Checker {
	return []Checker{
		NewUntranslatedChecker(),
		NewLengthRatioChecker(cfg.LengthRatioMin, cfg.LengthRatioMax, cfg.LengthMethod),
		NewDuplicateTranslationChecker(),
		NewSourceResidualChecker(cfg.SourceLang, cfg.TargetLang),
		NewPunctuationPairingChecker(cfg.TargetLang),
		NewPunctuationMissingChecker(),
		NewWhitespaceIrregularChecker(),
		NewRepeatedSpaceChecker(cfg.TargetLang),
		NewWidthMixChecker(cfg.TargetLang),
		NewNumberMismatchChecker(),
		NewURLEmailMismatchChecker(),
		NewSubtitleLineCountChecker(cfg.Format),
		NewForbiddenTermChecker(cfg.Glossary, cfg.SourceLang, cfg.TargetLang),
		NewTermInconsistencyChecker(cfg.Glossary, cfg.SourceLang, cfg.TargetLang),
		NewLeftoverPlaceholderChecker(),
		NewXMLTagMismatchChecker(),
	}
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
