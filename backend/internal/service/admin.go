package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrAdminSelfDemotion  = errors.New("admin cannot change own role")
	ErrAdminSelfDeletion  = errors.New("admin cannot disable self")
	ErrLastAdmin          = errors.New("cannot remove the last active admin")
	ErrRegistrationClosed = errors.New("registration is disabled")
)

const (
	SettingRegistrationEnabled = "registration_enabled"
	SettingDefaultUserRole     = "default_user_role"
	SettingAutoAdmin           = "auto_admin"
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
	username := normalizeIdentity(input.Username)
	email := normalizeIdentity(input.Email)
	if username == "" || email == "" || len(input.Password) < 8 {
		return nil, ErrInvalidInput
	}
	if !strings.Contains(email, "@") {
		return nil, ErrInvalidInput
	}

	role := SystemRoleUser
	if input.Role == SystemRoleAdmin {
		role = SystemRoleAdmin
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	created, err := s.client.User.Create().
		SetUsername(username).
		SetPasswordHash(string(passwordHash)).
		SetEmail(email).
		SetDisplayName(strings.TrimSpace(input.DisplayName)).
		SetRole(role).
		SetActive(true).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	return created, nil
}

func (s *AdminService) UpdateUser(ctx context.Context, actorUserID, targetUserID int, input AdminUpdateUserInput) (*ent.User, error) {
	if actorUserID == targetUserID && input.Role != nil {
		return nil, ErrAdminSelfDemotion
	}

	target, err := s.client.User.Get(ctx, targetUserID)
	if err != nil {
		return nil, err
	}

	if input.Role != nil {
		newRole := strings.ToLower(strings.TrimSpace(*input.Role))
		if newRole != SystemRoleUser && newRole != SystemRoleAdmin {
			return nil, ErrInvalidInput
		}
		if target.Role == SystemRoleAdmin && newRole == SystemRoleUser {
			count, err := s.client.User.Query().Where(user.RoleEQ(SystemRoleAdmin), user.ActiveEQ(true)).Count(ctx)
			if err != nil {
				return nil, err
			}
			if count <= 1 {
				return nil, ErrLastAdmin
			}
		}
	}

	update := s.client.User.UpdateOneID(targetUserID)
	if input.DisplayName != nil {
		update.SetDisplayName(strings.TrimSpace(*input.DisplayName))
	}
	if input.Email != nil {
		email := normalizeIdentity(*input.Email)
		if email != "" && !strings.Contains(email, "@") {
			return nil, ErrInvalidInput
		}
		if email != "" {
			update.SetEmail(email)
		}
	}
	if input.Role != nil {
		update.SetRole(strings.ToLower(strings.TrimSpace(*input.Role)))
	}
	if input.Active != nil {
		if !*input.Active && actorUserID == targetUserID {
			return nil, ErrAdminSelfDeletion
		}
		if !*input.Active && target.Role == SystemRoleAdmin {
			count, err := s.client.User.Query().Where(user.RoleEQ(SystemRoleAdmin), user.ActiveEQ(true)).Count(ctx)
			if err != nil {
				return nil, err
			}
			if count <= 1 {
				return nil, ErrLastAdmin
			}
		}
		update.SetActive(*input.Active)
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

func (s *AdminService) DisableUser(ctx context.Context, actorUserID, targetUserID int) error {
	if actorUserID == targetUserID {
		return ErrAdminSelfDeletion
	}

	target, err := s.client.User.Get(ctx, targetUserID)
	if err != nil {
		return err
	}

	if target.Role == SystemRoleAdmin {
		count, err := s.client.User.Query().Where(user.RoleEQ(SystemRoleAdmin), user.ActiveEQ(true)).Count(ctx)
		if err != nil {
			return err
		}
		if count <= 1 {
			return ErrLastAdmin
		}
	}

	return s.client.User.UpdateOneID(targetUserID).SetActive(false).Exec(ctx)
}

func (s *AdminService) ResetPassword(ctx context.Context, userID int, newPassword string) error {
	if len(newPassword) < 8 {
		return ErrInvalidInput
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.client.User.UpdateOneID(userID).SetPasswordHash(string(passwordHash)).Exec(ctx)
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

func (s *AdminService) GetSettings(ctx context.Context) (map[string]string, error) {
	settings, err := s.client.SystemSetting.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(settings))
	for _, setting := range settings {
		result[setting.Key] = setting.Value
	}
	return result, nil
}

func (s *AdminService) UpdateSettings(ctx context.Context, settings map[string]string) error {
	for key, value := range settings {
		existing, err := s.client.SystemSetting.Query().Where(systemsetting.KeyEQ(key)).Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				_, err = s.client.SystemSetting.Create().
					SetKey(key).
					SetValue(value).
					Save(ctx)
				if err != nil {
					return err
				}
				continue
			}
			return err
		}
		if err := s.client.SystemSetting.UpdateOneID(existing.ID).SetValue(value).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *AdminService) GetSetting(ctx context.Context, key string) (string, error) {
	setting, err := s.client.SystemSetting.Query().Where(systemsetting.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return setting.Value, nil
}

func (s *AdminService) HasAnyAdmin(ctx context.Context) (bool, error) {
	count, err := s.client.User.Query().Where(user.RoleEQ(SystemRoleAdmin)).Count(ctx)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// EnsureAdminRole ensures the given user has admin role.
func (s *AdminService) EnsureAdminRole(ctx context.Context, userID int) error {
	return s.client.User.UpdateOneID(userID).SetRole(SystemRoleAdmin).Exec(ctx)
}

// InitializeSettings seeds default system settings from YAML config values
// when the settings table is empty. Called once at startup.
func (s *AdminService) InitializeSettings(ctx context.Context, defaults map[string]string) error {
	for key, value := range defaults {
		existing, err := s.client.SystemSetting.Query().Where(systemsetting.KeyEQ(key)).Only(ctx)
		if err != nil {
			if !ent.IsNotFound(err) {
				return err
			}
			// Not found — create with default value.
			if _, err := s.client.SystemSetting.Create().SetKey(key).SetValue(value).Save(ctx); err != nil {
				return err
			}
			continue
		}
		_ = existing // Already exists, keep the DB value (admin may have changed it).
	}
	return nil
}

// IsRegistrationEnabled reads the registration_enabled setting from the database.
// Returns true if the setting is missing or set to "true".
func (s *AdminService) IsRegistrationEnabled(ctx context.Context) bool {
	val, err := s.GetSetting(ctx, SettingRegistrationEnabled)
	if err != nil {
		return true // Fail open: allow registration on error.
	}
	if val == "" {
		return true // Default: enabled.
	}
	return val == "true"
}

// ShouldAutoAdmin reads the auto_admin setting from the database.
// Returns true if no admin exists and the setting is "true" or missing.
func (s *AdminService) ShouldAutoAdmin(ctx context.Context) bool {
	hasAdmin, err := s.HasAnyAdmin(ctx)
	if err != nil || hasAdmin {
		return false
	}
	val, err := s.GetSetting(ctx, SettingAutoAdmin)
	if err != nil {
		return true // Fail open: auto-promote on error.
	}
	if val == "" {
		return true // Default: enabled.
	}
	return val == "true"
}
