package worker

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestBuildEngineConfigExtractOnlyKeepsGlossaryEnabled(t *testing.T) {
	snapshot := &service.JobExecutionSnapshot{
		GlossaryEnabled: true,
		Rounds: []service.JobRoundSnapshot{
			{
				Mode:    "extract",
				Extract: &service.JobExtractRoundSnapshot{},
			},
		},
	}

	cfg := BuildEngineConfig(snapshot)
	if !cfg.Glossary.Enabled {
		t.Fatal("extract-only snapshot lost glossary enabled state")
	}
}

func TestBuildEngineConfigTranslateKeepsGlossarySettings(t *testing.T) {
	snapshot := &service.JobExecutionSnapshot{
		GlossaryEnabled: true,
		Rounds: []service.JobRoundSnapshot{
			{
				Mode: "translate",
				Translate: &service.JobTranslateRoundSnapshot{
					Strategy: service.StrategySnapshot{},
				},
			},
		},
	}

	cfg := BuildEngineConfig(snapshot)
	if !cfg.Glossary.Enabled {
		t.Fatal("translate snapshot lost glossary enabled state")
	}
}

// borrowTestTranslateRound 构造带 protect/ruby 策略的 translate 轮快照。
func borrowTestTranslateRound(protectEnabled bool, protectRules []string, rubyEnabled bool, rubyKinds []string) service.JobRoundSnapshot {
	return service.JobRoundSnapshot{
		Mode: "translate",
		Translate: &service.JobTranslateRoundSnapshot{
			Strategy: service.StrategySnapshot{
				Protect: schema.ProfileProtectConfig{Enabled: protectEnabled, Rules: protectRules},
				Ruby:    schema.ProfileRubyConfig{Enabled: rubyEnabled, PreserveKinds: rubyKinds},
			},
		},
	}
}

func TestBorrowTranslateProtectRuby_TakesFirstTranslateRound(t *testing.T) {
	wantRules := []string{"code", "link"}
	wantKinds := []string{"phonetic"}
	snapshot := &service.JobExecutionSnapshot{
		Rounds: []service.JobRoundSnapshot{
			borrowTestTranslateRound(true, wantRules, true, wantKinds),
			{Mode: "revise", Revise: &service.JobReviseRoundSnapshot{}},
			// 第二条 translate 轮不应覆盖第一条的借用结果。
			borrowTestTranslateRound(false, nil, false, nil),
		},
	}

	rules, rubyEnabled, kinds := borrowTranslateProtectRuby(snapshot)
	if len(rules) != 2 || rules[0] != "code" || rules[1] != "link" {
		t.Fatalf("protectRules = %v，want %v", rules, wantRules)
	}
	if !rubyEnabled {
		t.Fatal("rubyEnabled = false，want true")
	}
	if len(kinds) != 1 || kinds[0] != "phonetic" {
		t.Fatalf("rubyPreserveKinds = %v，want %v", kinds, wantKinds)
	}
}

func TestBorrowTranslateProtectRuby_NoTranslateRoundReturnsZero(t *testing.T) {
	snapshot := &service.JobExecutionSnapshot{
		Rounds: []service.JobRoundSnapshot{
			{Mode: "revise", Revise: &service.JobReviseRoundSnapshot{}},
		},
	}

	rules, rubyEnabled, kinds := borrowTranslateProtectRuby(snapshot)
	if len(rules) != 0 || rubyEnabled || len(kinds) != 0 {
		t.Fatalf("无 translate 轮应返回零值，实际 rules=%v rubyEnabled=%v kinds=%v", rules, rubyEnabled, kinds)
	}

	// Protect.Enabled=false 时即使配置了 Rules 也视为未启用（与 translate 轮语义一致）。
	disabled := &service.JobExecutionSnapshot{
		Rounds: []service.JobRoundSnapshot{
			borrowTestTranslateRound(false, []string{"link"}, false, nil),
		},
	}
	rules, _, _ = borrowTranslateProtectRuby(disabled)
	if len(rules) != 0 {
		t.Fatalf("Protect 未启用时应忽略 Rules，实际 %v", rules)
	}
}

// TestBuildReviseRound_BorrowsTranslateProtectRuby 验证 buildReviseRound 把借用的
// protect/ruby 配置写入 engine.Round（随后经 engine 层注入 ReviseHandler）；
// 无 translate 轮借用零值时 Round 对应字段为零值。
func TestBuildReviseRound_BorrowsTranslateProtectRuby(t *testing.T) {
	reviseSnap := service.JobRoundSnapshot{
		Mode:   "revise",
		Revise: &service.JobReviseRoundSnapshot{},
	}

	t.Run("有 translate 轮则借用", func(t *testing.T) {
		snapshot := &service.JobExecutionSnapshot{
			Rounds: []service.JobRoundSnapshot{
				borrowTestTranslateRound(true, []string{"code"}, true, []string{"semantic"}),
				reviseSnap,
			},
		}
		rules, rubyEnabled, kinds := borrowTranslateProtectRuby(snapshot)

		round, err := buildReviseRound(reviseSnap, nil, rules, rubyEnabled, kinds)
		if err != nil {
			t.Fatalf("buildReviseRound: %v", err)
		}
		if round.Mode != pipeline.RoundModeRevise {
			t.Fatalf("Mode = %q，want revise", round.Mode)
		}
		if len(round.ProtectRules) != 1 || round.ProtectRules[0] != "code" {
			t.Fatalf("ProtectRules = %v，want [code]", round.ProtectRules)
		}
		if !round.RubyEnabled {
			t.Fatal("RubyEnabled = false，want true")
		}
		if len(round.RubyPreserveKinds) != 1 || round.RubyPreserveKinds[0] != "semantic" {
			t.Fatalf("RubyPreserveKinds = %v，want [semantic]", round.RubyPreserveKinds)
		}
	})

	t.Run("无 translate 轮降级零值", func(t *testing.T) {
		snapshot := &service.JobExecutionSnapshot{
			Rounds: []service.JobRoundSnapshot{reviseSnap},
		}
		rules, rubyEnabled, kinds := borrowTranslateProtectRuby(snapshot)

		round, err := buildReviseRound(reviseSnap, nil, rules, rubyEnabled, kinds)
		if err != nil {
			t.Fatalf("buildReviseRound: %v", err)
		}
		if len(round.ProtectRules) != 0 || round.RubyEnabled || len(round.RubyPreserveKinds) != 0 {
			t.Fatalf("无 translate 轮时 Round 应为零值借用，实际 rules=%v rubyEnabled=%v kinds=%v",
				round.ProtectRules, round.RubyEnabled, round.RubyPreserveKinds)
		}
	})
}

// TestBuildReviseRound_SnapshotExplicitStrategyWins 修订预览的合成单轮快照显式
// 携带借用结果（裁剪前物化）时优先于工厂级借用，保证预览与真实 revise 轮一致。
func TestBuildReviseRound_SnapshotExplicitStrategyWins(t *testing.T) {
	reviseSnap := service.JobRoundSnapshot{
		Mode: "revise",
		Revise: &service.JobReviseRoundSnapshot{
			ProtectRules:      []string{"xml"},
			RubyEnabled:       true,
			RubyPreserveKinds: []string{"phonetic"},
		},
	}

	// 工厂级借用值与快照显式值不同：显式值必须胜出（即使借用为零值）。
	round, err := buildReviseRound(reviseSnap, nil, nil, false, nil)
	if err != nil {
		t.Fatalf("buildReviseRound: %v", err)
	}
	if len(round.ProtectRules) != 1 || round.ProtectRules[0] != "xml" {
		t.Fatalf("ProtectRules = %v，want [xml]", round.ProtectRules)
	}
	if !round.RubyEnabled || len(round.RubyPreserveKinds) != 1 || round.RubyPreserveKinds[0] != "phonetic" {
		t.Fatalf("ruby 字段未按快照显式值注入: enabled=%v kinds=%v", round.RubyEnabled, round.RubyPreserveKinds)
	}
}
