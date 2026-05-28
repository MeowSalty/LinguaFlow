package protect

import (
	"regexp"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// CodeProtector 保护 Markdown 代码：围栏块（``` 或 ~~~）和行内代码（`...`）。
type CodeProtector struct{}

func (CodeProtector) Name() string { return "code" }

// 围栏代码块：``` ... ``` 或 ~~~ ... ~~~（Go RE2 不支持反向引用，故分两种）
// (?ms) → 多行 + . 匹配换行；非贪婪。
var (
	fencedBacktickRe = regexp.MustCompile("(?ms)^[ \\t]*`{3,}[^\\n]*\\n.*?\\n[ \\t]*`{3,}[ \\t]*$")
	fencedTildeRe    = regexp.MustCompile("(?ms)^[ \\t]*~{3,}[^\\n]*\\n.*?\\n[ \\t]*~{3,}[ \\t]*$")
)

// 行内代码：单个或多个反引号包裹
var inlineCodeRe = regexp.MustCompile("`+[^`\n]+`+")

func (p *CodeProtector) Protect(seg *pipeline.Segment) error {
	s := seg.Source
	for _, re := range []*regexp.Regexp{fencedBacktickRe, fencedTildeRe, inlineCodeRe} {
		s = re.ReplaceAllStringFunc(s, func(match string) string {
			k := nextKey(seg)
			seg.Protected[k] = match
			return k
		})
	}
	seg.Source = s
	return nil
}

func (p *CodeProtector) Unprotect(seg *pipeline.Segment) error {
	seg.Target = restoreAll(seg.Target, seg.Protected)
	return nil
}
