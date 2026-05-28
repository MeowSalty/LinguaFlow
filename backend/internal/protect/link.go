package protect

import (
	"regexp"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// LinkProtector 保护 Markdown 链接的 URL/图片部分，但保留可见文本以便翻译。
// 处理：
//   - [text](url)        → [text](__LF_xxxx__)
//   - ![alt](url)        → ![alt](__LF_xxxx__)
//   - <https://...>      → __LF_xxxx__
//   - [text][ref]        → [text][__LF_xxxx__]
type LinkProtector struct{}

func (LinkProtector) Name() string { return "link" }

var (
	mdLinkRe   = regexp.MustCompile(`(!?\[[^\]]*\])\(([^)\s]+)(\s+"[^"]*")?\)`)
	autoLinkRe = regexp.MustCompile(`<(?:https?|ftp|mailto):[^>\s]+>`)
	refLinkRe  = regexp.MustCompile(`(\[[^\]]+\])\[([^\]]+)\]`)
)

func (p *LinkProtector) Protect(seg *pipeline.Segment) error {
	s := seg.Source
	// inline link / image：仅保护 URL 部分
	s = mdLinkRe.ReplaceAllStringFunc(s, func(match string) string {
		m := mdLinkRe.FindStringSubmatch(match)
		k := nextKey(seg)
		url := m[2]
		if m[3] != "" {
			url += m[3]
		}
		seg.Protected[k] = url
		return m[1] + "(" + k + ")"
	})
	// auto link：整体保护
	s = autoLinkRe.ReplaceAllStringFunc(s, func(match string) string {
		k := nextKey(seg)
		seg.Protected[k] = match
		return k
	})
	// reference link：保护 ref id
	s = refLinkRe.ReplaceAllStringFunc(s, func(match string) string {
		m := refLinkRe.FindStringSubmatch(match)
		k := nextKey(seg)
		seg.Protected[k] = m[2]
		return m[1] + "[" + k + "]"
	})
	seg.Source = s
	return nil
}

func (p *LinkProtector) Unprotect(seg *pipeline.Segment) error {
	seg.Target = restoreAll(seg.Target, seg.Protected)
	return nil
}
