// Package protect 提供「保护-还原」机制：把不应被翻译的片段（代码、链接、占位符、XML 标签）
// 替换为形如 __LF_0001__ 的占位符；翻译后按映射还原。
package protect

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// mergeAdjacentPlaceholderRE 匹配一个占位符后跟一个或多个（可选空白 + 占位符）的序列。
// 当两个占位符之间没有非空白字符（或只有空白字符）时，视为相邻，可以合并。
// 例如 __LF_000001____LF_000002__ 或 __LF_000001__ __LF_000002__ 都会匹配。
var mergeAdjacentPlaceholderRE = regexp.MustCompile(`__LF_\d{6}__(?:\s*__LF_\d{6}__)+`)

// singlePlaceholderRE 匹配单个 __LF_NNNNNN__ 占位符，用于从合并序列中提取各个占位符。
var singlePlaceholderRE = regexp.MustCompile(`__LF_\d{6}__`)

// Protector 把不应翻译的片段替换为占位符，并在翻译后还原。
type Protector interface {
	Name() string
	Protect(seg *model.Segment) error
	Unprotect(seg *model.Segment) error
}

// placeholderFmt 形如 __LF_000001__。固定 6 位数字、总长 14，
// 容纳百万级占位符仍宽度一致；纯 ASCII，主流 BPE 不易拆分。
const placeholderFmt = "__LF_%06d__"

// nextKey 在 seg.Protected 中分配下一个未使用的占位符 key。
func nextKey(seg *model.Segment) string {
	if seg.Protected == nil {
		seg.Protected = make(map[string]string)
	}
	i := len(seg.Protected) + 1
	for {
		k := fmt.Sprintf(placeholderFmt, i)
		if _, exists := seg.Protected[k]; !exists {
			return k
		}
		i++
	}
}

// composed 串行调用多个 Protector：protect 按声明顺序；
// unprotect 在顶层一次性还原（基于全部占位符），避免子 protector
// 各自 restoreAll 时对「已回填内容中再次出现的占位符字面」二次替换。
type composed struct{ ps []Protector }

func Compose(ps ...Protector) Protector { return &composed{ps: ps} }

func (c *composed) Name() string { return "composed" }

func (c *composed) Protect(seg *model.Segment) error {
	for _, p := range c.ps {
		if err := p.Protect(seg); err != nil {
			return fmt.Errorf("%s.protect: %w", p.Name(), err)
		}
	}
	// 合并相邻占位符为单个占位符，减少 LLM 需要保留的占位符数量。
	// 例如 __LF_000001____LF_000002__ → __LF_NNNNNN__（映射为两者拼接）
	mergeAdjacentPlaceholders(seg)
	return nil
}

func (c *composed) Unprotect(seg *model.Segment) error {
	seg.Target = restoreAll(seg.Target, seg.Protected)
	return nil
}

// mergeAdjacentPlaceholders 扫描 seg.Source 中的相邻占位符序列，将每个序列合并为
// 单个占位符。合并后的新占位符映射为原始占位符映射的拼接（保留中间的空白字符）。
// 这减少了 LLM 需要保留的占位符数量，降低占位符丢失的概率。
//
// 例如，给定：
//
//	__LF_000001____LF_000002__じゅ__LF_000003____LF_000004__
//
// 产生：
//
//	__LF_NNNNNN__じゅ__LF_MMMMMM__
//
// 其中每个合并占位符的值是原始占位符值的拼接。
func mergeAdjacentPlaceholders(seg *model.Segment) {
	if len(seg.Protected) == 0 {
		return
	}
	seg.Source = mergeAdjacentPlaceholderRE.ReplaceAllStringFunc(seg.Source, func(match string) string {
		locs := singlePlaceholderRE.FindAllStringIndex(match, -1)
		if len(locs) < 2 {
			return match
		}

		// 拼接所有原始占位符的值，保留占位符之间的空白字符。
		var mergedValue strings.Builder
		for i, loc := range locs {
			ph := match[loc[0]:loc[1]]
			if i > 0 {
				// 保留前一个占位符与当前占位符之间的空白。
				between := match[locs[i-1][1]:loc[0]]
				mergedValue.WriteString(between)
			}
			if val, ok := seg.Protected[ph]; ok {
				mergedValue.WriteString(val)
			}
		}

		// 分配合并占位符的唯一 key。
		newKey := nextKey(seg)
		seg.Protected[newKey] = mergedValue.String()

		// 删除已合并的原始条目。
		for _, loc := range locs {
			delete(seg.Protected, match[loc[0]:loc[1]])
		}

		return newKey
	})
}

// restoreAll 把 text 中出现的所有占位符替换回原内容。
// 按 key 长度从长到短遍历，避免占位符之间存在前缀关系时被错误替换。
func restoreAll(text string, protected map[string]string) string {
	if len(protected) == 0 {
		return text
	}
	keys := make([]string, 0, len(protected))
	for k := range protected {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] > keys[j]
	})
	for _, k := range keys {
		if strings.Contains(text, k) {
			text = strings.ReplaceAll(text, k, protected[k])
		}
	}
	return text
}

// MissingPlaceholders 返回 seg.Protected 中未出现在 seg.Target 里的占位符 key。
// 用于 translate 阶段在 LLM 返回后做完整性校验。结果按 key 升序排序，便于日志稳定输出。
func MissingPlaceholders(seg *model.Segment) []string {
	if len(seg.Protected) == 0 {
		return nil
	}
	var missing []string
	for k := range seg.Protected {
		if !strings.Contains(seg.Target, k) {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing
}

// FromRules 按规则名（"code"/"link"/"placeholder"/"xml"）构造 Protector。
// 未知规则名会被忽略。
//
// 注意：不再处理 "ruby"，RubyProtector 由 BuildPreStages/BuildPostStages 根据 ruby.enabled
// 单独控制，在其他 Protector 之前运行（先剥离 ruby 标签，再处理剩余 XML）。
func FromRules(rules []string) Protector {
	var ps []Protector
	for _, r := range rules {
		switch r {
		case "code":
			ps = append(ps, &CodeProtector{})
		case "link":
			ps = append(ps, &LinkProtector{})
		case "placeholder":
			ps = append(ps, &PlaceholderProtector{})
		case "xml":
			ps = append(ps, &XMLProtector{})
		}
	}
	return Compose(ps...)
}
