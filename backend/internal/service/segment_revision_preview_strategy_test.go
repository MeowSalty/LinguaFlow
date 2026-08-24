package service

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// TestRevisionRoundFromSnapshot_ClonesAndNarrows 修订预览的单轮快照（复刻已配置
// revise 轮与从 translate 轮合成两条路径）：策略位于快照顶层（snapshot.Strategy），
// 轮次裁剪后天然存活，revise 轮不再携带/物化任何策略字段。
func TestRevisionRoundFromSnapshot_ClonesAndNarrows(t *testing.T) {
	issues := []qa.QualityIssue{{Code: "calque", Message: "借译"}}

	t.Run("复刻已配置 revise 轮", func(t *testing.T) {
		snapshot := &JobExecutionSnapshot{
			Rounds: []JobRoundSnapshot{
				{Mode: "translate", Translate: &JobTranslateRoundSnapshot{}},
				{Mode: "revise", Revise: &JobReviseRoundSnapshot{BatchSize: 5, SegmentScope: "with_issue_codes", IssueCodes: []string{"calque"}}},
			},
		}
		round, synthesized, err := revisionRoundFromSnapshot(snapshot, issues)
		if err != nil || synthesized {
			t.Fatalf("round=%+v synthesized=%v err=%v", round, synthesized, err)
		}
		if len(round.Revise.IssueCodes) != 1 || round.Revise.IssueCodes[0] != "calque" {
			t.Fatalf("IssueCodes=%v want [calque]", round.Revise.IssueCodes)
		}
		// 原快照中的 revise 轮本身不得被改动（复刻而非原地修改）。
		orig := snapshot.Rounds[1].Revise
		if len(orig.IssueCodes) != 1 || orig.IssueCodes[0] != "calque" {
			t.Fatalf("原快照 revise 轮被修改: %+v", orig)
		}
	})

	t.Run("从 translate 轮合成", func(t *testing.T) {
		snapshot := &JobExecutionSnapshot{
			Rounds: []JobRoundSnapshot{{Mode: "translate", Translate: &JobTranslateRoundSnapshot{}}},
		}
		round, synthesized, err := revisionRoundFromSnapshot(snapshot, issues)
		if err != nil || !synthesized {
			t.Fatalf("round=%+v synthesized=%v err=%v", round, synthesized, err)
		}
		if round.Mode != "revise" || len(round.Revise.IssueCodes) != 1 {
			t.Fatalf("合成轮不符合预期: %+v", round)
		}
	})

	t.Run("无 revise 且无 translate 轮报错", func(t *testing.T) {
		snapshot := &JobExecutionSnapshot{
			Rounds: []JobRoundSnapshot{{Mode: "correct", Correct: &JobCorrectRoundSnapshot{}}},
		}
		if _, _, err := revisionRoundFromSnapshot(snapshot, issues); err != ErrRevisionNoBackend {
			t.Fatalf("err=%v want ErrRevisionNoBackend", err)
		}
	})
}

// TestPreviewReadPathsUseTopLevelStrategy 两个预览读取路径必须读顶层计划级策略：
// 即使轮次被裁剪为单 revise 轮（找不到任何 translate 轮），QA 与修复配置仍应来自
// snapshot.Strategy 而非落空为零值。
func TestPreviewReadPathsUseTopLevelStrategy(t *testing.T) {
	snapshot := &JobExecutionSnapshot{
		SourceLang: "en",
		TargetLang: "zh",
		Strategy: StrategySnapshot{
			QA: schema.ProfileQAConfig{
				Enabled:        true,
				LengthMethod:   "char_weight",
				LengthRatioMin: 0.5,
				LengthRatioMax: 2.0,
			},
			Repair: schema.ProfileRepairConfig{
				Enabled:              true,
				JSONStructural:       true,
				PlaceholderNormalize: true,
			},
		},
		Rounds: []JobRoundSnapshot{
			{Mode: "revise", Revise: &JobReviseRoundSnapshot{BatchSize: 5}},
		},
	}

	qaClaims := qaConfigFromSnapshot(snapshot, "epub")
	if !qaClaims.Enabled {
		t.Fatal("qaConfigFromSnapshot 未读到顶层 Strategy 的 QA 配置")
	}
	if qaClaims.LengthMethod != "char_weight" || qaClaims.LengthRatioMin != 0.5 || qaClaims.LengthRatioMax != 2.0 {
		t.Fatalf("QA 配置读取不完整: %+v", qaClaims)
	}
	if qaClaims.SourceLang != "en" || qaClaims.TargetLang != "zh" {
		t.Fatalf("语言回填错误: source=%s target=%s", qaClaims.SourceLang, qaClaims.TargetLang)
	}
	if qaClaims.Format != "epub" {
		t.Fatalf("Format=%s want epub", qaClaims.Format)
	}

	repairOpts := repairOptionsFromSnapshot(snapshot)
	if !repairOpts.JSONStructural || !repairOpts.PlaceholderNormalize {
		t.Fatalf("Repair 选项映射不完整: %+v", repairOpts)
	}
	if repairOpts.SchemaAliases || repairOpts.PromptUpgrade {
		t.Fatalf("未启用的 Repair 算子不应被打开: %+v", repairOpts)
	}
}

// TestPreviewReadPathsZeroValueStrategy 无策略快照（零值）时两个读取路径应安全降级：
// QA 未启用、无修复算子。
func TestPreviewReadPathsZeroValueStrategy(t *testing.T) {
	snapshot := &JobExecutionSnapshot{}
	if claims := qaConfigFromSnapshot(snapshot, ""); claims.Enabled {
		t.Fatalf("零值策略不应启用 QA: %+v", claims)
	}
	if opts := repairOptionsFromSnapshot(snapshot); opts != (repair.Options{}) {
		t.Fatalf("零值策略应得零值修复选项: %+v", opts)
	}
}
