package cli

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func TestCLIRubyBatchReachesBothContentHandlers(t *testing.T) {
	cfg := newTestCLIConfig()
	cfg.Execution.Rounds = append(cfg.Execution.Rounds, reviseRoundCfg())
	cfg.Execution.RubyRetry = &config.CLIConfigRubyRetry{Enabled: true, Backend: "test", MaxAttempts: 2, BatchSize: new(4), MaxWordsPerBatch: new(80), BatchWaitMS: new(0)}
	eng, resolved, err := buildEngineFromCLIConfig(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resolved.Close()
	defer eng.Close()
	want := pipeline.AlignmentBatchConfig{BatchSize: 4, MaxWordsPerBatch: 80, ProtocolVersion: 1}
	for _, round := range eng.Rounds() {
		var got pipeline.AlignmentBatchConfig
		switch h := round.Handler.(type) {
		case *pipeline.TranslateHandler:
			got = h.RubyBatch
		case *pipeline.ReviseHandler:
			got = h.RubyBatch
		default:
			t.Fatal("unexpected test handler")
		}
		if got != want {
			t.Fatalf("CLI batch=%+v want=%+v", got, want)
		}
	}
}
