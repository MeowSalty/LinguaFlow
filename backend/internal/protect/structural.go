package protect

import (
	"strings"
	"unicode"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// Analysis 是一次保护链结构分析的结果：结构段判定（Structural）与保护链产物
// （Protected 保护后文本、Mapping「占位符 → 原片段」映射、Meta 链产生的段级副作用）。
// 判定与保护来自同一次链执行——调用方经 Apply 落盘产物即可完成保护阶段，
// 无须对同一段文本二次执行保护链。
// ran 标记链是否真正执行过：短路（原文无字母/数字）与 nil p 时为 false，
// 此时产物全部为零值、Apply 无副作用。保护链失败时 Err 非空（Structural 按
// 原文 fail-safe 判为有内容，产物为零值），落盘前须先检查 Err。
// Meta 承载保护链写入段级 Meta 的副作用（如 ruby protector 的 ruby_items）；
// 仅在链产生该副作用时非 nil，Apply 时合并进 seg.Meta。
type Analysis struct {
	Structural bool
	Protected  string
	Mapping    map[string]string
	Meta       map[string]any
	Err        error
	ran        bool
}

// AnalyzeStructural 在保护链 p 下分析文本：判定是否结构段，并保留保护链产物。
// 判定语义与 IsStructuralOnly 完全一致（同一实现，见其文档）；
// 映射 key 固定 14 字符且互无前缀关系，逐 key ReplaceAll 剥除与顺序无关；
// 原文里的字面量 __LF_NNNNNN__ 因 nextKey 冲突跳过不进映射，
// 残留在残余中（含字母数字）→ 判为有内容，fail-safe 方向正确。
func AnalyzeStructural(p Protector, text string) Analysis {
	if !hasLetterOrDigit(text) {
		return Analysis{Structural: true}
	}
	if p == nil {
		return Analysis{}
	}
	seg := &model.Segment{Source: text}
	if err := p.Protect(seg); err != nil {
		return Analysis{Err: err}
	}
	residual := seg.Source
	for k := range seg.Protected {
		residual = strings.ReplaceAll(residual, k, "")
	}
	var meta map[string]any
	if len(seg.Meta) > 0 {
		meta = seg.Meta
	}
	return Analysis{
		Structural: !hasLetterOrDigit(residual),
		Protected:  seg.Source,
		Mapping:    seg.Protected,
		Meta:       meta,
		ran:        true,
	}
}

// IsStructuralOnly 判断文本在保护链 p 下是否为"结构段"：
// 复用 ProtectText 的完整保护链（含 link 子跨度替换、相邻占位符合并、ruby 剥离）
// 得到占位符形态文本，再将映射 key 全部替换为空得残余文本；
// 残余不含任何字母或数字（汉字属 Letter 类别）即为结构段——无可译文本，仅剩空白/标点。
// 判定随 p 的配置浮动：保护链会把哪些跨度变成占位符，本函数就剥掉哪些跨度，
// 与翻译轮的实际保护行为永远同源，不另立第二套口径。
// 原文本身无字母/数字时直接短路返回 true（残余是原文字符集的子集），
// 纯标点/空段零正则成本。
// p 为 nil 时按原文本判定（无保护配置的降级路径，纯标点仍命中）。
// ProtectText 出错时按原文本判定（fail-safe：宁可判为有内容，绝不静默丢弃可译内容）。
func IsStructuralOnly(p Protector, text string) bool {
	return AnalyzeStructural(p, text).Structural
}

// Apply 将分析产物落盘到 seg：Source 换为保护后文本、Protected 换为映射、
// Meta 副作用合并进 seg.Meta——与对同一文本直接执行 p.Protect 的落盘结果
// 完全一致（同一链的同一次执行）。链未跑过（短路/nil p）时无副作用；
// 链跑过但无占位符时仍落盘 Source 与 Meta（如 ruby-only 段：Mapping 为空
// 但 Source 已被剥离注音、Meta 已写入 ruby_items）。
// Err 非空时返回该错误（与直接 Protect 失败同语义），不改动 seg。
func (a Analysis) Apply(seg *model.Segment) error {
	if a.Err != nil {
		return a.Err
	}
	if !a.ran {
		return nil
	}
	seg.Source = a.Protected
	seg.Protected = a.Mapping
	for k, v := range a.Meta {
		if seg.Meta == nil {
			seg.Meta = make(map[string]any)
		}
		seg.Meta[k] = v
	}
	return nil
}

// hasLetterOrDigit 判断文本是否含任何 Unicode 字母或数字（汉字属 Letter 类别）。
func hasLetterOrDigit(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
