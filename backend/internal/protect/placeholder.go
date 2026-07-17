package protect

import (
	"regexp"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// PlaceholderProtector 保护程序员常用的占位语法：
//   - {{var}} / {{ .Field }}
//   - {var} / {0}
//   - %s / %d / %v / %.2f
//   - $VAR / ${VAR}
type PlaceholderProtector struct{}

func (PlaceholderProtector) Name() string { return "placeholder" }

var (
	doubleBraceRe = regexp.MustCompile(`\{\{[^{}]+\}\}`)
	singleBraceRe = regexp.MustCompile(`\{[A-Za-z0-9_.]+\}`)
	printfVerbRe  = regexp.MustCompile(`%[+\-#0 ]?[0-9]*(?:\.[0-9]+)?[a-zA-Z]`)
	// shellVarRe 排除 $_ 开头，以避免误吞已被替换的 __LF_xxxx__ 占位符。
	shellVarRe = regexp.MustCompile(`\$(?:\{[A-Za-z_][A-Za-z0-9_]*\}|[A-Za-z][A-Za-z0-9_]*)`)
)

func (p *PlaceholderProtector) Protect(seg *model.Segment) error {
	s := seg.Source
	for _, re := range []*regexp.Regexp{doubleBraceRe, singleBraceRe, printfVerbRe, shellVarRe} {
		s = re.ReplaceAllStringFunc(s, func(match string) string {
			k := nextKey(seg)
			seg.Protected[k] = match
			return k
		})
	}
	seg.Source = s
	return nil
}

func (p *PlaceholderProtector) Unprotect(seg *model.Segment) error {
	seg.Target = restoreAll(seg.Target, seg.Protected)
	return nil
}
