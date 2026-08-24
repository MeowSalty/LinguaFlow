package cli

import (
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// newTestCLIConfig 构造最小可用的 CLIConfig：一个 openai 后端 + 内联翻译提示词，
// execution.rounds 为空由各用例自行填充。
func newTestCLIConfig() *config.CLIConfig {
	return &config.CLIConfig{
		Version:    1,
		SourceLang: "en",
		TargetLang: "zh",
		Backends: map[string]config.CLIConfigBackend{
			"test": {
				Type:    "openai",
				Enabled: true,
				Options: map[string]any{
					"api_key":  "sk-test",
					"base_url": "https://example.invalid/v1",
					"model":    "test-model",
				},
			},
		},
		PromptTemplates: map[string]config.CLIConfigPromptTemplate{
			"default": {Content: "translate: {{ .SourceText }}"},
		},
		BootstrapPromptTemplates: map[string]config.CLIConfigBootstrapTemplate{},
		TranslationProfiles:      map[string]config.CLIConfigTranslationProfile{},
		Glossary:                 config.CLIConfigGlossary{Path: "./glossary.csv", Save: true},
	}
}

func translateRoundCfg() config.CLIConfigRound {
	return config.CLIConfigRound{
		Mode:      "translate",
		Backend:   "test",
		Translate: &config.CLIConfigTranslateRound{Prompt: "default", BatchSize: 1, Concurrency: 1, FallbackShrink: 0.5},
	}
}

// TestBuildEngineFromCLIConfig_TopLevelProfile 验证计划级策略从 execution.profile
// 按名解析，translate 轮的 protect/ruby/postprocess/context 均取自该唯一策略。
func TestBuildEngineFromCLIConfig_TopLevelProfile(t *testing.T) {
	cfg := newTestCLIConfig()
	cfg.Execution.Profile = "strict"
	cfg.TranslationProfiles["strict"] = config.CLIConfigTranslationProfile{
		Protect:     config.ProtectConfig{Enabled: true, Rules: []string{"code", "xml"}},
		Ruby:        config.RubyConfig{Enabled: true, PreserveKinds: []string{"semantic"}},
		Postprocess: config.PostprocessConfig{Enabled: true, TrimSpaces: true},
		Context:     config.ContextConfig{Enabled: true, Before: 2, After: 1, MaxChars: 80},
		Repair:      config.RepairConfig{Enabled: true},
	}
	cfg.Execution.Rounds = []config.CLIConfigRound{translateRoundCfg()}

	opts, err := buildEngineFromCLIConfig(cfg)
	if err != nil {
		t.Fatalf("buildEngineFromCLIConfig: %v", err)
	}
	if !opts.Config.Ruby.Enabled || len(opts.Config.Ruby.PreserveKinds) != 1 || opts.Config.Ruby.PreserveKinds[0] != "semantic" {
		t.Errorf("引擎级 Ruby 未按命名策略注入: %+v", opts.Config.Ruby)
	}
	if len(opts.Rounds) != 1 {
		t.Fatalf("rounds = %d，want 1", len(opts.Rounds))
	}
	r := opts.Rounds[0]
	if len(r.ProtectRules) != 2 || r.ProtectRules[0] != "code" || r.ProtectRules[1] != "xml" {
		t.Errorf("ProtectRules = %v，want [code xml]", r.ProtectRules)
	}
	if !r.RubyEnabled || len(r.RubyPreserveKinds) != 1 || r.RubyPreserveKinds[0] != "semantic" {
		t.Errorf("ruby 未按命名策略注入: enabled=%v kinds=%v", r.RubyEnabled, r.RubyPreserveKinds)
	}
	if r.Postprocess == nil || !r.Postprocess.TrimSpaces {
		t.Error("Postprocess 应按命名策略启用 trim_spaces")
	}
	if r.Context == nil || r.Context.Before != 2 || r.Context.MaxChars != 80 {
		t.Errorf("Context = %+v，未按命名策略注入", r.Context)
	}
	if r.Repair == nil {
		t.Error("Repair 应按命名策略注入")
	}
}

// TestBuildEngineFromCLIConfig_DefaultProfileFallback 验证 execution.profile 缺省时
// 回退内置默认策略（protect 规则与 ruby 开关来自 templates 包嵌入资源）。
func TestBuildEngineFromCLIConfig_DefaultProfileFallback(t *testing.T) {
	cfg := newTestCLIConfig()
	cfg.Execution.Rounds = []config.CLIConfigRound{translateRoundCfg()}

	opts, err := buildEngineFromCLIConfig(cfg)
	if err != nil {
		t.Fatalf("buildEngineFromCLIConfig: %v", err)
	}
	builtin := config.BuiltinExecutionProfile()
	r := opts.Rounds[0]
	if len(r.ProtectRules) != len(builtin.Protect.Rules) {
		t.Errorf("ProtectRules = %v，want 内置默认 %v", r.ProtectRules, builtin.Protect.Rules)
	}
	if r.RubyEnabled != builtin.Ruby.Enabled {
		t.Errorf("RubyEnabled = %v，want 内置默认 %v", r.RubyEnabled, builtin.Ruby.Enabled)
	}
}

// TestBuildEngineFromCLIConfig_ReviseRoundWiresProtectRuby 验证 revise 轮与
// translate 轮统一从解析出的唯一策略接入 protect/ruby（修复 revise 轮裸奔缺口）。
func TestBuildEngineFromCLIConfig_ReviseRoundWiresProtectRuby(t *testing.T) {
	cfg := newTestCLIConfig()
	cfg.Execution.Profile = "strict"
	cfg.TranslationProfiles["strict"] = config.CLIConfigTranslationProfile{
		Protect: config.ProtectConfig{Enabled: true, Rules: []string{"link"}},
		Ruby:    config.RubyConfig{Enabled: true, PreserveKinds: []string{"creative"}},
	}
	cfg.Execution.Rounds = []config.CLIConfigRound{
		translateRoundCfg(),
		{
			Mode:    "revise",
			Backend: "test",
			Revise:  &config.CLIConfigReviseRound{BatchSize: 10, Concurrency: 1},
		},
	}

	opts, err := buildEngineFromCLIConfig(cfg)
	if err != nil {
		t.Fatalf("buildEngineFromCLIConfig: %v", err)
	}
	if len(opts.Rounds) != 2 {
		t.Fatalf("rounds = %d，want 2", len(opts.Rounds))
	}
	tr, rv := opts.Rounds[0], opts.Rounds[1]
	if tr.Mode != pipeline.RoundModeTranslate || rv.Mode != pipeline.RoundModeRevise {
		t.Fatalf("modes = %q/%q，want translate/revise", tr.Mode, rv.Mode)
	}
	// protect/ruby 两轮同源。
	if !reflect.DeepEqual(tr.ProtectRules, rv.ProtectRules) {
		t.Errorf("protect 不同源: translate=%v revise=%v", tr.ProtectRules, rv.ProtectRules)
	}
	if tr.RubyEnabled != rv.RubyEnabled || !reflect.DeepEqual(tr.RubyPreserveKinds, rv.RubyPreserveKinds) {
		t.Errorf("ruby 不同源: translate=%v/%v revise=%v/%v",
			tr.RubyEnabled, tr.RubyPreserveKinds, rv.RubyEnabled, rv.RubyPreserveKinds)
	}
	if len(rv.ProtectRules) != 1 || rv.ProtectRules[0] != "link" {
		t.Errorf("revise ProtectRules = %v，want [link]", rv.ProtectRules)
	}
	if !rv.RubyEnabled || len(rv.RubyPreserveKinds) != 1 || rv.RubyPreserveKinds[0] != "creative" {
		t.Errorf("revise ruby 未按策略注入: enabled=%v kinds=%v", rv.RubyEnabled, rv.RubyPreserveKinds)
	}
	// with_issues（默认）物化为完整语义白名单；渲染器必须就绪。
	if rv.ReviseRenderer == nil {
		t.Error("revise 轮缺少渲染器")
	}
	if !reflect.DeepEqual(rv.IssueCodes, qa.SemanticQACodes()) {
		t.Errorf("revise IssueCodes 应物化为完整语义白名单，实际 %d 个", len(rv.IssueCodes))
	}
}

// TestBuildEngineFromCLIConfig_ReviseExplicitIssueCodes 验证 with_issue_codes 作用域
// 校验并透传用户指定 codes。
func TestBuildEngineFromCLIConfig_ReviseExplicitIssueCodes(t *testing.T) {
	cfg := newTestCLIConfig()
	validCode := qa.SemanticQACodes()[0]
	cfg.Execution.Rounds = []config.CLIConfigRound{
		translateRoundCfg(),
		{
			Mode:    "revise",
			Backend: "test",
			Revise: &config.CLIConfigReviseRound{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
				IssueCodes:   []string{validCode},
			},
		},
	}

	opts, err := buildEngineFromCLIConfig(cfg)
	if err != nil {
		t.Fatalf("buildEngineFromCLIConfig: %v", err)
	}
	rv := opts.Rounds[1]
	if !reflect.DeepEqual(rv.IssueCodes, []string{validCode}) {
		t.Errorf("IssueCodes = %v，want [%s]", rv.IssueCodes, validCode)
	}

	// 无效 code 报错而非静默透传。
	cfg.Execution.Rounds[1].Revise.IssueCodes = []string{"not_a_code"}
	if _, err := buildEngineFromCLIConfig(cfg); err == nil {
		t.Fatal("expected error for invalid issue code")
	}
}

// TestBuildEngineFromCLIConfig_DisabledProtectDegrades 验证策略关闭 protect/ruby 时
// 两轮均零值降级（原文直发）。
func TestBuildEngineFromCLIConfig_DisabledProtectDegrades(t *testing.T) {
	cfg := newTestCLIConfig()
	cfg.Execution.Profile = "bare"
	cfg.TranslationProfiles["bare"] = config.CLIConfigTranslationProfile{
		Protect: config.ProtectConfig{Enabled: false, Rules: []string{"code"}},
		Ruby:    config.RubyConfig{Enabled: false},
	}
	cfg.Execution.Rounds = []config.CLIConfigRound{
		translateRoundCfg(),
		{Mode: "revise", Backend: "test", Revise: &config.CLIConfigReviseRound{BatchSize: 10, Concurrency: 1}},
	}

	opts, err := buildEngineFromCLIConfig(cfg)
	if err != nil {
		t.Fatalf("buildEngineFromCLIConfig: %v", err)
	}
	for i, r := range opts.Rounds {
		if len(r.ProtectRules) != 0 {
			t.Errorf("round[%d] ProtectRules = %v，protect 未启用时应为零值", i, r.ProtectRules)
		}
		if r.RubyEnabled {
			t.Errorf("round[%d] RubyEnabled = true，策略已关闭注音", i)
		}
	}
}

// TestApplyTranslateFlags_ProfileOverridesTopLevel 验证 --profile flag 覆盖
// execution.profile，且未知名称报错。
func TestApplyTranslateFlags_ProfileOverridesTopLevel(t *testing.T) {
	cfg := newTestCLIConfig()
	cfg.Execution.Profile = "a"
	cfg.TranslationProfiles["a"] = config.CLIConfigTranslationProfile{}
	cfg.TranslationProfiles["b"] = config.CLIConfigTranslationProfile{}
	cfg.Execution.Rounds = []config.CLIConfigRound{translateRoundCfg()}

	if err := applyTranslateFlags(cfg, translateOptions{profile: "b"}); err != nil {
		t.Fatalf("applyTranslateFlags: %v", err)
	}
	if cfg.Execution.Profile != "b" {
		t.Errorf("execution.profile = %q，want b", cfg.Execution.Profile)
	}

	err := applyTranslateFlags(cfg, translateOptions{profile: "nope"})
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
}
