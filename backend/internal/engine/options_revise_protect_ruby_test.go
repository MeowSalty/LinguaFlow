package engine

import (
	"log/slog"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// buildReviseHandlerForTest 走 buildRoundConfigs → buildRevisePipelineRound 完整链路，
// 返回首个 revise 轮的 Handler。
func buildReviseHandlerForTest(t *testing.T, r Round) *pipeline.ReviseHandler {
	t.Helper()
	configs := buildRoundConfigs([]Round{r}, &Config{})
	if len(configs) != 1 || configs[0].Revise == nil {
		t.Fatalf("buildRoundConfigs 未产出 revise 轮配置：configs=%d", len(configs))
	}
	rc := configs[0]

	restorer := ruby.NewRestorer()
	retryBackends := []backend.Backend{&closeTrackingBackend{name: "ruby-retry"}}
	pipelineRound, err := buildRevisePipelineRound(
		rc, restorer, retryBackends,
		repair.Options{}, slog.Default(), progress.Nop{}, 2,
	)
	if err != nil {
		t.Fatalf("buildRevisePipelineRound: %v", err)
	}
	h, ok := pipelineRound.Handler.(*pipeline.ReviseHandler)
	if !ok {
		t.Fatalf("Handler 类型 = %T，want *pipeline.ReviseHandler", pipelineRound.Handler)
	}
	return h
}

// TestBuildRevisePipelineRound_ProtectRubyPropagation 验证 Round 顶层的
// ProtectRules/RubyEnabled/RubyPreserveKinds 经 RoundConfig 注入 ReviseHandler，
// 且 RubyRestorer/RubyRetry* 沿传递链到位；RubyMode 随 ResponseMode 推导。
func TestBuildRevisePipelineRound_ProtectRubyPropagation(t *testing.T) {
	tests := []struct {
		name         string
		responseMode string
		protectRules []string
		rubyEnabled  bool
		wantProtNil  bool
		wantRubyMode string
	}{
		{
			name:         "json 响应 + 规则与注音全开",
			responseMode: "json",
			protectRules: []string{"code", "link"},
			rubyEnabled:  true,
			wantProtNil:  false,
			wantRubyMode: prompt.RubyModeJSON,
		},
		{
			name:         "text 响应退化为 section",
			responseMode: "text",
			protectRules: []string{"placeholder"},
			rubyEnabled:  true,
			wantProtNil:  false,
			wantRubyMode: prompt.RubyModeSection,
		},
		{
			name:         "仅注音无规则：链内不接 extractor，Protector 为 nil",
			responseMode: "json",
			protectRules: nil,
			rubyEnabled:  true,
			wantProtNil:  true,
			wantRubyMode: prompt.RubyModeJSON,
		},
		{
			name:         "零值降级：无规则无注音",
			responseMode: "json",
			protectRules: nil,
			rubyEnabled:  false,
			wantProtNil:  true,
			wantRubyMode: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := buildReviseHandlerForTest(t, Round{
				Mode:              pipeline.RoundModeRevise,
				ResponseMode:      tc.responseMode,
				ProtectRules:      tc.protectRules,
				RubyEnabled:       tc.rubyEnabled,
				RubyPreserveKinds: []string{"phonetic", "semantic"},
			})

			if tc.wantProtNil && h.Protector != nil {
				t.Fatalf("Protector 应为 nil（零值/不接 extractor），实际非 nil")
			}
			if !tc.wantProtNil && h.Protector == nil {
				t.Fatalf("Protector 不应为 nil")
			}
			if h.RubyEnabled != tc.rubyEnabled {
				t.Fatalf("RubyEnabled = %v，want %v", h.RubyEnabled, tc.rubyEnabled)
			}
			if h.RubyMode != tc.wantRubyMode {
				t.Fatalf("RubyMode = %q，want %q", h.RubyMode, tc.wantRubyMode)
			}
			if want := []string{"phonetic", "semantic"}; !reflect.DeepEqual(h.RubyPreserveKinds, want) {
				t.Fatalf("RubyPreserveKinds = %v，want %v", h.RubyPreserveKinds, want)
			}
		})
	}
}

// TestBuildRevisePipelineRound_RubyRetryInjection 验证 RubyRestorer 与定向重试参数
// 沿 buildPipelineRounds 同一条传递链注入 ReviseHandler。
func TestBuildRevisePipelineRound_RubyRetryInjection(t *testing.T) {
	configs := buildRoundConfigs([]Round{{
		Mode:              pipeline.RoundModeRevise,
		RubyEnabled:       true,
		IssueCodes:        []string{"qa.semantic.mistranslation"},
		MaxBatchIndexSpan: 3,
	}}, &Config{})

	restorer := ruby.NewRestorer()
	retryBackends := []backend.Backend{&closeTrackingBackend{name: "ruby-retry"}}
	pipelineRound, err := buildRevisePipelineRound(
		configs[0], restorer, retryBackends,
		repair.Options{}, slog.Default(), progress.Nop{}, 3,
	)
	if err != nil {
		t.Fatalf("buildRevisePipelineRound: %v", err)
	}
	h := pipelineRound.Handler.(*pipeline.ReviseHandler)

	if h.RubyRestorer != restorer {
		t.Fatal("RubyRestorer 未按同一实例注入")
	}
	if len(h.RubyRetryBackends) != 1 {
		t.Fatalf("RubyRetryBackends = %v，want 单元素", h.RubyRetryBackends)
	}
	if h.RubyRetryAttempts != 3 {
		t.Fatalf("RubyRetryAttempts = %d，want 3", h.RubyRetryAttempts)
	}
	if !reflect.DeepEqual(h.IssueCodes, []string{"qa.semantic.mistranslation"}) {
		t.Fatalf("IssueCodes 被破坏 = %v", h.IssueCodes)
	}
	if h.MaxBatchIndexSpan != 3 {
		t.Fatalf("MaxBatchIndexSpan = %d，want 3", h.MaxBatchIndexSpan)
	}
}
