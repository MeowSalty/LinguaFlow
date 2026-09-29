package service

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

const (
	OrgRoleOwner  = "owner"
	OrgRoleAdmin  = "admin"
	OrgRoleMember = "member"
)

var (
	ErrForbidden              = errors.New("forbidden")
	ErrOrganizationNameExists = errors.New("organization name already exists")
	ErrOrganizationSlugExists = errors.New("organization slug already exists")
	ErrOrganizationNotFound   = errors.New("organization not found")
	ErrMembershipNotFound     = errors.New("membership not found")
	ErrMembershipExists       = errors.New("membership already exists")
	ErrOwnerRequired          = errors.New("organization must retain at least one owner")
)

type OrganizationView struct {
	*ent.Organization
	CurrentUserRole string
}

type CreateOrganizationInput struct {
	Name        string
	Slug        string
	DisplayName string
	Description string
}

type UpdateOrganizationInput struct {
	Name        string
	Slug        string
	DisplayName *string
	Description *string
}

type AddOrgMemberInput struct {
	Username string
	Role     *string
}

type UpdateOrgMemberRoleInput struct {
	Role string
}

func (s *UserService) CreateOrganization(ctx context.Context, actorUserID int, input CreateOrganizationInput) (*OrganizationView, error) {
	name, slug := strings.TrimSpace(input.Name), normalizeIdentity(input.Slug)
	if actorUserID <= 0 || name == "" || slug == "" {
		return nil, ErrInvalidInput
	}
	var org *ent.Organization
	err := withOrganizationTransaction(ctx, s.client, func(client *ent.Client) error {
		created, err := client.Organization.Create().SetName(name).SetSlug(slug).
			SetDisplayName(strings.TrimSpace(input.DisplayName)).SetDescription(strings.TrimSpace(input.Description)).Save(ctx)
		if err != nil {
			return organizationConflict(err)
		}
		if _, err := client.OrgMembership.Create().SetOrganizationID(created.ID).SetUserID(actorUserID).SetRole(OrgRoleOwner).Save(ctx); err != nil {
			return err
		}
		if err := recordAuditEvent(ctx, client, organizationAudit(actorUserID, created.ID, "organization.create", "organization", created.ID, nil)); err != nil {
			return err
		}
		org = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &OrganizationView{Organization: org.Unwrap(), CurrentUserRole: OrgRoleOwner}, nil
}

func (s *UserService) ListOrganizationsForUser(ctx context.Context, userID int) ([]*OrganizationView, error) {
	if userID <= 0 {
		return nil, ErrInvalidInput
	}
	orgs, err := s.client.Organization.Query().
		Where(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(userID)))).
		WithMemberships(func(q *ent.OrgMembershipQuery) {
			q.Where(orgmembership.HasUserWith(user.IDEQ(userID)))
		}).Order(ent.Asc(organization.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*OrganizationView, 0, len(orgs))
	for _, org := range orgs {
		if len(org.Edges.Memberships) == 1 && validOrgRole(org.Edges.Memberships[0].Role) {
			result = append(result, &OrganizationView{Organization: org, CurrentUserRole: org.Edges.Memberships[0].Role})
		}
	}
	return result, nil
}

func (s *UserService) GetOrganization(ctx context.Context, actorUserID, orgID int) (*OrganizationView, error) {
	membership, err := s.requireMembership(ctx, actorUserID, orgID, OrgRoleMember)
	if err != nil {
		return nil, err
	}
	org, err := s.client.Organization.Get(ctx, orgID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrOrganizationNotFound
		}
		return nil, err
	}
	return &OrganizationView{Organization: org, CurrentUserRole: membership.Role}, nil
}

func (s *UserService) UpdateOrganization(ctx context.Context, actorUserID, orgID int, input UpdateOrganizationInput) (*OrganizationView, error) {
	name, slug := strings.TrimSpace(input.Name), normalizeIdentity(input.Slug)
	if actorUserID <= 0 || name == "" || slug == "" {
		return nil, ErrInvalidInput
	}
	var result *OrganizationView
	err := withOrganizationMutation(ctx, s.client, orgID, func(client *ent.Client) error {
		membership, err := requireOrganizationMembership(ctx, client, actorUserID, orgID, OrgRoleAdmin)
		if err != nil {
			return err
		}
		update := client.Organization.UpdateOneID(orgID).SetName(name).SetSlug(slug)
		if input.DisplayName != nil {
			update.SetDisplayName(strings.TrimSpace(*input.DisplayName))
		}
		if input.Description != nil {
			update.SetDescription(strings.TrimSpace(*input.Description))
		}
		org, err := update.Save(ctx)
		if err != nil {
			return organizationConflict(err)
		}
		if err := recordAuditEvent(ctx, client, organizationAudit(actorUserID, orgID, "organization.update", "organization", orgID, nil)); err != nil {
			return err
		}
		result = &OrganizationView{Organization: org, CurrentUserRole: membership.Role}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.Organization.Unwrap()
	return result, nil
}

func (s *UserService) ListMembers(ctx context.Context, actorUserID, orgID int) ([]*ent.OrgMembership, error) {
	if _, err := s.requireMembership(ctx, actorUserID, orgID, OrgRoleMember); err != nil {
		return nil, err
	}
	return s.client.OrgMembership.Query().Where(orgmembership.HasOrganizationWith(organization.IDEQ(orgID))).
		WithUser().Order(ent.Asc(orgmembership.FieldID)).All(ctx)
}

func (s *UserService) AddMember(ctx context.Context, actorUserID, orgID int, input AddOrgMemberInput) (*ent.OrgMembership, error) {
	role := OrgRoleMember
	if input.Role != nil {
		role = *input.Role
	}
	if actorUserID <= 0 || !validOrgRole(role) || normalizeIdentity(input.Username) == "" {
		return nil, ErrInvalidInput
	}
	var result *ent.OrgMembership
	err := withOrganizationMutation(ctx, s.client, orgID, func(client *ent.Client) error {
		actor, err := requireOrganizationMembership(ctx, client, actorUserID, orgID, OrgRoleAdmin)
		if err != nil {
			return err
		}
		if actor.Role != OrgRoleOwner && role != OrgRoleMember {
			return ErrForbidden
		}
		target, err := client.User.Query().Where(user.UsernameEQ(normalizeIdentity(input.Username))).Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return ErrInvalidInput
			}
			return err
		}
		created, err := client.OrgMembership.Create().SetOrganizationID(orgID).SetUserID(target.ID).SetRole(role).Save(ctx)
		if err != nil {
			if isOrganizationUniqueConstraint(err) {
				return ErrMembershipExists
			}
			return err
		}
		if err := recordAuditEvent(ctx, client, organizationAudit(actorUserID, orgID, "organization.member.add", "organization_member", target.ID, map[string]any{"new_role": role})); err != nil {
			return err
		}
		created.Edges.User = target
		result = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.Unwrap()
	result.Edges.User.Unwrap()
	return result, nil
}

func (s *UserService) UpdateMemberRole(ctx context.Context, actorUserID, orgID, memberUserID int, input UpdateOrgMemberRoleInput) (*ent.OrgMembership, error) {
	if actorUserID <= 0 || memberUserID <= 0 || !validOrgRole(input.Role) {
		return nil, ErrInvalidInput
	}
	var result *ent.OrgMembership
	err := withOrganizationMutation(ctx, s.client, orgID, func(client *ent.Client) error {
		if _, err := requireOrganizationMembership(ctx, client, actorUserID, orgID, OrgRoleOwner); err != nil {
			return err
		}
		membership, err := findOrganizationMember(ctx, client, orgID, memberUserID)
		if err != nil {
			return err
		}
		if membership.Role == input.Role {
			result = membership
			return nil
		}
		if err := retainOrganizationOwner(ctx, client, orgID, membership); err != nil {
			return err
		}
		updated, err := client.OrgMembership.UpdateOneID(membership.ID).SetRole(input.Role).Save(ctx)
		if err != nil {
			return err
		}
		if err := recordAuditEvent(ctx, client, organizationAudit(actorUserID, orgID, "organization.member.role_change", "organization_member", memberUserID, map[string]any{"old_role": membership.Role, "new_role": input.Role})); err != nil {
			return err
		}
		updated.Edges.User = membership.Edges.User
		result = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.Unwrap()
	result.Edges.User.Unwrap()
	return result, nil
}

func (s *UserService) RemoveMember(ctx context.Context, actorUserID, orgID, memberUserID int) error {
	if actorUserID <= 0 || memberUserID <= 0 {
		return ErrInvalidInput
	}
	return withOrganizationMutation(ctx, s.client, orgID, func(client *ent.Client) error {
		actor, err := requireOrganizationMembership(ctx, client, actorUserID, orgID, OrgRoleMember)
		if err != nil {
			return err
		}
		membership, err := findOrganizationMember(ctx, client, orgID, memberUserID)
		if err != nil {
			return err
		}
		if actorUserID != memberUserID && actor.Role != OrgRoleOwner && !(actor.Role == OrgRoleAdmin && membership.Role == OrgRoleMember) {
			return ErrForbidden
		}
		if err := retainOrganizationOwner(ctx, client, orgID, membership); err != nil {
			return err
		}
		if err := client.OrgMembership.DeleteOneID(membership.ID).Exec(ctx); err != nil {
			return err
		}
		action := "organization.member.remove"
		if actorUserID == memberUserID {
			action = "organization.member.leave"
		}
		return recordAuditEvent(ctx, client, organizationAudit(actorUserID, orgID, action, "organization_member", memberUserID, map[string]any{"old_role": membership.Role}))
	})
}

func (s *UserService) RequireMembership(ctx context.Context, actorUserID, orgID int, minRole string) (*ent.OrgMembership, error) {
	return s.requireMembership(ctx, actorUserID, orgID, minRole)
}

func (s *UserService) requireMembership(ctx context.Context, actorUserID, orgID int, minRole string) (*ent.OrgMembership, error) {
	return requireOrganizationMembership(ctx, s.client, actorUserID, orgID, minRole)
}

func requireOrganizationMembership(ctx context.Context, client *ent.Client, actorUserID, orgID int, minRole string) (*ent.OrgMembership, error) {
	if actorUserID <= 0 || orgID <= 0 || !validOrgRole(minRole) {
		return nil, ErrInvalidInput
	}
	membership, err := client.OrgMembership.Query().Where(orgmembership.HasOrganizationWith(organization.IDEQ(orgID)), orgmembership.HasUserWith(user.IDEQ(actorUserID))).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			exists, orgErr := client.Organization.Query().Where(organization.IDEQ(orgID)).Exist(ctx)
			if orgErr != nil {
				return nil, orgErr
			}
			if !exists {
				return nil, ErrOrganizationNotFound
			}
			return nil, ErrForbidden
		}
		return nil, err
	}
	if !hasRequiredOrgRole(membership.Role, minRole) {
		return nil, ErrForbidden
	}
	return membership, nil
}

func findOrganizationMember(ctx context.Context, client *ent.Client, orgID, memberUserID int) (*ent.OrgMembership, error) {
	membership, err := client.OrgMembership.Query().Where(orgmembership.HasOrganizationWith(organization.IDEQ(orgID)), orgmembership.HasUserWith(user.IDEQ(memberUserID))).WithUser().Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrMembershipNotFound
	}
	return membership, err
}

func retainOrganizationOwner(ctx context.Context, client *ent.Client, orgID int, target *ent.OrgMembership) error {
	if target.Role != OrgRoleOwner {
		return nil
	}
	count, err := client.OrgMembership.Query().Where(orgmembership.HasOrganizationWith(organization.IDEQ(orgID)), orgmembership.RoleEQ(OrgRoleOwner)).Count(ctx)
	if err != nil {
		return err
	}
	if count <= 1 {
		return ErrOwnerRequired
	}
	return nil
}

func validOrgRole(role string) bool {
	return role == OrgRoleOwner || role == OrgRoleAdmin || role == OrgRoleMember
}

func hasRequiredOrgRole(actual, required string) bool {
	if !validOrgRole(actual) || !validOrgRole(required) {
		return false
	}
	return actual == OrgRoleOwner || actual == required || required == OrgRoleMember
}

func organizationAudit(actorUserID, orgID int, action, resourceType string, resourceID int, metadata map[string]any) AuditEvent {
	return AuditEvent{ActorUserID: actorUserID, OrgID: &orgID, Action: action, ResourceType: resourceType, ResourceID: resourceID, Metadata: metadata, VisibilityScope: "organization"}
}

func organizationConflict(err error) error {
	if !isOrganizationUniqueConstraint(err) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		constraint := strings.ToLower(pgErr.ConstraintName)
		if strings.Contains(constraint, "slug") {
			return ErrOrganizationSlugExists
		}
		if strings.Contains(constraint, "name") {
			return ErrOrganizationNameExists
		}
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "organizations.slug") {
		return ErrOrganizationSlugExists
	}
	if strings.Contains(message, "organizations.name") {
		return ErrOrganizationNameExists
	}
	return err
}

func isOrganizationUniqueConstraint(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code() == 2067 || sqliteErr.Code() == 1555
	}
	return false
}
