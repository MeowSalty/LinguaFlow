package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
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
	TotalUsers           int `json:"total_users"`
	ActiveUsers          int `json:"active_users"`
	TotalProjects        int `json:"total_projects"`
	TotalOrganizations   int `json:"total_organizations"`
	TotalTranslationJobs int `json:"total_translation_jobs"`
	TotalResources       int `json:"total_resources"`
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
	Settings map[string]string `json:"settings"`
}

type updateSystemSettingsRequest struct {
	Settings map[string]string `json:"settings"`
}

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
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
		writeServiceError(w, err)
		return
	}

	items := make([]userResponse, 0, len(result.Items))
	for _, u := range result.Items {
		items = append(items, toUserResponse(u))
	}
	writeJSON(w, http.StatusOK, adminUserListResponse{Items: items, Total: result.Total})
}

func (s *Server) handleAdminGetUser(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseIntParam(w, chi.URLParam(r, "userId"), "userId")
	if !ok {
		return
	}
	u, err := s.adminService.GetUser(r.Context(), userID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req adminCreateUserRequest
	if !decodeJSON(w, r, &req) {
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
		writeAdminServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUserResponse(u))
}

func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	userID, ok := parseIntParam(w, chi.URLParam(r, "userId"), "userId")
	if !ok {
		return
	}
	var req adminUpdateUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := s.adminService.UpdateUser(r.Context(), authUser.User.ID, userID, service.AdminUpdateUserInput{
		DisplayName: req.DisplayName,
		Email:       req.Email,
		Role:        req.Role,
		Active:      req.Active,
	})
	if err != nil {
		writeAdminServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

func (s *Server) handleAdminDisableUser(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	userID, ok := parseIntParam(w, chi.URLParam(r, "userId"), "userId")
	if !ok {
		return
	}
	if err := s.adminService.DisableUser(r.Context(), authUser.User.ID, userID); err != nil {
		writeAdminServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseIntParam(w, chi.URLParam(r, "userId"), "userId")
	if !ok {
		return
	}
	var req adminResetPasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.adminService.ResetPassword(r.Context(), userID, req.NewPassword); err != nil {
		writeAdminServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminGetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.adminService.GetSystemStats(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, systemStatsResponse{
		TotalUsers:           stats.TotalUsers,
		ActiveUsers:          stats.ActiveUsers,
		TotalProjects:        stats.TotalProjects,
		TotalOrganizations:   stats.TotalOrganizations,
		TotalTranslationJobs: stats.TotalTranslationJobs,
		TotalResources:       stats.TotalResources,
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
		writeServiceError(w, err)
		return
	}

	items := make([]adminAuditLogItem, 0, len(result.Items))
	for _, log := range result.Items {
		item := adminAuditLogItem{
			ID:           log.ID,
			Action:       log.Action,
			ResourceType: log.ResourceType,
			Message:      log.Message,
			Metadata:     log.Metadata,
			CreatedAt:    log.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if log.ResourceID != nil {
			item.ResourceID = log.ResourceID
		}
		if log.Edges.Actor != nil {
			actorID := log.Edges.Actor.ID
			item.ActorID = &actorID
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, adminAuditLogListResponse{Items: items, Total: result.Total})
}

func (s *Server) handleAdminGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.adminService.GetSettings(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, systemSettingsResponse{Settings: settings})
}

func (s *Server) handleAdminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req updateSystemSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.adminService.UpdateSettings(r.Context(), req.Settings); err != nil {
		writeServiceError(w, err)
		return
	}
	settings, err := s.adminService.GetSettings(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, systemSettingsResponse{Settings: settings})
}

func writeAdminServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrAdminSelfDemotion):
		writeProblem(w, http.StatusConflict, "conflict", "管理员不能修改自己的角色")
	case errors.Is(err, service.ErrAdminSelfDeletion):
		writeProblem(w, http.StatusConflict, "conflict", "管理员不能停用自己的账户")
	case errors.Is(err, service.ErrLastAdmin):
		writeProblem(w, http.StatusConflict, "conflict", "不能移除最后一个活跃管理员")
	case errors.Is(err, service.ErrRegistrationClosed):
		writeProblem(w, http.StatusForbidden, "forbidden", "注册已关闭")
	default:
		writeServiceError(w, err)
	}
}
