package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgbackend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/userbackend"
)

const (
	BackendTypeOpenAI    = "openai"
	BackendTypeAnthropic = "anthropic"
	BackendTypeGoogle    = "google"

	BackendSourceUser = "user"
	BackendSourceOrg  = "org"
)

var (
	ErrBackendNotFound      = errors.New("backend not found")
	ErrBackendExists        = errors.New("backend already exists")
	ErrBackendTypeInvalid   = errors.New("backend type invalid")
	ErrBackendSourceInvalid = errors.New("backend source invalid")
	ErrBackendNameAmbiguous = errors.New("backend name ambiguous")
)

type BackendService struct {
	client *ent.Client
	users  *UserService
}

type BackendInput struct {
	Name     string
	Type     string
	Priority int
	Options  map[string]any
}

type BackendRecord struct {
	ID             int
	Source         string
	Name           string
	Type           string
	Priority       int
	Options        map[string]any
	OptionsVisible bool
	OwnerUserID    *int
	OwnerOrgID     *int
}

type resolvedBackend struct {
	Source      string
	BackendID   int
	Name        string
	Type        string
	Priority    int
	Options     map[string]any
	OwnerUserID *int
	OwnerOrgID  *int
}

func NewBackendService(client *ent.Client, users *UserService) *BackendService {
	return &BackendService{client: client, users: users}
}

func (s *BackendService) CreateUserBackend(ctx context.Context, actorUserID int, input BackendInput) (*BackendRecord, error) {
	normalized, err := normalizeBackendInput(input)
	if err != nil {
		return nil, err
	}
	created, err := s.client.UserBackend.Create().
		SetUserID(actorUserID).
		SetName(normalized.Name).
		SetBackendType(userbackend.BackendType(normalized.Type)).
		SetPriority(normalized.Priority).
		SetOptions(cloneMap(normalized.Options)).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrBackendExists
		}
		return nil, err
	}
	return userBackendRecord(created, actorUserID), nil
}

func (s *BackendService) ListUserBackends(ctx context.Context, actorUserID int) ([]*BackendRecord, error) {
	rows, err := s.client.UserBackend.Query().
		Where(userbackend.HasUserWith(user.IDEQ(actorUserID))).
		Order(ent.Desc(userbackend.FieldPriority), ent.Asc(userbackend.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*BackendRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, userBackendRecord(row, actorUserID))
	}
	return out, nil
}

func (s *BackendService) UpdateUserBackend(ctx context.Context, actorUserID, backendID int, input BackendInput) (*BackendRecord, error) {
	normalized, err := normalizeBackendInput(input)
	if err != nil {
		return nil, err
	}
	target, err := s.client.UserBackend.Query().
		Where(userbackend.IDEQ(backendID), userbackend.HasUserWith(user.IDEQ(actorUserID))).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrBackendNotFound
		}
		return nil, err
	}
	updated, err := s.client.UserBackend.UpdateOneID(target.ID).
		SetName(normalized.Name).
		SetBackendType(userbackend.BackendType(normalized.Type)).
		SetPriority(normalized.Priority).
		SetOptions(cloneMap(normalized.Options)).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrBackendExists
		}
		return nil, err
	}
	return userBackendRecord(updated, actorUserID), nil
}

func (s *BackendService) DeleteUserBackend(ctx context.Context, actorUserID, backendID int) error {
	deleted, err := s.client.UserBackend.Delete().
		Where(userbackend.IDEQ(backendID), userbackend.HasUserWith(user.IDEQ(actorUserID))).
		Exec(ctx)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrBackendNotFound
	}
	return nil
}

func (s *BackendService) CreateOrgBackend(ctx context.Context, actorUserID, orgID int, input BackendInput) (*BackendRecord, error) {
	if _, err := s.users.requireMembership(ctx, actorUserID, orgID, OrgRoleAdmin); err != nil {
		return nil, err
	}
	normalized, err := normalizeBackendInput(input)
	if err != nil {
		return nil, err
	}
	created, err := s.client.OrgBackend.Create().
		SetOrganizationID(orgID).
		SetName(normalized.Name).
		SetBackendType(orgbackend.BackendType(normalized.Type)).
		SetPriority(normalized.Priority).
		SetOptions(cloneMap(normalized.Options)).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrBackendExists
		}
		return nil, err
	}
	return orgBackendRecord(created, orgID, true), nil
}

func (s *BackendService) ListOrgBackends(ctx context.Context, actorUserID, orgID int) ([]*BackendRecord, error) {
	membership, err := s.users.requireMembership(ctx, actorUserID, orgID, OrgRoleMember)
	if err != nil {
		return nil, err
	}
	showOptions := hasRequiredOrgRole(membership.Role, OrgRoleAdmin)
	rows, err := s.client.OrgBackend.Query().
		Where(orgbackend.HasOrganizationWith(organization.IDEQ(orgID))).
		Order(ent.Desc(orgbackend.FieldPriority), ent.Asc(orgbackend.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*BackendRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, orgBackendRecord(row, orgID, showOptions))
	}
	return out, nil
}

func (s *BackendService) UpdateOrgBackend(ctx context.Context, actorUserID, orgID, backendID int, input BackendInput) (*BackendRecord, error) {
	if _, err := s.users.requireMembership(ctx, actorUserID, orgID, OrgRoleAdmin); err != nil {
		return nil, err
	}
	normalized, err := normalizeBackendInput(input)
	if err != nil {
		return nil, err
	}
	target, err := s.client.OrgBackend.Query().
		Where(orgbackend.IDEQ(backendID), orgbackend.HasOrganizationWith(organization.IDEQ(orgID))).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrBackendNotFound
		}
		return nil, err
	}
	updated, err := s.client.OrgBackend.UpdateOneID(target.ID).
		SetName(normalized.Name).
		SetBackendType(orgbackend.BackendType(normalized.Type)).
		SetPriority(normalized.Priority).
		SetOptions(cloneMap(normalized.Options)).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrBackendExists
		}
		return nil, err
	}
	return orgBackendRecord(updated, orgID, true), nil
}

func (s *BackendService) DeleteOrgBackend(ctx context.Context, actorUserID, orgID, backendID int) error {
	if _, err := s.users.requireMembership(ctx, actorUserID, orgID, OrgRoleAdmin); err != nil {
		return err
	}
	deleted, err := s.client.OrgBackend.Delete().
		Where(orgbackend.IDEQ(backendID), orgbackend.HasOrganizationWith(organization.IDEQ(orgID))).
		Exec(ctx)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrBackendNotFound
	}
	return nil
}

func (s *BackendService) resolveAccessibleBackends(ctx context.Context, project *ent.Project) ([]resolvedBackend, error) {
	out := make([]resolvedBackend, 0)
	if project.OwnerUserID != nil {
		userBackends, err := s.client.UserBackend.Query().
			Where(userbackend.HasUserWith(user.IDEQ(*project.OwnerUserID))).
			Order(ent.Desc(userbackend.FieldPriority), ent.Asc(userbackend.FieldID)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range userBackends {
			ownerUserID := *project.OwnerUserID
			out = append(out, resolvedBackend{
				Source:      BackendSourceUser,
				BackendID:   row.ID,
				Name:        row.Name,
				Type:        string(row.BackendType),
				Priority:    row.Priority,
				Options:     cloneMap(row.Options),
				OwnerUserID: &ownerUserID,
			})
		}

		orgIDs, err := s.client.Organization.Query().
			Where(organization.HasMembershipsWith(orgmembership.HasUserWith(user.IDEQ(*project.OwnerUserID)))).
			IDs(ctx)
		if err != nil {
			return nil, err
		}
		if len(orgIDs) > 0 {
			orgBackends, err := s.client.OrgBackend.Query().
				Where(orgbackend.HasOrganizationWith(organization.IDIn(orgIDs...))).
				Order(ent.Desc(orgbackend.FieldPriority), ent.Asc(orgbackend.FieldID)).
				All(ctx)
			if err != nil {
				return nil, err
			}
			for _, row := range orgBackends {
				orgID, err := row.QueryOrganization().OnlyID(ctx)
				if err != nil {
					return nil, err
				}
				ownerOrgID := orgID
				out = append(out, resolvedBackend{
					Source:     BackendSourceOrg,
					BackendID:  row.ID,
					Name:       row.Name,
					Type:       string(row.BackendType),
					Priority:   row.Priority,
					Options:    cloneMap(row.Options),
					OwnerOrgID: &ownerOrgID,
				})
			}
		}
	}
	if project.OwnerOrgID != nil {
		orgBackends, err := s.client.OrgBackend.Query().
			Where(orgbackend.HasOrganizationWith(organization.IDEQ(*project.OwnerOrgID))).
			Order(ent.Desc(orgbackend.FieldPriority), ent.Asc(orgbackend.FieldID)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range orgBackends {
			ownerOrgID := *project.OwnerOrgID
			out = append(out, resolvedBackend{
				Source:     BackendSourceOrg,
				BackendID:  row.ID,
				Name:       row.Name,
				Type:       string(row.BackendType),
				Priority:   row.Priority,
				Options:    cloneMap(row.Options),
				OwnerOrgID: &ownerOrgID,
			})
		}
	}
	return dedupeResolvedBackends(out), nil
}

func normalizeBackendInput(input BackendInput) (BackendInput, error) {
	name := strings.TrimSpace(input.Name)
	typ := strings.ToLower(strings.TrimSpace(input.Type))
	if name == "" || typ == "" {
		return BackendInput{}, ErrInvalidInput
	}
	if !isAllowedBackendType(typ) {
		return BackendInput{}, ErrBackendTypeInvalid
	}
	return BackendInput{
		Name:     name,
		Type:     typ,
		Priority: input.Priority,
		Options:  cloneMap(input.Options),
	}, nil
}

func isAllowedBackendType(typ string) bool {
	switch typ {
	case BackendTypeOpenAI, BackendTypeAnthropic, BackendTypeGoogle:
		return true
	default:
		return false
	}
}

func userBackendRecord(row *ent.UserBackend, ownerUserID int) *BackendRecord {
	owner := ownerUserID
	return &BackendRecord{
		ID:             row.ID,
		Source:         BackendSourceUser,
		Name:           row.Name,
		Type:           string(row.BackendType),
		Priority:       row.Priority,
		Options:        cloneMap(row.Options),
		OptionsVisible: true,
		OwnerUserID:    &owner,
	}
}

func orgBackendRecord(row *ent.OrgBackend, ownerOrgID int, showOptions bool) *BackendRecord {
	owner := ownerOrgID
	record := &BackendRecord{
		ID:             row.ID,
		Source:         BackendSourceOrg,
		Name:           row.Name,
		Type:           string(row.BackendType),
		Priority:       row.Priority,
		OptionsVisible: showOptions,
		OwnerOrgID:     &owner,
	}
	if showOptions {
		record.Options = cloneMap(row.Options)
	}
	return record
}

func dedupeResolvedBackends(in []resolvedBackend) []resolvedBackend {
	seen := make(map[string]struct{}, len(in))
	out := make([]resolvedBackend, 0, len(in))
	for _, item := range in {
		key := backendBindingKey(item.Source, item.BackendID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func backendBindingKey(source string, backendID int) string {
	return fmt.Sprintf("%s:%d", source, backendID)
}
