package correct

// NewWithRules 直接以 Rule 实例构建 Engine：保持传入顺序、不做白名单过滤。
//
// 生产配置仍走 New（按 OpenAPI 白名单 + rule-level enabled 过滤）；本构造器
// 服务于测试注入自定义 Rule，以及未来动态规则组合的扩展点。codes 为各规则
// ResolvedCodes 的去重并集，语义与 New 一致，供 handler 构建幂等引擎。
func NewWithRules(rules []Rule) *Engine {
	e := &Engine{}
	for _, r := range rules {
		if r == nil {
			continue
		}
		e.rules = append(e.rules, r)
		e.codes = mergeCodes(e.codes, r.ResolvedCodes())
	}
	return e
}
