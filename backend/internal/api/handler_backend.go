package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type createBackendRequest struct {
	Name               string         `json:"name"`
	Type               string         `json:"type"`
	Options            BackendOptions `json:"options"`
	RateLimitPerMinute *int           `json:"rate_limit_per_minute"`
}

type updateBackendRequest struct {
	Name               string         `json:"name"`
	Type               string         `json:"type"`
	Options            BackendOptions `json:"options"`
	RateLimitPerMinute *int           `json:"rate_limit_per_minute"`
}

// backendOptionsToMap 将 BackendOptions 转换为 service 层需要的 map[string]any 格式。
func backendOptionsToMap(opts BackendOptions) (map[string]any, error) {
	if len(opts.union) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(opts.union, &m); err != nil {
		return nil, err
	}
	return m, nil
}

type backendResponse struct {
	ID                 int            `json:"id"`
	Scope              string         `json:"scope"`
	Name               string         `json:"name"`
	Type               string         `json:"type"`
	Options            map[string]any `json:"options,omitempty"`
	RateLimitPerMinute int            `json:"rate_limit_per_minute"`
	OwnerUserID        *int           `json:"owner_user_id,omitempty"`
	OwnerOrgID         *int           `json:"owner_org_id,omitempty"`
}

func toBackendResponse(record *service.BackendRecord, showOptions bool) backendResponse {
	resp := backendResponse{
		ID:                 record.ID,
		Scope:              record.Scope,
		Name:               record.Name,
		Type:               record.Type,
		RateLimitPerMinute: record.RateLimitPerMinute,
		OwnerUserID:        record.OwnerUserID,
		OwnerOrgID:         record.OwnerOrgID,
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
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	var req createBackendRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	optionsMap, err := backendOptionsToMap(req.Options)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "options 格式无效")
		return
	}
	userID := authUser.User.ID
	input := service.CreateBackendInput{
		Scope:       service.ScopeUser,
		OwnerUserID: &userID,
		BackendInput: service.BackendInput{
			Name:    req.Name,
			Type:    req.Type,
			Options: optionsMap,
		},
	}
	if req.RateLimitPerMinute != nil {
		input.RateLimitPerMinute = *req.RateLimitPerMinute
	}
	record, err := s.backendSvc.Create(r.Context(), input)
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toBackendResponse(record, true))
}

func (s *Server) handleListUserBackends(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	records, err := s.backendSvc.List(r.Context(), service.ScopeUser, authUser.User.ID)
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackendListResponse(records, true))
}

func (s *Server) handleUpdateUserBackend(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	backendID, ok := s.parseIntParam(w, r, chi.URLParam(r, "backendId"), "backendId")
	if !ok {
		return
	}
	var req updateBackendRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	optionsMap, err := backendOptionsToMap(req.Options)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "options 格式无效")
		return
	}
	input := service.BackendInput{
		Name:    req.Name,
		Type:    req.Type,
		Options: optionsMap,
	}
	if req.RateLimitPerMinute != nil {
		input.RateLimitPerMinute = *req.RateLimitPerMinute
	}
	record, err := s.backendSvc.Update(r.Context(), authUser.User.ID, backendID, input)
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackendResponse(record, true))
}

func (s *Server) handleDeleteUserBackend(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	backendID, ok := s.parseIntParam(w, r, chi.URLParam(r, "backendId"), "backendId")
	if !ok {
		return
	}
	if err := s.backendSvc.Delete(r.Context(), authUser.User.ID, backendID); err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateOrgBackend(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	orgID, ok := s.parseIntParam(w, r, chi.URLParam(r, "orgId"), "orgId")
	if !ok {
		return
	}
	// handler 层负责验证组织管理员权限
	if _, err := s.userService.RequireMembership(r.Context(), authUser.User.ID, orgID, service.OrgRoleAdmin); err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	var req createBackendRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	optionsMap, err := backendOptionsToMap(req.Options)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "options 格式无效")
		return
	}
	input := service.CreateBackendInput{
		Scope:      service.ScopeOrg,
		OwnerOrgID: &orgID,
		BackendInput: service.BackendInput{
			Name:    req.Name,
			Type:    req.Type,
			Options: optionsMap,
		},
	}
	if req.RateLimitPerMinute != nil {
		input.RateLimitPerMinute = *req.RateLimitPerMinute
	}
	record, err := s.backendSvc.Create(r.Context(), input)
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toBackendResponse(record, true))
}

func (s *Server) handleListOrgBackends(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	orgID, ok := s.parseIntParam(w, r, chi.URLParam(r, "orgId"), "orgId")
	if !ok {
		return
	}
	// handler 层负责验证组织成员权限，并根据角色决定是否返回 options
	membership, err := s.userService.RequireMembership(r.Context(), authUser.User.ID, orgID, service.OrgRoleMember)
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	showOptions := membership.Role == service.OrgRoleAdmin || membership.Role == service.OrgRoleOwner
	records, err := s.backendSvc.List(r.Context(), service.ScopeOrg, orgID)
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackendListResponse(records, showOptions))
}

func (s *Server) handleUpdateOrgBackend(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	backendID, ok := s.parseIntParam(w, r, chi.URLParam(r, "backendId"), "backendId")
	if !ok {
		return
	}
	var req updateBackendRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	optionsMap, err := backendOptionsToMap(req.Options)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "options 格式无效")
		return
	}
	// Update 内部通过 requireOwnership 验证 org 管理员权限
	input := service.BackendInput{
		Name:    req.Name,
		Type:    req.Type,
		Options: optionsMap,
	}
	if req.RateLimitPerMinute != nil {
		input.RateLimitPerMinute = *req.RateLimitPerMinute
	}
	record, err := s.backendSvc.Update(r.Context(), authUser.User.ID, backendID, input)
	if err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBackendResponse(record, true))
}

func (s *Server) handleDeleteOrgBackend(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	backendID, ok := s.parseIntParam(w, r, chi.URLParam(r, "backendId"), "backendId")
	if !ok {
		return
	}
	// Delete 内部通过 requireOwnership 验证 org 管理员权限
	if err := s.backendSvc.Delete(r.Context(), authUser.User.ID, backendID); err != nil {
		s.writeBackendServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeBackendServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrBackendNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "后端不存在")
	case errors.Is(err, service.ErrBackendExists):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "后端已存在")
	case errors.Is(err, service.ErrBackendTypeInvalid):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "后端类型无效")
	case errors.Is(err, service.ErrBackendSourceInvalid):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "后端来源无效")
	case errors.Is(err, service.ErrInvalidInput):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求参数不合法")
	default:
		s.writeServiceError(w, r, err)
	}
}

type listBackendModelsRequest struct {
	Type    string `json:"type"`
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
}

func (s *Server) handleListBackendModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := authUserFromContext(r.Context()); !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	var req listBackendModelsRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.APIKey) == "" {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "api_key 不能为空")
		return
	}
	opts := map[string]any{"api_key": strings.TrimSpace(req.APIKey)}
	if strings.TrimSpace(req.BaseURL) != "" {
		opts["base_url"] = strings.TrimSpace(req.BaseURL)
	}
	models, err := s.backendSvc.ListModels(r.Context(), req.Type, opts)
	if err != nil {
		s.writeBackendModelListError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": toModelItems(models)})
}

func toModelItems(models []backend.ModelInfo) []map[string]string {
	items := make([]map[string]string, 0, len(models))
	for _, m := range models {
		items = append(items, map[string]string{"id": m.ID, "name": m.Name})
	}
	return items
}

func (s *Server) writeBackendModelListError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrBackendTypeInvalid),
		errors.Is(err, backend.ErrUnknownBackendType):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "后端类型无效")
	case errors.Is(err, service.ErrInvalidInput):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求参数不合法")
	default:
		var se *backend.StatusError
		if errors.As(err, &se) {
			code := se.HTTPStatus()
			// 将上游 4xx 错误统一映射为 400，避免与前端全局鉴权 401 拦截器冲突
			// 同时在响应体中保留原始状态码和错误信息
			if code >= 400 && code < 500 {
				msg := fmt.Sprintf("拉取模型列表失败 (上游返回 %d: %s)", code, se.Err.Error())
				s.writeProblem(w, r, http.StatusBadRequest, "upstream_error", msg)
				return
			}
			s.writeProblem(w, r, http.StatusBadGateway, "bad_gateway", "拉取模型列表失败")
			return
		}
		s.writeProblem(w, r, http.StatusBadGateway, "bad_gateway", "拉取模型列表失败")
	}
}
