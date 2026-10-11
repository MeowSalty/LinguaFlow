package worker

import (
	"context"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func TestEngineFactoryFrozenRubyBatchVersions(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
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
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			handler := eng.Rounds()[0].Handler.(*pipeline.TranslateHandler)
			want := pipeline.AlignmentBatchConfig{BatchSize: 1}
			if version == 3 {
				want = pipeline.AlignmentBatchConfig{MaxWordsPerBatch: 90, ProtocolVersion: 1}
			}
			if handler.RubyBatch != want {
				t.Fatalf("v%d batch=%+v want=%+v", version, handler.RubyBatch, want)
			}
		})
	}
}
