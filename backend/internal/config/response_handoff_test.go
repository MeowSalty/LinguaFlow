package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestResponseHandoffDeploymentPresenceAndArithmetic(t *testing.T) {
	input := deploymentInputs(t)
	resolved, err := ResolveServerConfig(input)
	if err != nil || resolved.Config.Pipeline.ResponseHandoffMB != 64 {
		t.Fatalf("missing default: %+v %v", resolved, err)
	}
	input.Environment["LINGUAFLOW_PIPELINE_RESPONSE_HANDOFF_MB"] = "32"
	resolved, err = ResolveServerConfig(input)
	if err != nil || resolved.Config.Pipeline.ResponseHandoffMB != 32 {
		t.Fatalf("environment ignored: %v", err)
	}
	data, err := EncodeServerDocument(resolved.Config, resolved.Log)
	if err != nil || !strings.Contains(string(data), "response_handoff_mb: 32") {
		t.Fatalf("field omitted from saved deployment: %v", err)
	}
	for _, value := range []string{"0", "-1", fmt.Sprint(int64(1<<63-1)/(1<<20) + 1)} {
		input = deploymentInputs(t)
		input.Environment["LINGUAFLOW_PIPELINE_RESPONSE_HANDOFF_MB"] = value
		if _, err := ResolveServerConfig(input); err == nil {
			t.Fatalf("invalid handoff accepted: %s", value)
		}
	}
}
