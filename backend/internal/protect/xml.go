package protect

import (
	"regexp"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// XMLProtector 保护 XML / HTML 标签整体（包括属性），但保留标签之间的文本以便翻译。
type XMLProtector struct{}

func (XMLProtector) Name() string { return "xml" }

// 匹配 <tag>, </tag>, <tag attr="...">, <tag/>，但不匹配 < 后跟空格的情况
var xmlTagRe = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9:-]*(?:\s+[^<>]*)?/?>`)

func (p *XMLProtector) Protect(seg *pipeline.Segment) error {
	seg.Source = xmlTagRe.ReplaceAllStringFunc(seg.Source, func(match string) string {
		k := nextKey(seg)
		seg.Protected[k] = match
		return k
	})
	return nil
}

func (p *XMLProtector) Unprotect(seg *pipeline.Segment) error {
	seg.Target = restoreAll(seg.Target, seg.Protected)
	return nil
}
