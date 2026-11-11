package config

import (
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type ExecutionBudgetConfig = resourcebudget.Policy
type ExecutionBudgetLimits = resourcebudget.Limits

// ValidateExecutionBudget also protects direct Server constructors which do
// not pass through the deployment document loader.
func ValidateExecutionBudget(p ExecutionBudgetConfig) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("server.execution_budget: %w", err)
	}
	return nil
}
