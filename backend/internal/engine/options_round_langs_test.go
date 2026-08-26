package engine

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/correct"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// TestBuildRoundConfigsPropagatesLangs 回归：buildRoundConfigs 必须把引擎级
// SourceLang/TargetLang 拷进每一个 RoundConfig。correct 轮的幂等引擎依赖
// rc.TargetLang 构造语言敏感 checker（如 width_mix 的 CJK 分支）：通路断裂时
// 空 lang checker 走拉丁分支，CJK 目标的正常改写被幂等回滚，规则静默失效成环。
func TestBuildRoundConfigsPropagatesLangs(t *testing.T) {
	cfg := &Config{SourceLang: "en", TargetLang: "zh"}
	rounds := []Round{
		{
			RoundIndex:   0,
			Mode:         pipeline.RoundModeCorrect,
			CorrectRules: []correct.RuleConfig{{Name: correct.RulePunctuationMissingWrap, Enabled: true}},
		},
		{RoundIndex: 1}, // Mode 为空默认 translate：langs 应对所有模式的轮次生效
	}

	rcs := buildRoundConfigs(rounds, cfg)
	if len(rcs) != len(rounds) {
		t.Fatalf("len(rcs)=%d, want %d", len(rcs), len(rounds))
	}
	for i, rc := range rcs {
		if rc.SourceLang != cfg.SourceLang {
			t.Errorf("rcs[%d].SourceLang=%q, want %q", i, rc.SourceLang, cfg.SourceLang)
		}
		if rc.TargetLang != cfg.TargetLang {
			t.Errorf("rcs[%d].TargetLang=%q, want %q", i, rc.TargetLang, cfg.TargetLang)
		}
	}

	// correct 轮的特有配置不受影响（langs 是引擎级上下文，不挤占规则配置）
	if rcs[0].Correct == nil || len(rcs[0].Correct.Rules) != 1 {
		t.Fatalf("correct round config lost rules: %+v", rcs[0].Correct)
	}
}
