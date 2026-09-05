package qa

// RecheckFinalIssues 组合重检写回的最终 issue 集合。
//
// 契约（与 ReviseFinalIssues 的组合方式镜像，见 qa.go 的 ReviseFinalIssues）：
//   - fresh 为本次重算的确定性 issues（per-batch checker + 文档级
//     duplicate_source_divergence），经 ReconcileIssues 与 existing 对账：
//     同指纹继承旧裁决（dismissed 等），新指纹保持 pending；
//   - existing 中的语义类（IsSemanticQACode）与守恒类（ruby_restore_incomplete /
//     ruby_tag_loss）issue 原样追加——其维护者是 semantic_qa 轮与各写路径，
//     重检不可重算也不应清除；
//   - existing 中其余确定性 issue 若指纹不在 fresh 中则自然清除
//     （配置声明的检查集合即为真相，问题没了就是没了）。
func RecheckFinalIssues(fresh, existing []QualityIssue) []QualityIssue {
	out := ReconcileIssues(fresh, existing)
	for _, iss := range existing {
		if IsSemanticQACode(iss.Code) || IsConservationCode(iss.Code) {
			out = append(out, iss)
		}
	}
	return out
}
