package qa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

// IssueSeverity 表示质量问题的严重程度。
type IssueSeverity string

const (
	SeverityWarning IssueSeverity = "warning"
	SeverityError   IssueSeverity = "error"
)

// IssueDisposition 表示一条质量问题的裁决状态。
//
// 语义：裁决行为本身是等价的 —— LLM 和用户都是"给一条 issue 下结论"，
// 区别只在 actor（仅用于审计，由 DecidedBy 的 nil/非 nil 表达）。因此不设
// source 字段。disposition 只回答"这条算不算问题"：
//   - pending：未决，仍是待解决的问题；
//   - dismissed：已判定不是问题，无需再解决。
//
// 存储与 API 共用同一组显式字面值（"pending"/"dismissed"），跨层比较无需
// 归一化。MarshalJSON/UnmarshalJSON 兼容旧数据：DB 中缺失字段或空串在读取
// 时归一化为 pending，下次写入即渐进迁移为显式 "pending"。
type IssueDisposition string

const (
	DispositionPending   IssueDisposition = "pending"
	DispositionDismissed IssueDisposition = "dismissed"
)

// MarshalJSON 输出显式字面值。Go 零值 ""（checker 构造 issue 时省略 Disposition）
// 归一化为 "pending"，保证 DB 中永远是显式枚举值，不依赖零值省略。
func (d IssueDisposition) MarshalJSON() ([]byte, error) {
	if d == "" {
		return json.Marshal(string(DispositionPending))
	}
	return json.Marshal(string(d))
}

// UnmarshalJSON 接受 "pending"/"dismissed"，并兼容旧数据的空串与 null（归一化
// 为 pending）；非法值返回错误，避免静默落入未定义状态。
//
// 注意：JSON 中 disposition 字段缺失时 json 包不会调用本方法，字段保持 Go 零值 ""。
// 该场景由 QualityIssue.UnmarshalJSON 兜底归一化。
func (d *IssueDisposition) UnmarshalJSON(data []byte) error {
	// 旧数据中 disposition 为 null 或空串时归一化为 pending。
	if bytes.Equal(data, []byte("null")) || bytes.Equal(data, []byte(`""`)) {
		*d = DispositionPending
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch IssueDisposition(s) {
	case DispositionPending, DispositionDismissed:
		*d = IssueDisposition(s)
		return nil
	default:
		return fmt.Errorf("invalid disposition %q: want pending|dismissed", s)
	}
}

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
	CheckPunctuationSurplus       = "punctuation_surplus"
	CheckPunctuationWrapLoss      = "punctuation_wrap_loss"
	CheckWhitespaceIrregular      = "whitespace_irregular"
	CheckRepeatedSpace            = "repeated_space"
	CheckWidthMix                 = "width_mix"
	CheckScriptMismatch           = "script_mismatch"
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

// AdjudicableCodes 返回可交由裁决（adjudicate）轮做 LLM 降噪的质量问题 code 权威清单。
// 是 OpenAPI adjudicate_codes enum、裁决 JSON schema 的 issue_code enum、执行计划校验
// 白名单与 prompt 模板说明的单一来源。新增可裁决 code 时只需在此处追加，下游所有枚举点
// 自动同步，避免多点硬编码漂移。
//
// 不可裁决的硬规则（untranslated / duplicate）永不列入：前者为空译文硬规则，后者需同
// 批多段输入，二者均无 LLM 降噪价值。
func AdjudicableCodes() []string {
	return []string{
		CheckSourceResidual,
		CheckLengthRatio,
		CheckPunctuationSurplus,
	}
}

// DefaultAdjudicateCodes 返回执行计划未显式配置 adjudicate_codes 时的默认裁决集。
// 必须是 AdjudicableCodes 的子集；故若调整默认集，先确认其仍在允许集内。
// length_ratio 依赖用户配置的长度比阈值，仅在用户显式选用时才裁决，不进默认集。
func DefaultAdjudicateCodes() []string {
	return []string{
		CheckSourceResidual,
		CheckPunctuationSurplus,
	}
}

// adjudicableCodeSet 是 AdjudicableCodes 的 set 视图，供高频判定复用。
var adjudicableCodeSet = func() map[string]struct{} {
	set := make(map[string]struct{}, 3)
	for _, c := range AdjudicableCodes() {
		set[c] = struct{}{}
	}
	return set
}()

// IsAdjudicableCode 报告 code 是否为可裁决 code。
func IsAdjudicableCode(code string) bool {
	_, ok := adjudicableCodeSet[code]
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

// 守恒类检查（ConservationCodes）产出的 issue code 权威清单。
// 这些 code 不走 Engine 注册表，由 pipeline（translate 轮注音还原）与
// service（人工编辑）在特定写路径上按文本事实直接产出，同样持久化到
// quality_issues 并参与筛选，故与文档级检查一样纳入 FilterableIssueCodes。
const (
	CodeRubyRestoreIncomplete = "ruby_restore_incomplete"
	CodeRubyTagLoss           = "ruby_tag_loss"
)

// ConservationCodes 返回注音守恒类检查产出的全部 issue code。
// 新增守恒 code 时只需在此处追加，FilterableIssueCodes 自动同步。
func ConservationCodes() []string {
	return []string{CodeRubyRestoreIncomplete, CodeRubyTagLoss}
}

// FilterableIssueCodes 返回执行计划 issue_codes 与段落列表 quality_code
// 接受的全部 issue code（全部 per-batch checker code + 文档级 checker code
// + 全部语义 code + 守恒类 code）。
// 由 AllCheckerNames()、DocumentCheckerCodes()、SemanticQACodes() 与
// ConservationCodes() 合并派生，四者其一新增 code 时自动同步，避免多点硬编码漂移。
func FilterableIssueCodes() []string {
	return mergeUnique(mergeUnique(mergeUnique(AllCheckerNames(), DocumentCheckerCodes()), SemanticQACodes()), ConservationCodes())
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
		CheckPunctuationSurplus,
		CheckPunctuationWrapLoss,
		CheckWhitespaceIrregular,
		CheckRepeatedSpace,
		CheckWidthMix,
		CheckScriptMismatch,
		CheckNumberMismatch,
		CheckURLEmailMismatch,
		CheckSubtitleLineCount,
		CheckForbiddenTerm,
		CheckTermInconsistency,
		CheckLeftoverPlaceholder,
		CheckXMLTagMismatch,
	}
}

// ZeroConfigDeterministicChecks 返回无需用户额外配置阈值、单段即可判定的
// 确定性 checker 名称白名单。适用于手动编辑译文等无法读取执行计划 QA 配置的
// 即时场景。
//
// 排除规则：
//   - length_ratio：依赖用户配置的长度比阈值，用默认值跑会与正式翻译流程判定矛盾；
//   - forbidden_term / term_inconsistency：依赖术语表，gls==nil 时虽自动跳过，
//     但显式排除可避免语义歧义；
//   - duplicate：需同批次多段输入，单段场景恒为空；
//   - duplicate_source_divergence：文档级检查，不走 Engine 注册表。
func ZeroConfigDeterministicChecks() []string {
	return []string{
		CheckUntranslated,
		CheckSourceResidual,
		CheckPunctuationPairing,
		CheckPunctuationMissing,
		CheckPunctuationSurplus,
		CheckPunctuationWrapLoss,
		CheckWhitespaceIrregular,
		CheckRepeatedSpace,
		CheckWidthMix,
		CheckScriptMismatch,
		CheckNumberMismatch,
		CheckURLEmailMismatch,
		CheckSubtitleLineCount,
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
	// 裁决信息。pending=未决，dismissed=已判定不是问题。DecidedBy 为 nil 表示
	// 由 LLM 裁决（adjudicate 轮），非 nil 表示用户裁决（user_id）。
	Disposition IssueDisposition `json:"disposition"`
	DecidedBy   *int             `json:"decided_by,omitempty"`
	DecidedAt   *time.Time       `json:"decided_at,omitempty"`
	Note        string           `json:"note,omitempty"`
}

// UnmarshalJSON 在默认反序列化后归一化 Disposition：旧数据中 disposition 字段
// 缺失时 Go 零值 "" 不会触发 IssueDisposition.UnmarshalJSON，这里兜底为 pending。
// 用别名类型 rawQualityIssue 走默认解码，避免递归调用本方法。
func (i *QualityIssue) UnmarshalJSON(data []byte) error {
	type rawQualityIssue QualityIssue
	var raw rawQualityIssue
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*i = QualityIssue(raw)
	if i.Disposition == "" {
		i.Disposition = DispositionPending
	}
	return nil
}

// IsPending 报告该问题是否仍未裁决。
// 所有"是否仍需处理"的判定点必须调用本方法，而非直接比较 Disposition：
// 未来若引入新的非 dismissed 裁决值，只需在此处更新语义，避免各判定点行为漂移。
//
// 兼容 Go 零值 ""：checker 构造 issue 时省略 Disposition 得到零值，语义等同
// pending。落库时 MarshalJSON 会把 "" 归一化为 "pending"，读回后即不再为零值。
func (i QualityIssue) IsPending() bool {
	return i.Disposition == DispositionPending || i.Disposition == ""
}

// Dismissed 报告该问题是否已被裁决为"不是问题"。
func (i QualityIssue) Dismissed() bool { return i.Disposition == DispositionDismissed }

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

// ReconcileIssues 将新算出的 issues 与已有 issues 按 Fingerprint 对账：
//   - 同指纹 → 继承旧裁决（disposition/decided_by/decided_at/note），其余字段
//     （severity/message/span 等）用新算值；checker 可能更新了 message 或重新
//     定位了 span，但问题本身没变，旧裁决仍然成立。
//   - 指纹消失 → 旧裁决自然清除（问题真没了，或译文变了指纹不再命中）。
//
// 用于 job retry/崩溃恢复、文档级检查重算、手动编辑等"同译文/同规则重新计算"
// 场景，确保已下达的裁决不被静默冲掉。注意：它只在"问题指纹稳定"的前提下生效
// ——若译文整体改写导致指纹全变，旧裁决理应清除（re-translate 即此语义）。
//
// 裁决等价意味着不区分 actor：同指纹即继承，不论裁决来自 LLM 还是用户。
func ReconcileIssues(fresh, existing []QualityIssue) []QualityIssue {
	if len(existing) == 0 {
		for i := range fresh {
			if fresh[i].Disposition == "" {
				fresh[i].Disposition = DispositionPending
			}
		}
		return fresh
	}
	prev := make(map[string]QualityIssue, len(existing))
	for _, iss := range existing {
		prev[Fingerprint(iss)] = iss
	}
	out := make([]QualityIssue, 0, len(fresh))
	for _, iss := range fresh {
		if old, ok := prev[Fingerprint(iss)]; ok && !old.IsPending() {
			iss.Disposition = old.Disposition
			iss.DecidedBy = old.DecidedBy
			iss.DecidedAt = old.DecidedAt
			iss.Note = old.Note
		} else if iss.Disposition == "" {
			// checker 构造 issue 时省略 Disposition 得到 Go 零值 ""，归一化为
			// 显式 pending，保证落库与内存语义一致。
			iss.Disposition = DispositionPending
		}
		out = append(out, iss)
	}
	return out
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

// FilterOutPendingByCodes 移除 codes 命中且仍为 pending 的 issue，保留 dismissed
// 记录与范围外 issue。dismissed 的模式可能仍存在于新文本且为用户有意为之，
// 删除会让未来 QA 重跑时以 pending 复活骚扰用户。
// 自 pipeline/correct_handler.go 的 filterOutCodes 迁移而来，供 correct 与
// revise 两条修复轮共用，单一实现避免漂移。
func FilterOutPendingByCodes(issues []QualityIssue, drop map[string]struct{}) []QualityIssue {
	out := make([]QualityIssue, 0, len(issues))
	for _, iss := range issues {
		if _, ok := drop[iss.Code]; ok && !iss.Dismissed() {
			continue // 修复轮已声明解决且未 dismissed 的，移除
		}
		out = append(out, iss)
	}
	return out
}

// ReviseFinalIssues 组合修订轮写回的最终 issue 集合。
//
// 契约（与 correct 轮一致："声明修什么，就移除什么，其余判决不动"）：
//   - targetedCodes 范围内且仍 pending 的 issue 视为本轮已修复，移除；
//   - 范围外 pending 与 dismissed 记录一律保留；
//   - qaRan 时确定性 issue 以 fresh 重算（ReconcileIssues 按指纹继承旧裁决），
//     existing 中范围外的语义 issue 不由确定性 QA 维护，保留；
//   - qaRan=false 时不重算，existing 中非目标 issue 原样保留。
//
// 修订是声明性修复（无法像 correct 轮那样用幂等检查自证）：若 LLM 实际未修复，
// 仅当后续 semantic_qa 轮会重扫该段（scope=all；with_issues/with_issue_codes
// 作用域会跳过已无 issue 的段落）时才经 mergeSemanticQAIssues 重新检出；否则与
// 手动编辑/重译清除旧语义 issue 的既有语义一致——译文已变更，旧 issue 视为失效。
func ReviseFinalIssues(existing, fresh []QualityIssue, targetedCodes []string, qaRan bool) []QualityIssue {
	drop := make(map[string]struct{}, len(targetedCodes))
	for _, code := range targetedCodes {
		drop[code] = struct{}{}
	}
	kept := FilterOutPendingByCodes(existing, drop)
	if !qaRan {
		return kept
	}
	out := ReconcileIssues(fresh, kept)
	for _, iss := range kept {
		if IsSemanticQACode(iss.Code) {
			out = append(out, iss)
		}
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
		NewPunctuationSurplusChecker(),
		NewPunctuationWrapLossChecker(),
		NewWhitespaceIrregularChecker(),
		NewRepeatedSpaceChecker(cfg.TargetLang),
		NewWidthMixChecker(cfg.TargetLang),
		NewScriptMismatchChecker(cfg.TargetLang),
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
