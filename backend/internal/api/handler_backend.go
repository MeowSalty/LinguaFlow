package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type createBackendRequest struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Priority int            `json:"priority"`
	Options  map[string]any `json:"options"`
}

type updateBackendRequest struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Priority int            `json:"priority"`
	Options  map[string]any `json:"options"`
}

type backendResponse struct {
	ID          int            `json:"id"`
	Scope       string         `json:"scope"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Priority    int            `json:"priority"`
	Options     map[string]any `json:"options,omitempty"`
	OwnerUserID *int           `json:"owner_user_id,omitempty"`
	OwnerOrgID  *int           `json:"owner_org_id,omitempty"`
}

func toBackendResponse(record *service.BackendRecord, showOptions bool) backendResponse {
	resp := backendResponse{
		ID:          record.ID,
		Scope:       record.Scope,
		Name:        record.Name,
		Type:        record.Type,
		Priority:    record.Priority,
		OwnerUserID: record.OwnerUserID,
		OwnerOrgID:  record.OwnerOrgID,
	}
	if showOptions && record.Options != nil {
		resp.Options = record.Options
	}
	return resp
}

func toBackendListResponse(records []*service.BackendRecord, showOptions bool) map[string]any {
	items := make([]backendResponse, 0, len(records))
	for _, r := range records {
		items = append(items, toBackendResponse(r, showOptions))
	}
	return map[string]any{"items": items}
}

func (s *Server) handleCreateUserBackend(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	var req createBackendRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	userID := authUser.User.ID
	record, err := s.backendSvc.Create(r.Context(), service.CreateBackendInput{
		Scope:       service.ScopeUser,
		OwnerUserID: &userID,
		BackendInput: service.BackendInput{
			Name:     req.Name,
			Type:     req.Type,
			Priority: req.Priority,
			Options:  req.Options,
		},
	})
	if err != nil {
		writeBackendServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toBackendResponse(record, true))
}

func (s *Server) handleListUserBackends(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	records, err := s.backendSvc.List(r.Context(), service.ScopeUser, authUser.User.ID)
	if err != nil {
		writeBackendServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackendListResponse(records, true))
}

func (s *Server) handleUpdateUserBackend(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	backendID, ok := parseIntParam(w, chi.URLParam(r, "backendId"), "backendId")
	if !ok {
		return
	}
	var req updateBackendRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	record, err := s.backendSvc.Update(r.Context(), authUser.User.ID, backendID, service.BackendInput{
		Name:     req.Name,
		Type:     req.Type,
		Priority: req.Priority,
		Options:  req.Options,
	})
	if err != nil {
		writeBackendServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackendResponse(record, true))
}

func (s *Server) handleDeleteUserBackend(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	backendID, ok := parseIntParam(w, chi.URLParam(r, "backendId"), "backendId")
	if !ok {
		return
	}
	if err := s.backendSvc.Delete(r.Context(), authUser.User.ID, backendID); err != nil {
		writeBackendServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateOrgBackend(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	orgID, ok := parseIntParam(w, chi.URLParam(r, "orgId"), "orgId")
	if !ok {
		return
	}
	// handler 层负责验证组织管理员权限
	if _, err := s.userService.RequireMembership(r.Context(), authUser.User.ID, orgID, service.OrgRoleAdmin); err != nil {
		writeBackendServiceError(w, err)
		return
	}
	var req createBackendRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	record, err := s.backendSvc.Create(r.Context(), service.CreateBackendInput{
		Scope:      service.ScopeOrg,
		OwnerOrgID: &orgID,
		BackendInput: service.BackendInput{
			Name:     req.Name,
			Type:     req.Type,
			Priority: req.Priority,
			Options:  req.Options,
		},
	})
	if err != nil {
		writeBackendServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toBackendResponse(record, true))
}

func (s *Server) handleListOrgBackends(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	orgID, ok := parseIntParam(w, chi.URLParam(r, "orgId"), "orgId")
	if !ok {
		return
	}
	// handler 层负责验证组织成员权限，并根据角色决定是否返回 options
	membership, err := s.userService.RequireMembership(r.Context(), authUser.User.ID, orgID, service.OrgRoleMember)
	if err != nil {
		writeBackendServiceError(w, err)
		return
	}
	showOptions := membership.Role == service.OrgRoleAdmin || membership.Role == service.OrgRoleOwner
	records, err := s.backendSvc.List(r.Context(), service.ScopeOrg, orgID)
	if err != nil {
		writeBackendServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackendListResponse(records, showOptions))
}

func (s *Server) handleUpdateOrgBackend(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	backendID, ok := parseIntParam(w, chi.URLParam(r, "backendId"), "backendId")
	if !ok {
		return
	}
	var req updateBackendRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// Update 内部通过 requireOwnership 验证 org 管理员权限
	record, err := s.backendSvc.Update(r.Context(), authUser.User.ID, backendID, service.BackendInput{
		Name:     req.Name,
		Type:     req.Type,
		Priority: req.Priority,
		Options:  req.Options,
	})
	if err != nil {
		writeBackendServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackendResponse(record, true))
}

func (s *Server) handleDeleteOrgBackend(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	backendID, ok := parseIntParam(w, chi.URLParam(r, "backendId"), "backendId")
	if !ok {
		return
	}
	// Delete 内部通过 requireOwnership 验证 org 管理员权限
	if err := s.backendSvc.Delete(r.Context(), authUser.User.ID, backendID); err != nil {
		writeBackendServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeBackendServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		writeProblem(w, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrBackendNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "后端不存在")
	case errors.Is(err, service.ErrBackendExists):
		writeProblem(w, http.StatusConflict, "conflict", "后端已存在")
	case errors.Is(err, service.ErrBackendTypeInvalid):
		writeProblem(w, http.StatusBadRequest, "invalid_input", "后端类型无效")
	case errors.Is(err, service.ErrBackendSourceInvalid):
		writeProblem(w, http.StatusBadRequest, "invalid_input", "后端来源无效")
	case errors.Is(err, service.ErrInvalidInput):
		writeProblem(w, http.StatusBadRequest, "invalid_input", "请求参数不合法")
	default:
		writeServiceError(w, err)
	}
}
