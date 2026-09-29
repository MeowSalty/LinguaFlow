package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

type AuditService struct {
	client   *ent.Client
	users    *UserService
	projects *ProjectService
}

type AuditEvent struct {
	ActorUserID     int
	OrgID           *int
	ProjectID       *int
	Action          string
	ResourceType    string
	ResourceID      int
	Message         string
	Metadata        map[string]any
	VisibilityScope string
}

type ActivityPage struct {
	Items      []*ent.ActivityLog
	NextCursor int
}

func NewAuditService(client *ent.Client, users *UserService, projects *ProjectService) *AuditService {
	return &AuditService{client: client, users: users, projects: projects}
}

// EffectiveProjectOrgID preserves personal-owner precedence for malformed dual ownership.
func EffectiveProjectOrgID(row *ent.Project) *int {
	if row == nil || row.OwnerUserID != nil || row.OwnerOrgID == nil || *row.OwnerOrgID <= 0 {
		return nil
	}
	id := *row.OwnerOrgID
	return &id
}

func (s *AuditService) Record(ctx context.Context, event AuditEvent) error {
	return recordAuditEvent(ctx, s.client, event)
}

// recordAuditEvent also accepts tx.Client(), so a mutation and its audit commit together.
func recordAuditEvent(ctx context.Context, client *ent.Client, event AuditEvent) error {
	if event.Action == "" || event.ResourceType == "" {
		return ErrInvalidInput
	}
	scope := event.VisibilityScope
	if event.ProjectID != nil {
		if *event.ProjectID <= 0 {
			return ErrInvalidInput
		}
		row, err := client.Project.Get(ctx, *event.ProjectID)
		if err != nil {
			return err
		}
		effectiveOrg := EffectiveProjectOrgID(row)
		if event.OrgID != nil && (effectiveOrg == nil || *event.OrgID != *effectiveOrg) {
			return fmt.Errorf("%w: conflicting audit organization", ErrInvalidInput)
		}
		event.OrgID = effectiveOrg
		if row.OwnerUserID == nil && effectiveOrg == nil {
			return ErrProjectOwnerConflict
		}
		if scope != "" && scope != "project" {
			return ErrInvalidInput
		}
		scope = "project"
	} else if event.OrgID != nil {
		if *event.OrgID <= 0 || (scope != "" && scope != "organization") {
			return ErrInvalidInput
		}
		scope = "organization"
	} else if scope == "" {
		// Missing attribution is not evidence of personal ownership.
		scope = "unknown"
	} else if scope != "personal" && scope != "unknown" {
		return ErrInvalidInput
	}
	if scope == "personal" && event.ActorUserID <= 0 {
		return ErrInvalidInput
	}
	if strings.HasPrefix(event.Action, "admin.") || strings.HasPrefix(event.Action, "system.") {
		scope = "unknown"
	}
	create := client.ActivityLog.Create().SetAction(event.Action).
		SetResourceType(event.ResourceType).SetMessage(event.Message).
		SetMetadata(SanitizeActivityMetadata(event.Action, event.Metadata)).
		SetVisibilityScope(activitylog.VisibilityScope(scope))
	if event.ActorUserID > 0 {
		create.SetActorID(event.ActorUserID)
	}
	if event.OrgID != nil {
		create.SetOrganizationID(*event.OrgID)
	}
	if event.ProjectID != nil {
		create.SetProjectID(*event.ProjectID)
	}
	if event.ResourceID > 0 {
		create.SetResourceID(event.ResourceID)
	}
	return create.Exec(ctx)
}

func readableOrganizationPredicate(actorUserID int) predicate.Organization {
	return organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(actorUserID)),
		orgmembership.RoleIn(OrgRoleMember, OrgRoleAdmin, OrgRoleOwner))
}

func readableActivityPredicate(actorUserID int) predicate.ActivityLog {
	return activitylog.Or(
		activitylog.And(activitylog.VisibilityScopeEQ(activitylog.VisibilityScopeProject),
			activitylog.HasProjectWith(readableProjectPredicate(actorUserID))),
		activitylog.And(activitylog.VisibilityScopeEQ(activitylog.VisibilityScopeOrganization),
			activitylog.Not(activitylog.HasProject()),
			activitylog.HasOrganizationWith(readableOrganizationPredicate(actorUserID))),
		activitylog.And(activitylog.VisibilityScopeEQ(activitylog.VisibilityScopePersonal),
			activitylog.Not(activitylog.HasProject()), activitylog.Not(activitylog.HasOrganization()),
			activitylog.HasActorWith(user.IDEQ(actorUserID))),
	)
}

// An optional organization keeps existing service callers source compatible.
func (s *AuditService) ListActivity(ctx context.Context, actorUserID, afterID, limit int, orgIDs ...int) (*ActivityPage, error) {
	if afterID < 0 || limit < 0 || limit > 100 || len(orgIDs) > 1 {
		return nil, ErrInvalidInput
	}
	if limit == 0 {
		limit = 50
	}
	q := s.client.ActivityLog.Query().Where(readableActivityPredicate(actorUserID))
	if len(orgIDs) == 1 {
		orgID := orgIDs[0]
		if orgID <= 0 {
			return nil, ErrInvalidInput
		}
		if _, err := s.users.GetOrganization(ctx, actorUserID, orgID); err != nil {
			return nil, err
		}
		q.Where(activitylog.Or(
			activitylog.And(activitylog.VisibilityScopeEQ(activitylog.VisibilityScopeProject),
				activitylog.HasProjectWith(project.OwnerUserIDIsNil(), project.OwnerOrgIDEQ(orgID))),
			activitylog.And(activitylog.VisibilityScopeEQ(activitylog.VisibilityScopeOrganization),
				activitylog.Not(activitylog.HasProject()), activitylog.HasOrganizationWith(organization.IDEQ(orgID))),
		))
	}
	if afterID > 0 {
		q.Where(activitylog.IDLT(afterID))
	}
	rows, err := q.Order(ent.Desc(activitylog.FieldID)).Limit(limit + 1).
		WithActor().WithOrganization().WithProject().All(ctx)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []*ent.ActivityLog{}
	}
	page := &ActivityPage{Items: rows}
	if len(rows) > limit {
		page.NextCursor = rows[limit-1].ID
		page.Items = rows[:limit]
	}
	return page, nil
}

// SanitizeActivityMetadata applies the same allowlist on new writes and legacy responses.
func SanitizeActivityMetadata(action string, metadata map[string]any) map[string]any {
	out := map[string]any{}
	var keys []string
	switch {
	case strings.HasPrefix(action, "org."), strings.HasPrefix(action, "organization."):
		keys = []string{"target_user_id", "user_id", "old_role", "new_role", "role"}
	case action == "quick_translate":
		keys = []string{"execution_plan_id", "project_id"}
	case strings.HasPrefix(action, "resource.segment."):
		keys = []string{"execution_plan_id", "target_changed", "preview_kind", "resolved_codes"}
	case strings.HasPrefix(action, "segment."):
		keys = []string{"match_mode", "case_sensitive", "whole_word", "operation_id", "applied_count", "skipped_count", "restored_count", "code", "disposition"}
	case action == "qa.recheck":
		keys = []string{"profile_id", "resources_checked", "segments_checked", "issues_new"}
	case strings.HasPrefix(action, "project."), strings.HasPrefix(action, "job."), strings.HasPrefix(action, "glossary."):
		keys = []string{"project_id", "job_id", "target_id", "updated_count", "skipped_count"}
	}
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case bool, int, int32, int64, uint, uint32, uint64, float64:
			out[key] = v
		case string:
			if len(v) > 128 {
				continue
			}
			if key == "old_role" || key == "new_role" || key == "role" {
				if v != OrgRoleOwner && v != OrgRoleAdmin && v != OrgRoleMember {
					continue
				}
			}
			out[key] = v
		case []string:
			if key == "resolved_codes" && len(v) <= 100 {
				valid := true
				for _, code := range v {
					if len(code) > 128 {
						valid = false
					}
				}
				if valid {
					out[key] = append([]string(nil), v...)
				}
			}
		case []any:
			if key == "resolved_codes" && len(v) <= 100 {
				codes := make([]string, 0, len(v))
				for _, item := range v {
					if code, ok := item.(string); ok && len(code) <= 128 {
						codes = append(codes, code)
					}
				}
				if len(codes) == len(v) {
					out[key] = codes
				}
			}
		}
	}
	return out
}
