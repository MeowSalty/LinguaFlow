package v015

import "github.com/MeowSalty/LinguaFlow/backend/internal/config"

// Only fixtures which actually construct the current execution Server need
// authorization. Legacy/maintenance deployment decoding intentionally does not.
func authorizeTestExecution(cfg *config.ServerConfig) {
	l := config.ExecutionBudgetLimits{MaxUnconfirmedMembers: 1024, MaxPayloadBytes: 1 << 28, MaxPrepareOwners: 32, MaxMainResponseOwners: 32, MaxAuxResponseOwners: 32, MaxBuildWorkers: 32, MaxQueuedDescriptors: 1024, MaxLogicalStorageBytes: 1 << 30}
	cfg.ExecutionBudget = config.ExecutionBudgetConfig{Actor: l, Instance: l}
}
