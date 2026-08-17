// Package correct 提供纯本地的机械修正轮次规则引擎。
// 与 QA 只读不同，correct 是 issue 流的消费者，可改写段译文；
// 规则按配置顺序执行，首个生效改写即停（不叠加），幂等由 handler 用同一 checker 重跑验证。
package correct

import "github.com/MeowSalty/LinguaFlow/backend/internal/model"

// Rule name whitelist (single source for validation + OpenAPI enum).
const (
	RulePunctuationMissingWrap = "punctuation_missing_wrap"
)

// AllRuleNames returns every built-in correct rule name, in canonical order.
func AllRuleNames() []string {
	return []string{RulePunctuationMissingWrap}
}

// CorrectionResult is the outcome of applying one Rule to a segment.
type CorrectionResult struct {
	Changed       bool
	NewTarget     string
	Op            string   // audit op, e.g. "punctuation_missing.wrap"
	ResolvedCodes []string // issue codes this rule claims to resolve
	Reason        string   // populated when Changed=false to explain the no-op
}

// Rule is a single mechanical correction.
type Rule interface {
	Name() string
	Apply(seg *model.Segment) CorrectionResult
	// ResolvedCodes returns the issue codes this rule resolves when it applies.
	ResolvedCodes() []string
}

// Engine runs an ordered list of enabled rules.
type Engine struct {
	rules []Rule
	codes []string // union of ResolvedCodes
}

// Config is the engine config (matches the ent/OpenAPI CorrectRoundConfig shape).
type Config struct {
	Rules       []RuleConfig
	Concurrency int
}

// RuleConfig is one rule entry.
type RuleConfig struct {
	Name    string
	Enabled bool
}

// New builds an Engine from cfg. If no enabled rules, returns an Engine
// that is a no-op (Apply returns Changed=false for everything). Unknown rule names
// are ignored (whitelist). Rule ORDER follows cfg.Rules order.
func New(cfg Config) *Engine {
	e := &Engine{}
	register := map[string]func() Rule{
		RulePunctuationMissingWrap: func() Rule { return &PunctuationMissingWrapRule{} },
	}
	enabledByName := make(map[string]bool, len(cfg.Rules))
	for _, rc := range cfg.Rules {
		enabledByName[rc.Name] = rc.Enabled
	}
	// canonical order: AllRuleNames(); but only include enabled & known rules.
	for _, name := range AllRuleNames() {
		if !enabledByName[name] {
			continue
		}
		ctor, ok := register[name]
		if !ok {
			continue
		}
		r := ctor()
		e.rules = append(e.rules, r)
		e.codes = mergeCodes(e.codes, r.ResolvedCodes())
	}
	return e
}

// Apply runs rules in order; returns the FIRST Changed=true result (no stacking).
// If no rule changes the segment, returns the last no-op result (Changed=false).
func (e *Engine) Apply(seg *model.Segment) CorrectionResult {
	if len(e.rules) == 0 {
		return CorrectionResult{}
	}
	var last CorrectionResult
	for _, r := range e.rules {
		res := r.Apply(seg)
		last = res
		if res.Changed {
			return res
		}
	}
	return last
}

// ConsumedIssueCodes returns the union of all enabled rules' ResolvedCodes,
// for the handler to build its idempotency checker engine.
func (e *Engine) ConsumedIssueCodes() []string {
	if len(e.codes) == 0 {
		return nil
	}
	out := append([]string(nil), e.codes...)
	return out
}

// Enabled reports whether the engine has any active rules.
func (e *Engine) Enabled() bool { return len(e.rules) > 0 }

func mergeCodes(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
