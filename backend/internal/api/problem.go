package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type problemDetails struct {
	Type   string `json:"type,omitempty"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON")
		return false
	}
	return true
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemDetails{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
	})
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		writeProblem(w, http.StatusBadRequest, "invalid_input", "请求参数不合法")
	case errors.Is(err, service.ErrInvalidCredentials), errors.Is(err, service.ErrTokenInvalid), errors.Is(err, service.ErrTokenExpired), errors.Is(err, service.ErrRefreshTokenRevoked), errors.Is(err, service.ErrUserInactive):
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
	case errors.Is(err, service.ErrUserExists):
		writeProblem(w, http.StatusConflict, "conflict", "用户已存在")
	case errors.Is(err, service.ErrForbidden):
		writeProblem(w, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrProjectNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "资源不存在")
	default:
		writeProblem(w, http.StatusInternalServerError, "internal_error", "服务器内部错误")
	}
}

func writeProjectServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		writeProblem(w, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrProjectNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "项目不存在")
	case errors.Is(err, service.ErrInvalidInput),
		errors.Is(err, service.ErrProjectOwnerConflict),
		errors.Is(err, service.ErrResourceScopeInvalid),
		errors.Is(err, service.ErrBackendSourceInvalid),
		errors.Is(err, service.ErrBackendNameAmbiguous):
		writeProblem(w, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, service.ErrBackendNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, service.ErrBackendExists):
		writeProblem(w, http.StatusConflict, "conflict", "后端已存在")
	case errors.Is(err, service.ErrBackendTypeInvalid),
		errors.Is(err, service.ErrBackendSourceInvalid):
		writeProblem(w, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, service.ErrGlossaryEntryNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "术语条目不存在")
	case errors.Is(err, service.ErrGlossaryEntryExists):
		writeProblem(w, http.StatusConflict, "conflict", "术语条目已存在")
	default:
		writeServiceError(w, err)
	}
}
