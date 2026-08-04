package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type problemDetails struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

func (s *Server) writeProblem(w http.ResponseWriter, r *http.Request, status int, title, detail string, extraAttrs ...slog.Attr) {
	requestID := chimiddleware.GetReqID(r.Context())

	level := slog.LevelDebug
	msg := "client error"
	if status >= 500 {
		level = slog.LevelError
		msg = "server error"
	}
	if s.logger.Enabled(r.Context(), level) {
		attrs := []slog.Attr{
			slog.String("request_id", requestID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.String("title", title),
			slog.String("detail", detail),
		}
		attrs = append(attrs, extraAttrs...)
		s.logger.LogAttrs(r.Context(), level, msg, attrs...)
	}

	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemDetails{
		Type:     "about:blank",
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: requestID,
	})
}

func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON")
		return false
	}
	return true
}

func (s *Server) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求参数不合法")
	case errors.Is(err, service.ErrInvalidCredentials),
		errors.Is(err, service.ErrTokenInvalid),
		errors.Is(err, service.ErrTokenExpired),
		errors.Is(err, service.ErrRefreshTokenRevoked),
		errors.Is(err, service.ErrUserInactive):
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
	case errors.Is(err, service.ErrUserExists):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "用户已存在")
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrRegistrationClosed):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "注册已关闭")
	case errors.Is(err, service.ErrProjectNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "资源不存在")
	default:
		s.writeProblem(w, r, http.StatusInternalServerError, "internal_error", "服务器内部错误",
			slog.String("error", err.Error()),
			slog.Any("error_type", reflect.TypeOf(err)),
		)
	}
}

func (s *Server) writeProjectServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrProjectNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "项目不存在")
	case errors.Is(err, service.ErrInvalidInput),
		errors.Is(err, service.ErrProjectOwnerConflict),
		errors.Is(err, service.ErrBackendSourceInvalid),
		errors.Is(err, service.ErrBackendNameAmbiguous):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, service.ErrBackendNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, service.ErrBackendExists):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "后端已存在")
	case errors.Is(err, service.ErrBackendTypeInvalid):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, service.ErrGlossaryEntryNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语条目不存在")
	case errors.Is(err, service.ErrGlossaryEntryExists):
		s.writeProblem(w, r, http.StatusConflict, "conflict", "术语条目已存在")
	default:
		s.writeServiceError(w, r, err)
	}
}

func (s *Server) parseIntParam(w http.ResponseWriter, r *http.Request, raw string, name string) (int, bool) {
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_path_parameter", name+" 必须是正整数")
		return 0, false
	}
	return v, true
}
