package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestRubySnapshotFactoryRejectsOldFormatsBeforeBackendBuild(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("spec%d", version), func(t *testing.T) {
			fixture := newEngineTelemetryFixture(t, 0, 0)
			snapshot := fixture.snapshot
			snapshot.SchemaVersion, snapshot.DefaultsVersion = version, version
			before, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var builds int
			fixture.factory.build = func(backend.Config) (backend.Backend, error) { builds++; return nil, nil }
			e, err := fixture.factory.BuildEngine(context.Background(), snapshot, engine.RuntimeResources{}, nil)
			if e != nil || !errors.Is(err, execution.ErrUnsupportedExecutionFormat) || builds != 0 {
				t.Fatalf("old format reached provider construction: engine=%v builds=%d err=%v", e, builds, err)
			}
			after, err := json.Marshal(snapshot)
			if err != nil || string(before) != string(after) {
				t.Fatal("old frozen history mutated during rejection")
			}
		})
	}
}
func TestRubySnapshotFactoryRejectsMissingConcurrencyBeforeBackendBuild(t *testing.T) {
	fixture := newEngineTelemetryFixture(t, 0, 0)
	snapshot := fixture.snapshot
	snapshot.SchemaVersion, snapshot.DefaultsVersion, snapshot.ResponseFormatVersion = 4, 4, 1
	snapshot.RubyRetry = &service.ExecutionPlanRubyRetrySnapshot{Enabled: true, Backend: snapshot.Rounds[0].Backend, MaxAttempts: 1}
	var builds int
	fixture.factory.build = func(backend.Config) (backend.Backend, error) { builds++; return nil, nil }
	if e, err := fixture.factory.BuildEngine(context.Background(), snapshot, engine.RuntimeResources{}, nil); err == nil || e != nil || builds != 0 {
		t.Fatalf("missing explicit A defaulted during restore: builds=%d err=%v", builds, err)
	}
}
