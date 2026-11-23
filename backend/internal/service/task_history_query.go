package service

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

type HistoryDeleteCandidate struct {
	Kind      string
	ID        int
	ProjectID int
	Status    string
}

type historyDeleteObservationKey struct{}
type historyDeleteObservation struct {
	actor    int
	projects map[int]bool
}

// PrepareDeleteObservation batches advisory project authorization for one page.
// CanDelete still observes maintenance and each task's current busy state;
// Delete always rechecks authorization and state in its write transaction.
func (s *TaskHistoryService) PrepareDeleteObservation(ctx context.Context, actor int, candidates []HistoryDeleteCandidate) context.Context {
	if s == nil {
		return ctx
	}
	ids := make([]int, 0, len(candidates))
	seen := make(map[int]bool, len(candidates))
	for _, row := range candidates {
		if row.Kind != OperationStorage && historyTerminal(row.Status) && !seen[row.ProjectID] {
			seen[row.ProjectID] = true
			ids = append(ids, row.ProjectID)
		}
	}
	allowed := make(map[int]bool, len(ids))
	if len(ids) > 0 && !s.blocked() {
		rows, err := s.client.Project.Query().Where(project.IDIn(ids...), project.StorageStateEQ("active"), project.Or(
			project.OwnerUserIDEQ(actor),
			project.And(project.OwnerUserIDIsNil(), project.HasOwnerOrgWith(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(actor)), orgmembership.RoleIn(OrgRoleAdmin, OrgRoleOwner)))),
		)).IDs(ctx)
		if err == nil {
			for _, id := range rows {
				allowed[id] = true
			}
		}
	}
	return context.WithValue(ctx, historyDeleteObservationKey{}, historyDeleteObservation{actor: actor, projects: allowed})
}
