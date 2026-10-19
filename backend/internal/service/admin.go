package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/accountidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

var (
	ErrAdminSelfDemotion  = errors.New("admin cannot change own role")
	ErrAdminSelfDeletion  = errors.New("admin cannot disable self")
	ErrLastAdmin          = errors.New("cannot remove the last active admin")
	ErrRegistrationClosed = errors.New("registration is disabled")
)

type AdminService struct {
	client *ent.Client
}

type ListUsersParams struct {
	Search string
	Role   string
	Active *bool
	Cursor int
	Limit  int
}

type PaginatedUsers struct {
	Items []*ent.User
	Total int
}

type AdminCreateUserInput struct {
	Username    string
	Password    string
	Email       string
	DisplayName string
	Role        string
}

type AdminUpdateUserInput struct {
	DisplayName *string
	Email       *string
	Role        *string
	Active      *bool
}

type SystemStats struct {
	TotalUsers         int `json:"total_users"`
	ActiveUsers        int `json:"active_users"`
	TotalProjects      int `json:"total_projects"`
	TotalOrganizations int `json:"total_organizations"`
	TotalJobs          int `json:"total_jobs"`
	TotalResources     int `json:"total_resources"`
}

type ListAuditLogsParams struct {
	Cursor int
	Limit  int
}

type PaginatedAuditLogs struct {
	Items []*ent.ActivityLog
	Total int
}

func NewAdminService(client *ent.Client) *AdminService {
	return &AdminService{client: client}
}

func (s *AdminService) ListUsers(ctx context.Context, params ListUsersParams) (*PaginatedUsers, error) {
	if params.Limit <= 0 || params.Limit > 100 {
		params.Limit = 50
	}

	query := s.client.User.Query()

	if params.Search != "" {
		search := strings.ToLower(params.Search)
		query = query.Where(user.Or(
			user.UsernameContainsFold(search),
			user.EmailContainsFold(search),
			user.DisplayNameContainsFold(search),
		))
	}
	if params.Role != "" {
		query = query.Where(user.RoleEQ(params.Role))
	}
	if params.Active != nil {
		query = query.Where(user.ActiveEQ(*params.Active))
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, err
	}

	if params.Cursor > 0 {
		query = query.Where(user.IDGT(params.Cursor))
	}

	items, err := query.Order(ent.Asc(user.FieldID)).Limit(params.Limit).All(ctx)
	if err != nil {
		return nil, err
	}

	return &PaginatedUsers{Items: items, Total: total}, nil
}

func (s *AdminService) GetUser(ctx context.Context, userID int) (*ent.User, error) {
	return s.client.User.Get(ctx, userID)
}

func (s *AdminService) CreateUser(ctx context.Context, input AdminCreateUserInput) (*ent.User, error) {
	role := SystemRoleUser
	if input.Role != "" {
		role = input.Role
	}
	if role != SystemRoleUser && role != SystemRoleAdmin {
		return nil, ErrInvalidInput
	}
	username, err := accountidentity.NormalizeUsername(input.Username)
	if err != nil {
		return nil, invalidAccountField("username", err)
	}
	purpose := accountidentity.Public
	if role == SystemRoleAdmin {
		purpose = accountidentity.Administrator
	}
	if err := accountidentity.ValidateAllocation(username, purpose); err != nil {
		return nil, invalidAccountField("username", err)
	}
	email, err := accountidentity.NormalizeEmail(input.Email)
	if err != nil {
		return nil, invalidAccountField("email", err)
	}
	if err := validateNewPassword(input.Password); err != nil {
		return nil, err
	}
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	create := s.client.User.Create().SetUsername(username).SetPasswordHash(passwordHash).SetDisplayName(strings.TrimSpace(input.DisplayName)).SetRole(role).SetActive(true)
	if email != "" {
		create.SetEmail(email)
	}
	created, err := create.Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, ErrUserExists
	}
	return created, err
}

// Serialize changes to the active administrator set before locking any users.
func lockAdministratorState(ctx context.Context, tx *ent.Client, actorID, targetID int) error {
	if err := lockAuthenticationConfiguration(ctx, tx); err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, "UPDATE instance_initializations SET version = version WHERE id = 1")
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInstanceUninitialized
	}
	ids := []int{actorID, targetID}
	sort.Ints(ids)
	for i, id := range ids {
		if i > 0 && id == ids[i-1] {
			continue
		}
		if err := lockAuthUser(ctx, tx, id); err != nil {
			return err
		}
	}
	actor, err := tx.User.Get(ctx, actorID)
	if err != nil {
		return err
	}
	if !actor.Active || actor.Role != SystemRoleAdmin {
		return ErrForbidden
	}
	return nil
}

func (s *AdminService) UpdateUser(ctx context.Context, actorUserID, targetUserID int, input AdminUpdateUserInput) (*ent.User, error) {
	if actorUserID == targetUserID && input.Role != nil {
		return nil, ErrAdminSelfDemotion
	}
	if input.Active != nil && !*input.Active && actorUserID == targetUserID {
		return nil, ErrAdminSelfDeletion
	}
	var email string
	if input.Email != nil {
		var err error
		email, err = accountidentity.NormalizeEmail(*input.Email)
		if err != nil {
			return nil, ErrInvalidInput
		}
	}
	if input.Role != nil && *input.Role != SystemRoleUser && *input.Role != SystemRoleAdmin {
		return nil, ErrInvalidInput
	}
	var result *ent.User
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if err := lockAdministratorState(ctx, tx, actorUserID, targetUserID); err != nil {
			return err
		}
		target, err := tx.User.Get(ctx, targetUserID)
		if err != nil {
			return err
		}
		if input.Email != nil && (target.EmailVerifiedAt != nil || target.VerifiedEmailKey != "" || target.EmailVerificationSource != "") {
			return ErrAccountConflict
		}
		losesAdmin := target.Active && target.Role == SystemRoleAdmin && ((input.Role != nil && *input.Role != SystemRoleAdmin) || (input.Active != nil && !*input.Active))
		if losesAdmin {
			count, err := tx.User.Query().Where(user.RoleEQ(SystemRoleAdmin), user.ActiveEQ(true)).Count(ctx)
			if err != nil {
				return err
			}
			if count <= 1 {
				return ErrLastAdmin
			}
		}
		update := tx.User.UpdateOneID(targetUserID)
		if input.DisplayName != nil {
			update.SetDisplayName(strings.TrimSpace(*input.DisplayName))
		}
		if input.Email != nil {
			if email == "" {
				update.ClearEmail()
			} else {
				update.SetEmail(email)
			}
		}
		if input.Role != nil {
			update.SetRole(*input.Role)
		}
		if input.Active != nil {
			if !*input.Active && target.Active {
				if err := revokeUserSessions(ctx, tx, targetUserID, timeutil.NowUTC()); err != nil {
					return err
				}
			}
			update.SetActive(*input.Active)
		}
		result, err = update.Save(ctx)
		if err != nil {
			return err
		}
		return recordAuditEvent(ctx, tx, AuditEvent{ActorUserID: actorUserID, Action: "admin.account.updated", ResourceType: "user", ResourceID: targetUserID,
			Metadata: map[string]any{"auth_version": result.AuthVersion, "active": result.Active}})
	})
	if err != nil {
		return nil, err
	}
	return result.Unwrap(), nil
}

func (s *AdminService) DisableUser(ctx context.Context, actorUserID, targetUserID int) error {
	active := false
	_, err := s.UpdateUser(ctx, actorUserID, targetUserID, AdminUpdateUserInput{Active: &active})
	return err
}

func (s *AdminService) ResetPassword(ctx context.Context, actorUserID, userID int, newPassword string) error {
	if err := validateNewPassword(newPassword); err != nil {
		return err
	}
	passwordHash, err := hashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if err := lockAdministratorState(ctx, tx, actorUserID, userID); err != nil {
			return err
		}
		if err := revokeUserSessions(ctx, tx, userID, timeutil.NowUTC()); err != nil {
			return err
		}
		if err := tx.User.UpdateOneID(userID).SetPasswordHash(passwordHash).Exec(ctx); err != nil {
			return err
		}
		return recordAuditEvent(ctx, tx, AuditEvent{ActorUserID: actorUserID, Action: "admin.account.password_reset", ResourceType: "user", ResourceID: userID})
	})
}

func (s *AdminService) GetSystemStats(ctx context.Context) (*SystemStats, error) {
	totalUsers, err := s.client.User.Query().Count(ctx)
	if err != nil {
		return nil, err
	}
	activeUsers, err := s.client.User.Query().Where(user.ActiveEQ(true)).Count(ctx)
	if err != nil {
		return nil, err
	}
	totalProjects, err := s.client.Project.Query().Count(ctx)
	if err != nil {
		return nil, err
	}
	totalOrgs, err := s.client.Organization.Query().Count(ctx)
	if err != nil {
		return nil, err
	}
	totalJobs, err := s.client.Job.Query().Count(ctx)
	if err != nil {
		return nil, err
	}
	totalResources, err := s.client.Resource.Query().Count(ctx)
	if err != nil {
		return nil, err
	}

	return &SystemStats{
		TotalUsers:         totalUsers,
		ActiveUsers:        activeUsers,
		TotalProjects:      totalProjects,
		TotalOrganizations: totalOrgs,
		TotalJobs:          totalJobs,
		TotalResources:     totalResources,
	}, nil
}

func (s *AdminService) ListAuditLogs(ctx context.Context, params ListAuditLogsParams) (*PaginatedAuditLogs, error) {
	if params.Limit <= 0 || params.Limit > 100 {
		params.Limit = 50
	}

	query := s.client.ActivityLog.Query()

	if params.Cursor > 0 {
		query = query.Where(activitylog.IDLT(params.Cursor))
	}

	total, err := s.client.ActivityLog.Query().Count(ctx)
	if err != nil {
		return nil, err
	}

	items, err := query.
		Order(ent.Desc(activitylog.FieldID)).
		Limit(params.Limit).
		WithActor().
		WithOrganization().
		WithProject().
		All(ctx)
	if err != nil {
		return nil, err
	}

	return &PaginatedAuditLogs{Items: items, Total: total}, nil
}
