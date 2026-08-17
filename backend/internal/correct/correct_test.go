package correct

import (
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// fakeAlwaysRule 恒定返回 Changed=true，用于验证"首个 Changed 即停"。
type fakeAlwaysRule struct{}

func (fakeAlwaysRule) Name() string { return "fake_always" }
func (fakeAlwaysRule) Apply(seg *model.Segment) CorrectionResult {
	return CorrectionResult{Changed: true, NewTarget: seg.Target + "!", Op: "fake.always"}
}
func (fakeAlwaysRule) ResolvedCodes() []string { return []string{"fake_code"} }

// fakeNeverRule 恒定 no-op，并记录调用次数。
type fakeNeverRule struct{ calls *int }

func (r fakeNeverRule) Name() string { return "fake_never" }
func (r fakeNeverRule) Apply(_ *model.Segment) CorrectionResult {
	*r.calls++
	return CorrectionResult{Reason: "never changes"}
}
func (fakeNeverRule) ResolvedCodes() []string { return nil }

func TestEngine_DisabledIsNoop(t *testing.T) {
	for _, cfg := range []Config{
		{Rules: []RuleConfig{}},                // 无规则
		{Rules: []RuleConfig{{Name: "bogus"}}}, // 未知规则名被忽略
		{Rules: []RuleConfig{{Name: "bogus", Enabled: true}}},
	} {
		e := New(cfg)
		if e.Enabled() {
			t.Errorf("cfg=%+v: engine should be disabled", cfg)
		}
		seg := &model.Segment{
			Source: "「对话」",
			Target: "对话",
		}
		res := e.Apply(seg)
		if res.Changed {
			t.Errorf("cfg=%+v: Apply should be no-op, got %+v", cfg, res)
		}
		if e.ConsumedIssueCodes() != nil {
			t.Errorf("cfg=%+v: ConsumedIssueCodes should be nil, got %v", cfg, e.ConsumedIssueCodes())
		}
	}
}

func TestEngine_SingleRoundMultiRuleOrder(t *testing.T) {
	// 1) 第一个规则恒定 Changed：应返回其结果且不再评估后续规则。
	neverCalls := 0
	e1 := &Engine{rules: []Rule{fakeAlwaysRule{}, fakeNeverRule{&neverCalls}}}
	seg := &model.Segment{Target: "对话"}
	res := e1.Apply(seg)
	if !res.Changed || res.Op != "fake.always" || res.NewTarget != "对话!" {
		t.Fatalf("res=%+v", res)
	}
	if neverCalls != 0 {
		t.Fatalf("second rule should not be evaluated after first Changed, calls=%d", neverCalls)
	}

	// 2) 第一个 no-op：第二个才被评估并返回其 Changed 结果。
	neverCalls2 := 0
	e2 := &Engine{rules: []Rule{fakeNeverRule{&neverCalls2}, fakeAlwaysRule{}}}
	res2 := e2.Apply(seg)
	if !res2.Changed || res2.Op != "fake.always" || res2.NewTarget != "对话!" {
		t.Fatalf("res2=%+v", res2)
	}
	if neverCalls2 != 1 {
		t.Fatalf("first rule should be evaluated once, calls=%d", neverCalls2)
	}

	// 3) 全 no-op：返回最后一个 no-op 结果。
	neverCalls3 := 0
	e3 := &Engine{rules: []Rule{fakeNeverRule{&neverCalls3}}}
	res3 := e3.Apply(seg)
	if res3.Changed || res3.Reason != "never changes" {
		t.Fatalf("res3=%+v", res3)
	}
	if neverCalls3 != 1 {
		t.Fatalf("calls=%d", neverCalls3)
	}
}

func TestEngine_ApplyEmptyRulesReturnsZero(t *testing.T) {
	e := &Engine{}
	res := e.Apply(&model.Segment{Target: "对话"})
	if res.Changed || res.NewTarget != "" || res.Op != "" || res.Reason != "" || len(res.ResolvedCodes) != 0 {
		t.Fatalf("res=%+v, want zero CorrectionResult", res)
	}
}

func TestEngine_ConsumedIssueCodesUnionDedup(t *testing.T) {
	e := &Engine{
		rules: []Rule{fakeAlwaysRule{}, fakeAlwaysRule{}},
		codes: mergeCodes(mergeCodes(nil, []string{"a", "b"}), []string{"b", "c"}),
	}
	got := e.ConsumedIssueCodes()
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
}
