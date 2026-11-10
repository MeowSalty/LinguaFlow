package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func TestEngineFactoryFrozenRubyBatchVersions(t *testing.T) {
	for _, version := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			fixture := newEngineTelemetryFixture(t, 0, 0)
			snapshot := *fixture.snapshot
			snapshot.Strategy.Ruby.Enabled = true
			snapshot.RubyRetry = &execution.ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: snapshot.Rounds[0].Backend, MaxAttempts: 2, BatchSize: new(0), MaxWordsPerBatch: new(90), BatchWaitMS: new(0)}
			resolved, err := execution.Resolve(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			resolved.SchemaVersion, resolved.DefaultsVersion = version, version
			eng, err := fixture.factory.BuildEngine(context.Background(), resolved, engine.RuntimeResources{}, nil)
			if version < 4 {
				if eng != nil {
					_ = eng.Close()
					t.Fatal("historical snapshot produced an executable engine")
				}
				if !errors.Is(err, execution.ErrUnsupportedExecutionFormat) {
					t.Fatalf("v%d error=%v, want unsupported execution format", version, err)
				}
				if fixture.calls.Load() != 0 {
					t.Fatal("historical snapshot reached HTTP")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			handler := eng.Rounds()[0].Handler.(*pipeline.TranslateHandler)
			want := pipeline.AlignmentBatchConfig{MaxWordsPerBatch: 90, ProtocolVersion: 1}
			if handler.RubyBatch != want {
				t.Fatalf("v%d batch=%+v want=%+v", version, handler.RubyBatch, want)
			}
		})
	}
}
