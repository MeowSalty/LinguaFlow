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
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "stage_separated", true: "legacy_round_shared"}[legacy], func(t *testing.T) {
			fixture := newEngineTelemetryFixture(t, 0, 0)
			snapshot := fixture.snapshot
			snapshot.Strategy.Ruby.Enabled = true
			snapshot.Rounds[0].Translate.Concurrency = 1
			snapshot.RubyRetry = &service.ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: snapshot.Rounds[0].Backend, MaxAttempts: 1, Concurrency: 1}
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
	var builds int
	fixture.factory.build = func(backend.Config) (backend.Backend, error) {
		builds++
		return nil, nil
	}
	if e, err := fixture.factory.BuildEngine(context.Background(), snapshot, engine.RuntimeResources{}, nil); err == nil || e != nil || builds != 0 {
		t.Fatalf("missing A was silently defaulted during restore: builds=%d err=%v", builds, err)
	}
}
