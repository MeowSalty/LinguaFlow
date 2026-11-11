package api

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

// Deliberately generous isolated test policy, never a deployment default.
func testServerConfig() *config.ServerConfig {
	cfg := config.DefaultServerConfig()
	limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 64, MaxMainResponseOwners: 64, MaxAuxResponseOwners: 64, MaxBuildWorkers: 64, MaxQueuedDescriptors: 4096, MaxLogicalStorageBytes: 1 << 30}
	cfg.ExecutionBudget = resourcebudget.Policy{Actor: limits, Instance: limits}
	return cfg
}
