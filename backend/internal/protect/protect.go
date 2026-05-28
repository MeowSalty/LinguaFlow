// Package protect 提供「保护-还原」机制：把不应被翻译的片段（代码、链接、占位符、XML 标签）
// 替换为形如 __LF_0001__ 的占位符；翻译后按映射还原。
package protect

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// Protector 把不应翻译的片段替换为占位符，并在翻译后还原。
type Protector interface {
	Name() string
	Protect(seg *pipeline.Segment) error
	Unprotect(seg *pipeline.Segment) error
}

// placeholderFmt 形如 __LF_000001__。固定 6 位数字、总长 14，
// 容纳百万级占位符仍宽度一致；纯 ASCII，主流 BPE 不易拆分。
const placeholderFmt = "__LF_%06d__"

// nextKey 在 seg.Protected 中分配下一个未使用的占位符 key。
func nextKey(seg *pipeline.Segment) string {
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

func (c *composed) Protect(seg *pipeline.Segment) error {
	for _, p := range c.ps {
		if err := p.Protect(seg); err != nil {
			return fmt.Errorf("%s.protect: %w", p.Name(), err)
		}
	}
	return nil
}

func (c *composed) Unprotect(seg *pipeline.Segment) error {
	seg.Target = restoreAll(seg.Target, seg.Protected)
	return nil
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
func MissingPlaceholders(seg *pipeline.Segment) []string {
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
