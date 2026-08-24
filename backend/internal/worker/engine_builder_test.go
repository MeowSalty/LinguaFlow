package worker

import (
	"context"
	"log/slog"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/preview"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
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

// TestBuildEngineConfigReadsTopLevelStrategy 验证引擎配置直接读快照顶层
// Strategy（计划级物化），不依赖快照中是否存在 translate 轮。
func TestBuildEngineConfigReadsTopLevelStrategy(t *testing.T) {
	snapshot := &service.JobExecutionSnapshot{
		SourceLang:      "en",
		TargetLang:      "zh",
		GlossaryEnabled: true,
		TMEnabled:       true,
		Strategy: service.StrategySnapshot{
			Repair: schema.ProfileRepairConfig{Enabled: true, JSONStructural: true},
			Ruby:   schema.ProfileRubyConfig{Enabled: true, PreserveKinds: []string{"phonetic"}},
			Glossary: schema.ProfileGlossaryConfig{Bootstrap: schema.ProfileBootstrapConfig{
				Enabled:                true,
				MaxTermsPer1000Chars:   5.0,
				MinSourceLen:           2,
				InlineConflictStrategy: "rewrite-local",
			}},
			QA: schema.ProfileQAConfig{Enabled: true, AutoReject: true, LengthMethod: "ratio"},
		},
		// 快照不含 translate 轮：旧实现扫描第一条 translate 轮会落空，新实现读顶层。
		Rounds: []service.JobRoundSnapshot{
			{Mode: "extract", Extract: &service.JobExtractRoundSnapshot{}},
			{Mode: "revise", Revise: &service.JobReviseRoundSnapshot{}},
		},
	}

	cfg := BuildEngineConfig(snapshot)
	if !cfg.Repair.JSONStructural {
		t.Error("cfg.Repair 应来自顶层 Strategy 的 repair 配置")
	}
	if !cfg.Ruby.Enabled || len(cfg.Ruby.PreserveKinds) != 1 || cfg.Ruby.PreserveKinds[0] != "phonetic" {
		t.Errorf("cfg.Ruby = %+v，want enabled + [phonetic]", cfg.Ruby)
	}
	if !cfg.Glossary.Bootstrap.Enabled || cfg.Glossary.Bootstrap.MaxTermsPer1000Chars != 5.0 ||
		cfg.Glossary.Bootstrap.MinSourceLen != 2 || cfg.Glossary.Bootstrap.InlineConflictStrategy != "rewrite-local" {
		t.Errorf("cfg.Glossary.Bootstrap = %+v，未按顶层 Strategy 注入", cfg.Glossary.Bootstrap)
	}
	if !cfg.QA.Enabled || !cfg.QA.AutoReject || string(cfg.QA.LengthMethod) != "ratio" {
		t.Errorf("cfg.QA = %+v，未按顶层 Strategy 注入", cfg.QA)
	}
	if cfg.QA.SourceLang != "en" || cfg.QA.TargetLang != "zh" {
		t.Errorf("QA 语言应取快照级字段，实际 %q/%q", cfg.QA.SourceLang, cfg.QA.TargetLang)
	}
	if !cfg.TMEnabled {
		t.Error("TMEnabled 应取快照级字段")
	}
}

// TestBuildEngineConfigZeroValueStrategy 零值策略安全降级：Repair/Ruby/QA 全零值
// （= 不修复、不注音、不质检）。
func TestBuildEngineConfigZeroValueStrategy(t *testing.T) {
	snapshot := &service.JobExecutionSnapshot{
		Rounds: []service.JobRoundSnapshot{{Mode: "revise", Revise: &service.JobReviseRoundSnapshot{}}},
	}

	cfg := BuildEngineConfig(snapshot)
	if cfg.Repair != (repair.Options{}) {
		t.Errorf("零值策略时 Repair 应为零值，实际 %+v", cfg.Repair)
	}
	if cfg.Ruby.Enabled || len(cfg.Ruby.PreserveKinds) != 0 {
		t.Errorf("零值策略时 Ruby 应关闭，实际 %+v", cfg.Ruby)
	}
	if cfg.QA.Enabled {
		t.Error("零值策略时 QA 应关闭")
	}
}

// testStrategy 构造计划级策略快照（protect/ruby/postprocess/context 可选注入）。
func testStrategy(protectEnabled bool, protectRules []string, rubyEnabled bool, rubyKinds []string) service.StrategySnapshot {
	return service.StrategySnapshot{
		Protect:     schema.ProfileProtectConfig{Enabled: protectEnabled, Rules: protectRules},
		Postprocess: schema.ProfilePostprocessConfig{Enabled: true, TrimSpaces: true},
		Context:     schema.ProfileContextConfig{Enabled: true, Before: 2, After: 1, MaxChars: 100},
		Ruby:        schema.ProfileRubyConfig{Enabled: rubyEnabled, PreserveKinds: rubyKinds},
	}
}

// TestBuildTranslateAndReviseRoundsUseTopLevelStrategy 验证 translate 与 revise
// 双轮的 protect/ruby 均从传入的计划级 Strategy 取得（postprocess/context 同源）。
func TestBuildTranslateAndReviseRoundsUseTopLevelStrategy(t *testing.T) {
	strategy := testStrategy(true, []string{"code", "link"}, true, []string{"semantic"})
	translateSnap := service.JobRoundSnapshot{
		Mode:      "translate",
		Translate: &service.JobTranslateRoundSnapshot{Prompt: service.PromptSnapshot{Content: "{{ .SourceText }}"}},
	}
	reviseSnap := service.JobRoundSnapshot{
		Mode:   "revise",
		Revise: &service.JobReviseRoundSnapshot{},
	}

	tr, err := buildTranslateRound(translateSnap, strategy, nil)
	if err != nil {
		t.Fatalf("buildTranslateRound: %v", err)
	}
	if len(tr.ProtectRules) != 2 || tr.ProtectRules[0] != "code" || tr.ProtectRules[1] != "link" {
		t.Fatalf("translate ProtectRules = %v，want [code link]", tr.ProtectRules)
	}
	if !tr.RubyEnabled || len(tr.RubyPreserveKinds) != 1 || tr.RubyPreserveKinds[0] != "semantic" {
		t.Fatalf("translate ruby 未按顶层 Strategy 注入: enabled=%v kinds=%v", tr.RubyEnabled, tr.RubyPreserveKinds)
	}
	if tr.Postprocess == nil || !tr.Postprocess.TrimSpaces {
		t.Fatal("translate Postprocess 应按顶层 Strategy 启用 trim_spaces")
	}
	if tr.Context == nil || !tr.Context.Enabled || tr.Context.Before != 2 || tr.Context.After != 1 || tr.Context.MaxChars != 100 {
		t.Fatalf("translate Context = %+v，未按顶层 Strategy 注入", tr.Context)
	}

	rr, err := buildReviseRound(reviseSnap, strategy, nil)
	if err != nil {
		t.Fatalf("buildReviseRound: %v", err)
	}
	if rr.Mode != pipeline.RoundModeRevise {
		t.Fatalf("Mode = %q，want revise", rr.Mode)
	}
	if len(rr.ProtectRules) != 2 || rr.ProtectRules[0] != "code" || rr.ProtectRules[1] != "link" {
		t.Fatalf("revise ProtectRules = %v，want [code link]", rr.ProtectRules)
	}
	if !rr.RubyEnabled || len(rr.RubyPreserveKinds) != 1 || rr.RubyPreserveKinds[0] != "semantic" {
		t.Fatalf("revise ruby 未按顶层 Strategy 注入: enabled=%v kinds=%v", rr.RubyEnabled, rr.RubyPreserveKinds)
	}
}

// TestBuildRoundsZeroValueStrategyDegrades 策略 protect.enabled=false 时零值降级：
// 即使配置了 Rules 也视为未启用（原文直发语义不变）；ruby 关闭同理。
func TestBuildRoundsZeroValueStrategyDegrades(t *testing.T) {
	strategy := testStrategy(false, []string{"link"}, false, nil)
	translateSnap := service.JobRoundSnapshot{
		Mode:      "translate",
		Translate: &service.JobTranslateRoundSnapshot{Prompt: service.PromptSnapshot{Content: "{{ .SourceText }}"}},
	}
	reviseSnap := service.JobRoundSnapshot{
		Mode:   "revise",
		Revise: &service.JobReviseRoundSnapshot{},
	}

	tr, err := buildTranslateRound(translateSnap, strategy, nil)
	if err != nil {
		t.Fatalf("buildTranslateRound: %v", err)
	}
	if len(tr.ProtectRules) != 0 {
		t.Fatalf("Protect 未启用时应忽略 Rules，实际 %v", tr.ProtectRules)
	}
	if tr.RubyEnabled {
		t.Fatal("ruby 未启用时 translate 轮不应开启注音")
	}

	rr, err := buildReviseRound(reviseSnap, strategy, nil)
	if err != nil {
		t.Fatalf("buildReviseRound: %v", err)
	}
	if len(rr.ProtectRules) != 0 || rr.RubyEnabled || len(rr.RubyPreserveKinds) != 0 {
		t.Fatalf("零值策略时 revise 轮应为零值降级，实际 rules=%v ruby=%v kinds=%v",
			rr.ProtectRules, rr.RubyEnabled, rr.RubyPreserveKinds)
	}
}

// TestFactoryWiresTopLevelStrategyToBothRounds 端到端：经 EngineFactory 构建
// translate+revise 双轮引擎，两轮 Handler 均从顶层 Strategy 取得 protect/ruby。
func TestFactoryWiresTopLevelStrategyToBothRounds(t *testing.T) {
	ensureFakeBackend(t)
	fakeBackendSnapshot := func(mode string) service.JobRoundSnapshot {
		return service.JobRoundSnapshot{
			Mode: mode,
			Backend: service.BackendSnapshot{
				ID:      1,
				Name:    "fake",
				Type:    fakeBackendType,
				Options: map[string]any{},
			},
		}
	}
	snapshot := &service.JobExecutionSnapshot{
		SourceLang: "en",
		TargetLang: "zh",
		Strategy:   testStrategy(true, []string{"code"}, true, []string{"semantic"}),
		Rounds: []service.JobRoundSnapshot{
			func() service.JobRoundSnapshot {
				rs := fakeBackendSnapshot("translate")
				rs.Translate = &service.JobTranslateRoundSnapshot{
					Prompt:      service.PromptSnapshot{Content: templates.EmbeddedPromptTemplate()},
					BatchSize:   10,
					Concurrency: 1,
				}
				return rs
			}(),
			func() service.JobRoundSnapshot {
				rs := fakeBackendSnapshot("revise")
				rs.Revise = &service.JobReviseRoundSnapshot{}
				return rs
			}(),
		},
	}

	factory := NewEngineFactory(slog.Default(), nil)
	eng, err := factory.BuildEngine(context.Background(), snapshot, engine.RuntimeResources{}, preview.NewMemoryCollector())
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	defer func() { _ = eng.Close() }()

	rounds := eng.Rounds()
	if len(rounds) != 2 {
		t.Fatalf("rounds = %d，want 2", len(rounds))
	}
	th, ok := rounds[0].Handler.(*pipeline.TranslateHandler)
	if !ok {
		t.Fatalf("round[0] handler = %T，want *pipeline.TranslateHandler", rounds[0].Handler)
	}
	if th.Protector == nil {
		t.Error("translate 轮 Protector 未从顶层 Strategy 注入")
	}
	if !th.RubyEnabled || len(th.RubyPreserveKinds) != 1 || th.RubyPreserveKinds[0] != "semantic" {
		t.Errorf("translate 轮 ruby = %v/%v，want true/[semantic]", th.RubyEnabled, th.RubyPreserveKinds)
	}
	rh, ok := rounds[1].Handler.(*pipeline.ReviseHandler)
	if !ok {
		t.Fatalf("round[1] handler = %T，want *pipeline.ReviseHandler", rounds[1].Handler)
	}
	if rh.Protector == nil {
		t.Error("revise 轮 Protector 未从顶层 Strategy 注入")
	}
	if !rh.RubyEnabled || len(rh.RubyPreserveKinds) != 1 || rh.RubyPreserveKinds[0] != "semantic" {
		t.Errorf("revise 轮 ruby = %v/%v，want true/[semantic]", rh.RubyEnabled, rh.RubyPreserveKinds)
	}
}
