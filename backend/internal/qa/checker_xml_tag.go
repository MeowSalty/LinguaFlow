package qa

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/markup"
)

// XMLTagMismatchChecker 对源/译做两条正交的 XML 判定：
//  1. 结构合法性——译文必须能作为 well-formed 片段嵌入 XHTML 元素内容
//     （markup.TargetRegression 自带「源文含标签且源文合法」的门禁，与资源格式
//     无关，因此这里不做格式过滤）。ruby 族在此**不能**排除：preserve_kinds
//     合法移除全部 ruby 后译文仍然平衡，结构判定不会误报；而译文丢一个
//     </ruby> 之类的损坏（标签总数守恒但嵌套非法）只有结构判定能抓到。
//  2. 标签守恒——比对源/译 XML 标签多重集合。ruby 族在此排除：preserve_kinds
//     过滤会合法地从译文移除这些标签，计入多重集会与配置耦合误报。
//
// 结构判定优先：命中时只报结构问题，不再为同段追加多重集 issue——结构损坏比
// 标签计数不一致更具体、更可操作，且保持每段最多一条 xml_tag_mismatch。
type XMLTagMismatchChecker struct{}

// NewXMLTagMismatchChecker 创建 XML 标签守恒检测器。
func NewXMLTagMismatchChecker() *XMLTagMismatchChecker {
	return &XMLTagMismatchChecker{}
}

func (c *XMLTagMismatchChecker) Name() string { return CheckXMLTagMismatch }

// 与 protect.XMLProtector 一致：匹配 <tag> / </tag> / <tag attr> / <tag/>。
var xmlTagTokenRe = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9:-]*(?:\s+[^<>]*)?/?>`)

// rubyTagFamily 是 HTML ruby 注音标签族。其守恒由 ruby 还原子系统独立负责，
// 与通用 XML 标签守恒正交：preserve_kinds 过滤会合法地从译文移除这些标签，
// 因此在多重集比对中一律排除，避免配置耦合误报。
var rubyTagFamily = map[string]struct{}{
	"ruby": {},
	"rt":   {},
	"rp":   {},
	"rb":   {},
}

// xmlTagNameRe 从标签 token 中提取标签名。
var xmlTagNameRe = regexp.MustCompile(`^</?([A-Za-z][A-Za-z0-9:-]*)`)

func (c *XMLTagMismatchChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		src := seg.SourceText
		tgt := seg.TargetText
		if strings.TrimSpace(src) == "" {
			continue
		}
		// 结构合法性判定先于多重集比对：多重集的门禁（提取后非空）会跳过
		// 纯 ruby 源文，而那里的结构损坏恰恰必须被抓到。
		if err := markup.TargetRegression(src, tgt); err != nil {
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityError,
				Code:         CheckXMLTagMismatch,
				Message:      fmt.Sprintf("译文 XML 结构损坏（标签未闭合或嵌套错误）：%s", err),
				Span:         &Span{MatchedText: markupCulprit(tgt)},
			})
			continue
		}
		srcTags := extractXMLTagMultiset(src)
		if len(srcTags) == 0 {
			continue
		}
		tgtTags := extractXMLTagMultiset(tgt)
		if multisetEqual(srcTags, tgtTags) {
			continue
		}
		issues = append(issues, QualityIssue{
			SegmentIndex: seg.Index,
			Severity:     SeverityError,
			Code:         CheckXMLTagMismatch,
			Message:      fmt.Sprintf("XML 标签不一致：原文 %v，译文 %v", keysOf(srcTags), keysOf(tgtTags)),
		})
	}
	return issues
}

func extractXMLTagMultiset(text string) map[string]int {
	counts := make(map[string]int)
	for _, m := range xmlTagTokenRe.FindAllString(text, -1) {
		nameMatch := xmlTagNameRe.FindStringSubmatch(m)
		if len(nameMatch) >= 2 {
			if _, skip := rubyTagFamily[strings.ToLower(nameMatch[1])]; skip {
				continue
			}
		}
		counts[m]++
	}
	return counts
}

// markupCulprit 在 target 中定位触发结构错误的标签 token，作为 issue 的指纹
// 载体（Fingerprint = code:matched_text）：优先多余的闭标签，其次最内层未闭合
// 的开标签；裸 & 等非标签错误退回末个标签 token，仍无标签时退回片段本身。
// 与编码器严格校验的报错点未必逐字一致，但足以把不同的损坏点区分开，
// 且回滚守卫与 ReconcileIssues 只依赖指纹的稳定性。
func markupCulprit(target string) string {
	tokens := xmlTagTokenRe.FindAllString(target, -1)
	type openTag struct {
		name string
		tok  string
	}
	var stack []openTag
	for _, tok := range tokens {
		nameMatch := xmlTagNameRe.FindStringSubmatch(tok)
		if nameMatch == nil {
			continue
		}
		name := strings.ToLower(nameMatch[1])
		if strings.HasPrefix(tok, "</") {
			matched := false
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].name == name {
					stack = stack[:i]
					matched = true
					break
				}
			}
			if !matched {
				return tok
			}
			continue
		}
		if !strings.HasSuffix(tok, "/>") {
			stack = append(stack, openTag{name: name, tok: tok})
		}
	}
	if len(stack) > 0 {
		return stack[len(stack)-1].tok
	}
	if len(tokens) > 0 {
		return tokens[len(tokens)-1]
	}
	return target
}
