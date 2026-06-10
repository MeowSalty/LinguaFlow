package service

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

type AuditService struct {
	client   *ent.Client
	users    *UserService
	projects *ProjectService
}

type AuditEvent struct {
	ActorUserID  int
	OrgID        *int
	ProjectID    *int
	Action       string
	ResourceType string
	ResourceID   int
	Message      string
	Metadata     map[string]any
}

type ActivityPage struct {
	Items      []*ent.ActivityLog
	NextCursor int
}

func NewAuditService(client *ent.Client, users *UserService, projects *ProjectService) *AuditService {
	return &AuditService{client: client, users: users, projects: projects}
}

func (s *AuditService) Record(ctx context.Context, event AuditEvent) error {
	if event.Action == "" || event.ResourceType == "" {
		return ErrInvalidInput
	}
	create := s.client.ActivityLog.Create().
		SetAction(event.Action).
		SetResourceType(event.ResourceType).
		SetMessage(event.Message).
		SetMetadata(cloneMap(event.Metadata))
	if event.ActorUserID > 0 {
		create.SetActorID(event.ActorUserID)
	}
	if event.OrgID != nil && *event.OrgID > 0 {
		create.SetOrganizationID(*event.OrgID)
	}
	if event.ProjectID != nil && *event.ProjectID > 0 {
		create.SetProjectID(*event.ProjectID)
	}
	if event.ResourceID > 0 {
		create.SetResourceID(event.ResourceID)
	}
	return create.Exec(ctx)
}

func (s *AuditService) ListActivity(ctx context.Context, actorUserID, afterID, limit int) (*ActivityPage, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	predicates := []func(*ent.ActivityLogQuery){
		func(q *ent.ActivityLogQuery) {
			q.Where(activitylog.Or(
				activitylog.HasActorWith(user.IDEQ(actorUserID)),
				activitylog.HasOrganizationWith(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(actorUserID)))),
				activitylog.HasProjectWith(project.Or(
					project.OwnerUserIDEQ(actorUserID),
					project.HasOwnerOrgWith(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(actorUserID)))),
				)),
			))
		},
	}
	if afterID > 0 {
		predicates = append(predicates, func(q *ent.ActivityLogQuery) { q.Where(activitylog.IDGT(afterID)) })
	}
	query := s.client.ActivityLog.Query()
	for _, apply := range predicates {
		apply(query)
	}
	rows, err := query.Order(ent.Asc(activitylog.FieldID)).Limit(limit + 1).WithActor().WithOrganization().WithProject().All(ctx)
	if err != nil {
		return nil, err
	}
	page := &ActivityPage{Items: rows}
	if len(rows) > limit {
		page.NextCursor = rows[limit-1].ID
		page.Items = rows[:limit]
	}
	return page, nil
}
