package service

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// strategyTestTranslateRound 构造带 protect/ruby 策略的 translate 轮快照。
func strategyTestTranslateRound() JobRoundSnapshot {
	return JobRoundSnapshot{
		Mode: "translate",
		Translate: &JobTranslateRoundSnapshot{
			Strategy: StrategySnapshot{
				Protect: schema.ProfileProtectConfig{Enabled: true, Rules: []string{"code"}},
				Ruby:    schema.ProfileRubyConfig{Enabled: true, PreserveKinds: []string{"phonetic"}},
			},
		},
	}
}

// TestRevisionRoundFromSnapshot_MaterializesBorrowedStrategy 修订预览的单轮快照
// （复刻已配置 revise 轮与从 translate 轮合成两条路径）都必须在裁剪前物化借用的
// protect/ruby 策略，否则裁剪后工厂级借用必然落空、预览与真实 revise 轮分叉。
func TestRevisionRoundFromSnapshot_MaterializesBorrowedStrategy(t *testing.T) {
	issues := []qa.QualityIssue{{Code: "calque", Message: "借译"}}

	t.Run("复刻已配置 revise 轮", func(t *testing.T) {
		snapshot := &JobExecutionSnapshot{
			Rounds: []JobRoundSnapshot{
				strategyTestTranslateRound(),
				{Mode: "revise", Revise: &JobReviseRoundSnapshot{BatchSize: 5}},
			},
		}
		round, synthesized, err := revisionRoundFromSnapshot(snapshot, issues)
		if err != nil || synthesized {
			t.Fatalf("round=%+v synthesized=%v err=%v", round, synthesized, err)
		}
		if len(round.Revise.ProtectRules) != 1 || round.Revise.ProtectRules[0] != "code" {
			t.Fatalf("ProtectRules=%v want [code]", round.Revise.ProtectRules)
		}
		if !round.Revise.RubyEnabled || len(round.Revise.RubyPreserveKinds) != 1 {
			t.Fatalf("ruby 策略未物化: enabled=%v kinds=%v", round.Revise.RubyEnabled, round.Revise.RubyPreserveKinds)
		}
		// 原快照中的 revise 轮本身不得被改动（复刻而非原地修改）。
		orig := snapshot.Rounds[1].Revise
		if len(orig.ProtectRules) != 0 || orig.RubyEnabled {
			t.Fatalf("原快照 revise 轮被修改: %+v", orig)
		}
	})

	t.Run("从 translate 轮合成", func(t *testing.T) {
		snapshot := &JobExecutionSnapshot{
			Rounds: []JobRoundSnapshot{strategyTestTranslateRound()},
		}
		round, synthesized, err := revisionRoundFromSnapshot(snapshot, issues)
		if err != nil || !synthesized {
			t.Fatalf("round=%+v synthesized=%v err=%v", round, synthesized, err)
		}
		if len(round.Revise.ProtectRules) != 1 || !round.Revise.RubyEnabled {
			t.Fatalf("合成轮未物化借用策略: %+v", round.Revise)
		}
	})

	t.Run("无 translate 轮降级零值", func(t *testing.T) {
		snapshot := &JobExecutionSnapshot{
			Rounds: []JobRoundSnapshot{
				{Mode: "revise", Revise: &JobReviseRoundSnapshot{BatchSize: 5}},
			},
		}
		round, _, err := revisionRoundFromSnapshot(snapshot, issues)
		if err != nil {
			t.Fatal(err)
		}
		if len(round.Revise.ProtectRules) != 0 || round.Revise.RubyEnabled {
			t.Fatalf("无 translate 轮应物化零值，实际 %+v", round.Revise)
		}
	})
}
