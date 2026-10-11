package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestRubySnapshotFactoryPreservesFrozenConcurrencyModel(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		t.Run(map[int]string{1: "legacy_round_shared", 2: "stage_separated", 3: "stage_separated_batch"}[version], func(t *testing.T) {
			legacy := version == 1
			fixture := newEngineTelemetryFixture(t, 0, 0)
			snapshot := fixture.snapshot
			snapshot.Strategy.Ruby.Enabled = true
			snapshot.Rounds[0].Translate.Concurrency = 1
			snapshot.RubyRetry = &service.ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: snapshot.Rounds[0].Backend, MaxAttempts: 1, Concurrency: 1}
			snapshot.SchemaVersion, snapshot.DefaultsVersion = version, version
			if version == 3 {
				snapshot.RubyRetry.BatchSize = new(4)
				snapshot.RubyRetry.MaxWordsPerBatch = new(0)
				snapshot.RubyRetry.BatchWaitMS = new(0)
			} else {
				snapshot.RubyBatchProtocolVersion = 0
				snapshot.RubyTemplates.BatchJSON, snapshot.RubyTemplates.BatchText = "", ""
			}
			wantProtocol := 2
			if legacy {
				snapshot.SchemaVersion, snapshot.DefaultsVersion = 1, 1
				snapshot.RubyProtocolVersion, snapshot.RubyValidatorVersion = 0, 0
				snapshot.RubyRetry.Concurrency = 0
				snapshot.RubyTemplates = execution.RubyTemplates{JSON: prompt.LegacyRubyAlignmentJSONTemplate, Text: prompt.LegacyRubyAlignmentTextTemplate}
				wantProtocol = 1
			}
			before, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			e, err := fixture.factory.BuildEngine(context.Background(), snapshot, engine.RuntimeResources{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			h := e.Rounds()[0].Handler.(*pipeline.TranslateHandler)
			if h.RubyProtocolVersion != wantProtocol || h.RubyTemplates.JSON != snapshot.RubyTemplates.JSON || h.RubyTemplates.Text != snapshot.RubyTemplates.Text {
				t.Fatal("factory replaced the frozen protocol or templates")
			}
			wantBatch := pipeline.AlignmentBatchConfig{BatchSize: 1}
			if version == 3 {
				wantBatch = pipeline.AlignmentBatchConfig{BatchSize: 4, ProtocolVersion: 1}
			}
			if h.RubyBatch != wantBatch {
				t.Fatalf("v%d batch=%+v want=%+v", version, h.RubyBatch, wantBatch)
			}
			runtime := e.Rounds()[0].Runtime
			main, _, err := runtime.Admission.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RoundIndex: 0, BackendID: snapshot.Rounds[0].Backend.ID, Stage: backend.RequestStageMain}, runtime.Gate)
			if err != nil || main == nil {
				t.Fatalf("main admission failed: %v", err)
			}
			defer main.Release()
			alignment, wait, err := runtime.Admission.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RoundIndex: 0, BackendID: snapshot.RubyRetry.Backend.ID, Stage: backend.RequestStageAlignment}, runtime.Gate)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				if alignment != nil || wait.Reason != "legacy_round_concurrency" {
					if alignment != nil {
						alignment.Release()
					}
					t.Fatal("legacy snapshot acquired a new independent alignment allowance")
				}
			} else {
				if alignment == nil {
					t.Fatal("v2 alignment was incorrectly charged to its full main allowance")
				}
				alignment.Release()
			}
			after, err := json.Marshal(snapshot)
			if err != nil || string(before) != string(after) {
				t.Fatal("factory re-resolved or mutated its frozen input")
			}
		})
	}
}

func TestRubySnapshotFactoryRejectsMissingConcurrencyBeforeBackendBuild(t *testing.T) {
	fixture := newEngineTelemetryFixture(t, 0, 0)
	snapshot := fixture.snapshot
	snapshot.RubyRetry = &service.ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: snapshot.Rounds[0].Backend, MaxAttempts: 1}
	snapshot.SchemaVersion, snapshot.DefaultsVersion = 2, 2
	var builds int
	fixture.factory.build = func(backend.Config) (backend.Backend, error) {
		builds++
		return nil, nil
	}
	if e, err := fixture.factory.BuildEngine(context.Background(), snapshot, engine.RuntimeResources{}, nil); err == nil || e != nil || builds != 0 {
		t.Fatalf("missing A was silently defaulted during restore: builds=%d err=%v", builds, err)
	}
}
