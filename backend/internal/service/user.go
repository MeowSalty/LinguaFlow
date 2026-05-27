package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	ErrForbidden            = errors.New("forbidden")
	ErrOrganizationExists   = errors.New("organization already exists")
	ErrOrganizationNotFound = errors.New("organization not found")
	ErrMembershipNotFound   = errors.New("membership not found")
	ErrOwnerRequired        = errors.New("organization must retain at least one owner")
)

type UserService struct {
	client *ent.Client
	auth   *AuthService
}

type UpdateProfileInput struct {
	DisplayName string
	Email       string
}

type CreateOrganizationInput struct {
	Name        string
	Slug        string
	DisplayName string
	Description string
}

type AddOrgMemberInput struct {
	Username string
	Role     string
}

type UpdateOrgMemberRoleInput struct {
	Role string
}

func NewUserService(client *ent.Client, auth *AuthService) *UserService {
	return &UserService{client: client, auth: auth}
}

func (s *UserService) GetMe(ctx context.Context, userID int) (*ent.User, error) {
	return s.client.User.Get(ctx, userID)
}

func (s *UserService) UpdateMe(ctx context.Context, userID int, input UpdateProfileInput) (*ent.User, error) {
	update := s.client.User.UpdateOneID(userID).
		SetDisplayName(strings.TrimSpace(input.DisplayName))
	if email := normalizeIdentity(input.Email); email != "" {
		if !strings.Contains(email, "@") {
			return nil, ErrInvalidInput
		}
		update.SetEmail(email)
	}
	updated, err := update.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	return updated, nil
}

func (s *UserService) ChangeMyPassword(ctx context.Context, userID int, currentPassword, newPassword string) error {
	return s.auth.ChangePassword(ctx, userID, currentPassword, newPassword)
}

func (s *UserService) CreateOrganization(ctx context.Context, actorUserID int, input CreateOrganizationInput) (*ent.Organization, error) {
	name := strings.TrimSpace(input.Name)
	slug := normalizeIdentity(input.Slug)
	if name == "" || slug == "" {
		return nil, ErrInvalidInput
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	org, err := tx.Organization.Create().
		SetName(name).
		SetSlug(slug).
		SetDisplayName(strings.TrimSpace(input.DisplayName)).
		SetDescription(strings.TrimSpace(input.Description)).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrOrganizationExists
		}
		return nil, err
	}
	if _, err = tx.OrgMembership.Create().
		SetOrganizationID(org.ID).
		SetUserID(actorUserID).
		SetRole(OrgRoleOwner).
		Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return org, nil
}

func (s *UserService) ListOrganizationsForUser(ctx context.Context, userID int) ([]*ent.Organization, error) {
	return s.client.Organization.Query().
		Where(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(userID)))).
		Order(ent.Asc(organization.FieldID)).
		All(ctx)
}

func (s *UserService) GetOrganization(ctx context.Context, actorUserID, orgID int) (*ent.Organization, error) {
	if _, err := s.requireMembership(ctx, actorUserID, orgID, OrgRoleMember); err != nil {
		return nil, err
	}
	org, err := s.client.Organization.Get(ctx, orgID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrOrganizationNotFound
		}
		return nil, err
	}
	return org, nil
}

func (s *UserService) UpdateOrganization(ctx context.Context, actorUserID, orgID int, input CreateOrganizationInput) (*ent.Organization, error) {
	if _, err := s.requireMembership(ctx, actorUserID, orgID, OrgRoleAdmin); err != nil {
		return nil, err
	}
	update := s.client.Organization.UpdateOneID(orgID)
	if v := strings.TrimSpace(input.Name); v != "" {
		update.SetName(v)
	}
	if v := normalizeIdentity(input.Slug); v != "" {
		update.SetSlug(v)
	}
	update.SetDisplayName(strings.TrimSpace(input.DisplayName))
	update.SetDescription(strings.TrimSpace(input.Description))
	updated, err := update.Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrOrganizationNotFound
		}
		if ent.IsConstraintError(err) {
			return nil, ErrOrganizationExists
		}
		return nil, err
	}
	return updated, nil
}

func (s *UserService) ListMembers(ctx context.Context, actorUserID, orgID int) ([]*ent.OrgMembership, error) {
	if _, err := s.requireMembership(ctx, actorUserID, orgID, OrgRoleMember); err != nil {
		return nil, err
	}
	return s.client.OrgMembership.Query().
		Where(orgmembership.HasOrganizationWith(organization.IDEQ(orgID))).
		WithUser().
		Order(ent.Asc(orgmembership.FieldID)).
		All(ctx)
}

func (s *UserService) AddMember(ctx context.Context, actorUserID, orgID int, input AddOrgMemberInput) (*ent.OrgMembership, error) {
	if _, err := s.requireMembership(ctx, actorUserID, orgID, OrgRoleAdmin); err != nil {
		return nil, err
	}
	role, err := normalizeOrgRole(input.Role)
	if err != nil {
		return nil, err
	}
	targetUser, err := s.client.User.Query().Where(user.UsernameEQ(normalizeIdentity(input.Username))).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrInvalidInput
		}
		return nil, err
	}
	created, err := s.client.OrgMembership.Create().
		SetOrganizationID(orgID).
		SetUserID(targetUser.ID).
		SetRole(role).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	return s.client.OrgMembership.Query().Where(orgmembership.IDEQ(created.ID)).WithUser().Only(ctx)
}

func (s *UserService) UpdateMemberRole(ctx context.Context, actorUserID, orgID, memberUserID int, input UpdateOrgMemberRoleInput) (*ent.OrgMembership, error) {
	if _, err := s.requireMembership(ctx, actorUserID, orgID, OrgRoleOwner); err != nil {
		return nil, err
	}
	role, err := normalizeOrgRole(input.Role)
	if err != nil {
		return nil, err
	}
	membership, err := s.client.OrgMembership.Query().
		Where(
			orgmembership.HasOrganizationWith(organization.IDEQ(orgID)),
			orgmembership.HasUserWith(user.IDEQ(memberUserID)),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrMembershipNotFound
		}
		return nil, err
	}
	if membership.Role == OrgRoleOwner && role != OrgRoleOwner {
		owners, err := s.countOwners(ctx, orgID)
		if err != nil {
			return nil, err
		}
		if owners <= 1 {
			return nil, ErrOwnerRequired
		}
	}
	updated, err := s.client.OrgMembership.UpdateOneID(membership.ID).SetRole(role).Save(ctx)
	if err != nil {
		return nil, err
	}
	return s.client.OrgMembership.Query().Where(orgmembership.IDEQ(updated.ID)).WithUser().Only(ctx)
}

func (s *UserService) RemoveMember(ctx context.Context, actorUserID, orgID, memberUserID int) error {
	actorMembership, err := s.requireMembership(ctx, actorUserID, orgID, OrgRoleAdmin)
	if err != nil {
		return err
	}
	membership, err := s.client.OrgMembership.Query().
		Where(
			orgmembership.HasOrganizationWith(organization.IDEQ(orgID)),
			orgmembership.HasUserWith(user.IDEQ(memberUserID)),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrMembershipNotFound
		}
		return err
	}
	if membership.Role == OrgRoleOwner {
		owners, err := s.countOwners(ctx, orgID)
		if err != nil {
			return err
		}
		if owners <= 1 {
			return ErrOwnerRequired
		}
		if actorMembership.Role != OrgRoleOwner && actorUserID != memberUserID {
			return ErrForbidden
		}
	}
	return s.client.OrgMembership.DeleteOneID(membership.ID).Exec(ctx)
}

func (s *UserService) requireMembership(ctx context.Context, actorUserID, orgID int, minRole string) (*ent.OrgMembership, error) {
	membership, err := s.client.OrgMembership.Query().
		Where(
			orgmembership.HasOrganizationWith(organization.IDEQ(orgID)),
			orgmembership.HasUserWith(user.IDEQ(actorUserID)),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			if _, orgErr := s.client.Organization.Get(ctx, orgID); orgErr != nil {
				if ent.IsNotFound(orgErr) {
					return nil, ErrOrganizationNotFound
				}
				return nil, orgErr
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

func (s *UserService) countOwners(ctx context.Context, orgID int) (int, error) {
	return s.client.OrgMembership.Query().
		Where(
			orgmembership.HasOrganizationWith(organization.IDEQ(orgID)),
			orgmembership.RoleEQ(OrgRoleOwner),
		).
		Count(ctx)
}

func normalizeOrgRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case OrgRoleOwner:
		return OrgRoleOwner, nil
	case OrgRoleAdmin:
		return OrgRoleAdmin, nil
	case "", OrgRoleMember:
		return OrgRoleMember, nil
	default:
		return "", fmt.Errorf("%w: invalid org role", ErrInvalidInput)
	}
}

func hasRequiredOrgRole(actual, required string) bool {
	rank := map[string]int{OrgRoleMember: 1, OrgRoleAdmin: 2, OrgRoleOwner: 3}
	return rank[actual] >= rank[required]
}
