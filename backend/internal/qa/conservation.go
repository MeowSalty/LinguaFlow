package qa

import (
	"fmt"
	"strings"
)

// RubyTagLossIssues 检测人工编辑导致的译文注音全丢失：编辑前译文含
// <ruby> 注音标签而编辑后一条不剩时，产出一条 warning 级 issue。
//
// 判据用基线对比（编辑前 vs 编辑后）而非 source 对比：零配置依赖、
// 对 ruby 关闭的资源零误报（编辑前译文有注音即证明其存在过）。
// 检测为 "<ruby>" 字面计数——标签形态变形（大小写/属性）视为丢失，
// 这本身值得提醒。issue 不带 Span，指纹稳定为 code 本身：用户对一次
// 全丢失标注 dismissed 后，后续编辑若仍全丢失会继承该裁决，不再骚扰。
// 有意剥离全部注音属于合法操作，warning 可 dismiss，绝不阻塞保存。
func RubyTagLossIssues(segmentIndex int, prev, next string) []QualityIssue {
	n := strings.Count(prev, "<ruby>")
	if n > 0 && !strings.Contains(next, "<ruby>") {
		return []QualityIssue{{
			SegmentIndex: segmentIndex,
			Severity:     SeverityWarning,
			Code:         CodeRubyTagLoss,
			Message:      fmt.Sprintf("译文注音全部丢失：编辑前 %d 条", n),
		}}
	}
	return nil
}
