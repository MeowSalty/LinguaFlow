package api

import (
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func testServerConfig() *config.ServerConfig { return config.DefaultServerConfig() }

func budgetLimit(value int64) *int64 { return &value }

// Explicit finite policy for budget regression scenarios, never a default.
func finiteTestExecutionBudget() config.ExecutionBudgetConfig {
	limits := func() config.ExecutionBudgetLimits {
		return config.ExecutionBudgetLimits{
			MaxUnconfirmedMembers: budgetLimit(4096), MaxPayloadBytes: budgetLimit(1 << 30),
			MaxPrepareOwners: budgetLimit(64), MaxMainResponseOwners: budgetLimit(64),
			MaxAuxResponseOwners: budgetLimit(64), MaxBuildWorkers: budgetLimit(64),
			MaxQueuedDescriptors: budgetLimit(4096), MaxLogicalStorageBytes: budgetLimit(1 << 30),
		}
	}
	return config.ExecutionBudgetConfig{Actor: limits(), Instance: limits()}
}

func testBudgetPolicy(t *testing.T, budget config.ExecutionBudgetConfig) resourcebudget.Policy {
	t.Helper()
	policy, err := budget.RuntimePolicy()
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestOptionalBudgetNewServerValidatesBeforeDependencies(t *testing.T) {
	for _, value := range []int64{0, -1} {
		cfg := testServerConfig()
		cfg.ExecutionBudget.Actor.MaxPayloadBytes = budgetLimit(value)
		if _, err := NewServer(cfg, nil, nil, nil, nil, config.ModeLocal, nil); err == nil || !strings.Contains(err.Error(), "server.execution_budget.actor.max_payload_bytes") {
			t.Fatalf("explicit %d bypassed budget validation: %v", value, err)
		}
	}
	cfg := testServerConfig()
	if _, err := NewServer(cfg, nil, nil, nil, nil, config.ModeLocal, nil); err == nil || !strings.Contains(err.Error(), "credential keys") {
		t.Fatalf("omitted budget did not reach independent credential validation: %v", err)
	}
}

func TestOptionalBudgetNewServerConstructsBothModes(t *testing.T) {
	for _, mode := range []string{config.ModeLocal, config.ModeServer} {
		t.Run(mode, func(t *testing.T) {
			server, _, actor := configurationConsumerServer(t, mode, nil)
			if server.executionBudget == nil || server.executionBudget.Snapshot(actor.ID).Instance != (resourcebudget.Vector{}) {
				t.Fatal("default construction did not retain a clean real budget")
			}
		})
	}
}

func testExecutionBudgets() []struct {
	name   string
	budget config.ExecutionBudgetConfig
} {
	return []struct {
		name   string
		budget config.ExecutionBudgetConfig
	}{
		{"finite", finiteTestExecutionBudget()},
		{"unlimited", config.ExecutionBudgetConfig{}},
		{"mixed", config.ExecutionBudgetConfig{
			Actor:    config.ExecutionBudgetLimits{MaxPayloadBytes: budgetLimit(1 << 30)},
			Instance: config.ExecutionBudgetLimits{MaxQueuedDescriptors: budgetLimit(4096)},
		}},
	}
}
