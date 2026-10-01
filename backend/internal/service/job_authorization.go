package service

import (
	"context"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

// validateSnapshotReferences prevents private dependencies from being frozen
// into a snapshot visible to an entire organization. Runtime workers continue
// using the frozen snapshot independently of the creator's later membership.
func (s *JobService) validateSnapshotReferences(ctx context.Context, actorUserID int, projectRow *ent.Project, plan *ent.ExecutionPlanTemplate) error {
	orgID := EffectiveProjectOrgID(projectRow)
	if err := validateSharedReference(plan.Scope, plan.OwnerOrgID, orgID); err != nil {
		return fmt.Errorf("execution plan ownership: %w", err)
	}
	// The validator is bound to this service's database, including transactional clients.
	plans := s.executionPlans
	if plans == nil {
		plans = NewExecutionPlanService(s.client, NewUserService(s.client, nil), s.profiles)
	}
	return plans.validatePlanReferences(ctx, actorUserID, orgID, plan.ProfileID, plan.RubyRetry, plan.Rounds)
}
