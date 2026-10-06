package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

type adminCreateUserRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type adminUpdateUserRequest struct {
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
	Role        *string `json:"role"`
	Active      *bool   `json:"active"`
}

type adminResetPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

type adminUserListResponse struct {
	Items []userResponse `json:"items"`
	Total int            `json:"total"`
}

type systemStatsResponse struct {
	TotalUsers         int `json:"total_users"`
	ActiveUsers        int `json:"active_users"`
	TotalProjects      int `json:"total_projects"`
	TotalOrganizations int `json:"total_organizations"`
	TotalJobs          int `json:"total_jobs"`
	TotalResources     int `json:"total_resources"`
}

type adminAuditLogItem struct {
	ID           int            `json:"id"`
	ActorID      *int           `json:"actor_id,omitempty"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   *int           `json:"resource_id,omitempty"`
	Message      string         `json:"message,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	CreatedAt    string         `json:"created_at"`
}

type adminAuditLogListResponse struct {
	Items []adminAuditLogItem `json:"items"`
	Total int                 `json:"total"`
}

type systemSettingsResponse struct {
	Settings service.SystemSettings `json:"settings"`
}

type updateSystemSettingsRequest struct {
	Settings json.RawMessage `json:"settings"`
}

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	_ = authUser

	params := service.ListUsersParams{
		Search: r.URL.Query().Get("search"),
		Role:   r.URL.Query().Get("role"),
	}
	if activeStr := r.URL.Query().Get("active"); activeStr != "" {
		active := activeStr == "true"
		params.Active = &active
	}
	if cursorStr := r.URL.Query().Get("cursor"); cursorStr != "" {
		if v, err := strconv.Atoi(cursorStr); err == nil {
			params.Cursor = v
		}
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil {
			params.Limit = v
		}
	}

	result, err := s.adminService.ListUsers(r.Context(), params)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}

	items := make([]userResponse, 0, len(result.Items))
	for _, u := range result.Items {
		items = append(items, toUserResponse(u))
	}
	writeJSON(w, http.StatusOK, adminUserListResponse{Items: items, Total: result.Total})
}

func (s *Server) handleAdminGetUser(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.parseIntParam(w, r, chi.URLParam(r, "userId"), "userId")
	if !ok {
		return
	}
	u, err := s.adminService.GetUser(r.Context(), userID)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req adminCreateUserRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	u, err := s.adminService.CreateUser(r.Context(), service.AdminCreateUserInput{
		Username:    req.Username,
		Password:    req.Password,
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Role:        req.Role,
	})
	if err != nil {
		s.writeAdminServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUserResponse(u))
}

func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	userID, ok := s.parseIntParam(w, r, chi.URLParam(r, "userId"), "userId")
	if !ok {
		return
	}
	var req adminUpdateUserRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	u, err := s.adminService.UpdateUser(r.Context(), authUser.User.ID, userID, service.AdminUpdateUserInput{
		DisplayName: req.DisplayName,
		Email:       req.Email,
		Role:        req.Role,
		Active:      req.Active,
	})
	if err != nil {
		s.writeAdminServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

func (s *Server) handleAdminDisableUser(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	userID, ok := s.parseIntParam(w, r, chi.URLParam(r, "userId"), "userId")
	if !ok {
		return
	}
	if err := s.adminService.DisableUser(r.Context(), authUser.User.ID, userID); err != nil {
		s.writeAdminServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.parseIntParam(w, r, chi.URLParam(r, "userId"), "userId")
	if !ok {
		return
	}
	var req adminResetPasswordRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if err := s.adminService.ResetPassword(r.Context(), userID, req.NewPassword); err != nil {
		s.writeAdminServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminGetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.adminService.GetSystemStats(r.Context())
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, systemStatsResponse{
		TotalUsers:         stats.TotalUsers,
		ActiveUsers:        stats.ActiveUsers,
		TotalProjects:      stats.TotalProjects,
		TotalOrganizations: stats.TotalOrganizations,
		TotalJobs:          stats.TotalJobs,
		TotalResources:     stats.TotalResources,
	})
}

func (s *Server) handleAdminListAuditLogs(w http.ResponseWriter, r *http.Request) {
	params := service.ListAuditLogsParams{}
	if cursorStr := r.URL.Query().Get("cursor"); cursorStr != "" {
		if v, err := strconv.Atoi(cursorStr); err == nil {
			params.Cursor = v
		}
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil {
			params.Limit = v
		}
	}

	result, err := s.adminService.ListAuditLogs(r.Context(), params)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}

	items := make([]adminAuditLogItem, 0, len(result.Items))
	for _, log := range result.Items {
		items = append(items, toAdminAuditLogItem(log))
	}
	writeJSON(w, http.StatusOK, adminAuditLogListResponse{Items: items, Total: result.Total})
}

func toAdminAuditLogItem(log *ent.ActivityLog) adminAuditLogItem {
	item := adminAuditLogItem{
		ID:           log.ID,
		Action:       log.Action,
		ResourceType: log.ResourceType,
		ResourceID:   log.ResourceID,
		Message:      log.Message,
		Metadata:     log.Metadata,
		CreatedAt:    timeutil.Format(log.CreatedAt),
	}
	if log.Edges.Actor != nil {
		actorID := log.Edges.Actor.ID
		item.ActorID = &actorID
	}
	return item
}

func (s *Server) handleAdminGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.settingsService.Get(r.Context())
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, systemSettingsResponse{Settings: settings})
}

func (s *Server) handleAdminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	var req updateSystemSettingsRequest
	if !s.decodeStrictBody(w, r, &req) {
		return
	}
	var fields struct {
		RegistrationEnabled json.RawMessage `json:"registration_enabled"`
		TaskRetention       json.RawMessage `json:"task_retention"`
	}
	if err := decodeStrictObject(req.Settings, &fields); err != nil || len(fields.RegistrationEnabled) == 0 && len(fields.TaskRetention) == 0 {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "settings 必须包含受支持的配置字段")
		return
	}
	patch := service.SettingsPatch{}
	if len(fields.RegistrationEnabled) > 0 {
		var enabled *bool
		if err := json.Unmarshal(fields.RegistrationEnabled, &enabled); err != nil || enabled == nil {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "registration_enabled 必须是布尔值")
			return
		}
		patch.RegistrationEnabled = enabled
	}
	if len(fields.TaskRetention) > 0 {
		var policy struct {
			Enabled          *bool  `json:"enabled"`
			RetentionDays    *int   `json:"retention_days"`
			ExpectedRevision *int64 `json:"expected_revision"`
		}
		if err := decodeStrictObject(fields.TaskRetention, &policy); err != nil || policy.Enabled == nil || policy.RetentionDays == nil || policy.ExpectedRevision == nil {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "task_retention 必须完整包含 enabled、retention_days 和 expected_revision")
			return
		}
		patch.TaskRetention = &service.TaskRetentionPatch{Enabled: *policy.Enabled, RetentionDays: *policy.RetentionDays, ExpectedRevision: *policy.ExpectedRevision}
	}
	settings, err := s.settingsService.Patch(r.Context(), authUser.User.ID, patch)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, systemSettingsResponse{Settings: settings})
}

func (s *Server) writeAdminServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrAdminSelfDemotion):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "管理员不能修改自己的角色")
	case errors.Is(err, service.ErrAdminSelfDeletion):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "管理员不能停用自己的账户")
	case errors.Is(err, service.ErrLastAdmin):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "不能移除最后一个活跃管理员")
	case errors.Is(err, service.ErrRegistrationClosed):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "注册已关闭")
	default:
		s.writeServiceError(w, r, err)
	}
}
