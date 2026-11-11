package cli

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"testing"
)

func testExecutionBudget() resourcebudget.Policy {
	l := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 64, MaxMainResponseOwners: 64, MaxAuxResponseOwners: 64, MaxBuildWorkers: 64, MaxQueuedDescriptors: 4096, MaxLogicalStorageBytes: 1 << 30}
	return resourcebudget.Policy{Actor: l, Instance: l}
}
func testServerConfig() *config.ServerConfig {
	cfg := config.DefaultServerConfig()
	cfg.ExecutionBudget = testExecutionBudget()
	return cfg
}
func executionBudgetTestEnvironment(t *testing.T) {
	t.Helper()
	for _, group := range []string{"ACTOR", "INSTANCE"} {
		for _, dim := range []string{"UNCONFIRMED_MEMBERS", "PREPARE_OWNERS", "MAIN_RESPONSE_OWNERS", "AUX_RESPONSE_OWNERS", "BUILD_WORKERS", "QUEUED_DESCRIPTORS"} {
			t.Setenv("LINGUAFLOW_EXECUTION_BUDGET_"+group+"_MAX_"+dim, "4096")
		}
		for _, dim := range []string{"PAYLOAD_BYTES", "LOGICAL_STORAGE_BYTES"} {
			t.Setenv("LINGUAFLOW_EXECUTION_BUDGET_"+group+"_MAX_"+dim, "1073741824")
		}
	}
}
func resolveTestServerConfig(in config.ServerInputs) (*config.ResolvedServer, error) {
	if in.Environment == nil {
		in.Environment = map[string]string{}
	}
	for _, group := range []string{"ACTOR", "INSTANCE"} {
		for _, dim := range []string{"UNCONFIRMED_MEMBERS", "PREPARE_OWNERS", "MAIN_RESPONSE_OWNERS", "AUX_RESPONSE_OWNERS", "BUILD_WORKERS", "QUEUED_DESCRIPTORS"} {
			in.Environment["LINGUAFLOW_EXECUTION_BUDGET_"+group+"_MAX_"+dim] = "4096"
		}
		for _, dim := range []string{"PAYLOAD_BYTES", "LOGICAL_STORAGE_BYTES"} {
			in.Environment["LINGUAFLOW_EXECUTION_BUDGET_"+group+"_MAX_"+dim] = "1073741824"
		}
	}
	return config.ResolveServerConfig(in)
}
