package config

import (
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

// ExecutionBudgetConfig keeps omitted deployment limits distinct from invalid
// explicit zero values. Actor and instance limits are independently optional.
type ExecutionBudgetConfig struct {
	Actor    ExecutionBudgetLimits `yaml:"actor"`
	Instance ExecutionBudgetLimits `yaml:"instance"`
}

type ExecutionBudgetLimits struct {
	MaxUnconfirmedMembers  *int64 `yaml:"max_unconfirmed_members"`
	MaxPayloadBytes        *int64 `yaml:"max_payload_bytes"`
	MaxPrepareOwners       *int64 `yaml:"max_prepare_owners"`
	MaxMainResponseOwners  *int64 `yaml:"max_main_response_owners"`
	MaxAuxResponseOwners   *int64 `yaml:"max_aux_response_owners"`
	MaxBuildWorkers        *int64 `yaml:"max_build_workers"`
	MaxQueuedDescriptors   *int64 `yaml:"max_queued_descriptors"`
	MaxLogicalStorageBytes *int64 `yaml:"max_logical_storage_bytes"`
}

// RuntimePolicy validates deployment limits and copies their values into the
// runtime policy. Zero in that policy means unlimited, never explicit zero.
func (p ExecutionBudgetConfig) RuntimePolicy() (resourcebudget.Policy, error) {
	var policy resourcebudget.Policy
	for _, scope := range []struct {
		name   string
		config ExecutionBudgetLimits
		limits *resourcebudget.Limits
	}{
		{"actor", p.Actor, &policy.Actor},
		{"instance", p.Instance, &policy.Instance},
	} {
		for _, field := range []struct {
			name   string
			value  *int64
			target *int64
		}{
			{"max_unconfirmed_members", scope.config.MaxUnconfirmedMembers, &scope.limits.MaxUnconfirmedMembers},
			{"max_payload_bytes", scope.config.MaxPayloadBytes, &scope.limits.MaxPayloadBytes},
			{"max_prepare_owners", scope.config.MaxPrepareOwners, &scope.limits.MaxPrepareOwners},
			{"max_main_response_owners", scope.config.MaxMainResponseOwners, &scope.limits.MaxMainResponseOwners},
			{"max_aux_response_owners", scope.config.MaxAuxResponseOwners, &scope.limits.MaxAuxResponseOwners},
			{"max_build_workers", scope.config.MaxBuildWorkers, &scope.limits.MaxBuildWorkers},
			{"max_queued_descriptors", scope.config.MaxQueuedDescriptors, &scope.limits.MaxQueuedDescriptors},
			{"max_logical_storage_bytes", scope.config.MaxLogicalStorageBytes, &scope.limits.MaxLogicalStorageBytes},
		} {
			if field.value == nil {
				continue
			}
			if *field.value <= 0 {
				return resourcebudget.Policy{}, fmt.Errorf("server.execution_budget.%s.%s must be positive when configured", scope.name, field.name)
			}
			*field.target = *field.value
		}
	}
	return policy, nil
}

// ValidateExecutionBudget also protects direct Server constructors which do
// not pass through the deployment document loader.
func ValidateExecutionBudget(p ExecutionBudgetConfig) error {
	_, err := p.RuntimePolicy()
	return err
}
