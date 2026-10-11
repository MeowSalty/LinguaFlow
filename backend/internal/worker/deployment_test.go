package worker

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestDeploymentPipelineAdmissionConsumers(t *testing.T) {
	dir := t.TempDir()
	inputs := config.ServerInputs{Mode: config.ModeLocal, WorkingDirectory: dir, UserConfigDir: dir, Environment: map[string]string{
		"LINGUAFLOW_PIPELINE_MAX_INFLIGHT_WEIGHT_MB": "2",
		"LINGUAFLOW_PIPELINE_MAX_INFLIGHT_RESOURCES": "3",
		"LINGUAFLOW_PIPELINE_RSS_LIMIT_MB":           "0",
		"LINGUAFLOW_PIPELINE_MAX_RESPONSE_MB":        "3",
		"LINGUAFLOW_PIPELINE_CANDIDATE_WINDOW_MB":    "7",
	}}
	resolved, err := config.ResolveServerConfig(inputs)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	limits, rss := PipelineRuntime(resolved.Config.Pipeline, logger)
	if rss != nil {
		t.Fatal("explicit RSS zero should disable the gate")
	}
	if limits.MaxResponseBytes != 3<<20 || limits.Candidates.Bytes != 7<<20 {
		t.Fatalf("response/candidate deployment limits lost: %+v", limits)
	}
	a := newAdmission(limits.MaxInflightWeight, limits.MaxInflightResources)
	if err := a.admit(2 << 20); err != nil {
		t.Fatal(err)
	}
	if err := a.admit(1); !errors.Is(err, errWeightBudget) {
		t.Fatalf("configured weight limit not applied: %v", err)
	}
	a.release(2 << 20)
	for i := 0; i < 3; i++ {
		if err := a.admit(1); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.admit(1); !errors.Is(err, errResourceCap) {
		t.Fatalf("configured resource limit not applied: %v", err)
	}
	inputs.Environment["LINGUAFLOW_PIPELINE_RSS_LIMIT_MB"] = "1"
	resolved, err = config.ResolveServerConfig(inputs)
	if err != nil {
		t.Fatal(err)
	}
	_, rss = PipelineRuntime(resolved.Config.Pipeline, logger)
	if rss == nil || rss.Allow() || !rss.Tripped() {
		t.Fatal("1 MiB limit must trip against actual Go process RSS")
	}
	inputs.Environment["LINGUAFLOW_PIPELINE_RSS_LIMIT_MB"] = "1048576"
	resolved, err = config.ResolveServerConfig(inputs)
	if err != nil {
		t.Fatal(err)
	}
	_, rss = PipelineRuntime(resolved.Config.Pipeline, logger)
	if rss == nil || !rss.Allow() {
		t.Fatal("large RSS limit unexpectedly blocks resource admission")
	}
}
