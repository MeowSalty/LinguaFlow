package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// ---- 辅助函数 ----

// parseSharedOrgQuery rejects ambiguous filtering before the service queries data.
func (s *Server) parseSharedOrgQuery(w http.ResponseWriter, r *http.Request) (*int, bool) {
	if !s.validateJobQueryParameters(w, r, "org_id") {
		return nil, false
	}
	values, present := r.URL.Query()["org_id"]
	if !present {
		return nil, true
	}
	id, err := strconv.Atoi(values[0])
	if err != nil || id <= 0 {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "org_id 必须是正整数")
		return nil, false
	}
	return &id, true
}

// decodeSharedJSON distinguishes omitted organization scope from explicit null.
// The typed decoder also rejects client supplied ownership and scope fields.
func (s *Server) decodeSharedJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体必须只有一个 JSON 对象")
		return false
	}
	var fields map[string]json.RawMessage
	if err := validateSharedJSONFields(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体含有重复字段或无效 JSON")
		return false
	}
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体必须是 JSON 对象")
		return false
	}
	// encoding/json matches struct fields case-insensitively. Enforce the exact
	// OpenAPI field names first so an alias cannot overwrite a validated org_id.
	typ := reflect.TypeOf(dst)
	if typ.Kind() == reflect.Pointer && typ.Elem().Kind() == reflect.Struct {
		typ = typ.Elem()
		allowed := make(map[string]bool, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			if name != "" && name != "-" {
				allowed[name] = true
			}
		}
		for name := range fields {
			if !allowed[name] {
				s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体含有未知字段")
				return false
			}
		}
	}
	if value, present := fields["org_id"]; present && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "org_id 必须是正整数")
		return false
	}
	typed := json.NewDecoder(bytes.NewReader(raw))
	typed.DisallowUnknownFields()
	if err := typed.Decode(dst); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON")
		return false
	}
	return true
}

func validateSharedJSONFields(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate JSON field")
			}
			seen[name] = true
			if err := validateSharedJSONFields(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := validateSharedJSONFields(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

// parsePromptTemplateID 从路径参数解析 promptTemplateId。
func (s *Server) parsePromptTemplateID(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "translationPromptTemplateId")
	id, err := strconv.Atoi(raw)
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_id", "提示词模板 ID 必须为整数")
		return 0, false
	}
	return id, true
}

// entTranslationPromptTemplateToResponse 将翻译提示词模板转换为 API 响应。
func entTranslationPromptTemplateToResponse(t *ent.TranslationPromptTemplate) TranslationPromptTemplate {
	resp := TranslationPromptTemplate{
		Id:          t.ID,
		Name:        t.Name,
		Description: t.Description,
		Scope:       TranslationPromptTemplateScope(t.Scope),
	}
	if t.SystemPromptContent != "" {
		resp.SystemPromptContent = &t.SystemPromptContent
	}
	if t.OwnerUserID != nil {
		resp.OwnerUserId = t.OwnerUserID
	}
	if t.OwnerOrgID != nil {
		resp.OwnerOrgId = t.OwnerOrgID
	}
	if !t.CreatedAt.IsZero() {
		resp.CreatedAt = timeutil.NormalizePtr(&t.CreatedAt)
	}
	if !t.UpdatedAt.IsZero() {
		resp.UpdatedAt = timeutil.NormalizePtr(&t.UpdatedAt)
	}
	return resp
}

// ---- Handler 方法 ----

// handleListPromptTemplates 列出当前用户的提示词模板。
func (s *Server) handleListPromptTemplates(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	orgID, ok := s.parseSharedOrgQuery(w, r)
	if !ok {
		return
	}
	var templates []*ent.TranslationPromptTemplate
	var err error
	if orgID == nil {
		templates, err = s.translationPromptTemplateSvc.ListByUser(r.Context(), authUser.User.ID)
	} else {
		templates, err = s.translationPromptTemplateSvc.ListByOrg(r.Context(), authUser.User.ID, *orgID)
	}
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}

	items := make([]TranslationPromptTemplate, 0, len(templates))
	for _, t := range templates {
		items = append(items, entTranslationPromptTemplateToResponse(t))
	}

	writeJSON(w, http.StatusOK, TranslationPromptTemplateListResponse{Items: items})
}

// handleCreatePromptTemplate 创建提示词模板。
func (s *Server) handleCreatePromptTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var req CreateTranslationPromptTemplateRequest
	if !s.decodeSharedJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		s.writeProblem(w, r, http.StatusBadRequest, "validation_error", "提示词模板名称不能为空")
		return
	}

	input := service.CreateTranslationPromptTemplateInput{
		Name:  req.Name,
		OrgID: req.OrgId,
	}
	if req.Description != nil {
		input.Description = *req.Description
	}
	if req.SystemPromptContent != nil {
		input.SystemPromptContent = *req.SystemPromptContent
	}

	pt, err := s.translationPromptTemplateSvc.Create(r.Context(), authUser.User.ID, input)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entTranslationPromptTemplateToResponse(pt))
}

// handleGetPromptTemplate 获取提示词模板详情。
func (s *Server) handleGetPromptTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	id, ok := s.parsePromptTemplateID(w, r)
	if !ok {
		return
	}

	pt, err := s.translationPromptTemplateSvc.GetByID(r.Context(), authUser.User.ID, id)
	if err != nil {
		if err == service.ErrTranslationPromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "提示词模板不存在")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entTranslationPromptTemplateToResponse(pt))
}

// handleUpdatePromptTemplate 更新提示词模板。
func (s *Server) handleUpdatePromptTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	id, ok := s.parsePromptTemplateID(w, r)
	if !ok {
		return
	}

	var req UpdateTranslationPromptTemplateRequest
	if !s.decodeSharedJSON(w, r, &req) {
		return
	}

	input := service.UpdateTranslationPromptTemplateInput{
		Name:                req.Name,
		Description:         req.Description,
		SystemPromptContent: req.SystemPromptContent,
	}

	pt, err := s.translationPromptTemplateSvc.Update(r.Context(), authUser.User.ID, id, input)
	if err != nil {
		if err == service.ErrTranslationPromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "提示词模板不存在")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entTranslationPromptTemplateToResponse(pt))
}

// handleDeletePromptTemplate 删除提示词模板。
func (s *Server) handleDeletePromptTemplate(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	id, ok := s.parsePromptTemplateID(w, r)
	if !ok {
		return
	}

	err := s.translationPromptTemplateSvc.Delete(r.Context(), authUser.User.ID, id)
	if err != nil {
		if err == service.ErrTranslationPromptTemplateNotFound {
			s.writeProblem(w, r, http.StatusNotFound, "not_found", "提示词模板不存在")
			return
		}
		if errors.Is(err, service.ErrTranslationPromptTemplateInUse) {
			s.writeProblem(w, r, http.StatusConflict, "conflict", "该模板正被执行计划引用，无法删除")
			return
		}
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
