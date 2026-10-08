package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

const (
	// urnPrefix 是 LinguaFlow RFC 9457 URN type 的统一前缀。
	// 例如: urn:linguaflow:token-expired
	urnPrefix = "urn:linguaflow:"
)

type problemDetails struct {
	ErrorCode   string `json:"error_code,omitempty"`
	TaskID      int    `json:"task_id,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	CheckID     int    `json:"check_id,omitempty"`
	Type        string `json:"type,omitempty"`
	Title       string `json:"title"`
	Status      int    `json:"status"`
	Detail      string `json:"detail,omitempty"`
	Instance    string `json:"instance,omitempty"`
}

type storageProblemIdentityKey struct{}

// urnForTitle 把 Problem title 映射为 RFC 9457 URN 格式的 type。
// 使用 kebab-case: snake_case 的 title 转为 urn:linguaflow:<kebab-case>。
// 未命中映射表时 fallback 为 urn:linguaflow: + kebab(title),保证健壮。
func urnForTitle(title string) string {
	// 快速映射表:覆盖全后端实际使用的 title
	switch title {
	case "unauthorized":
		return urnPrefix + "unauthorized"
	case "forbidden":
		return urnPrefix + "forbidden"
	case "invalid_input":
		return urnPrefix + "invalid-input"
	case "not_found":
		return urnPrefix + "not-found"
	case "conflict":
		return urnPrefix + "conflict"
	case "invalid_query_parameter":
		return urnPrefix + "invalid-query-parameter"
	case "invalid_multipart":
		return urnPrefix + "invalid-multipart"
	case "invalid_upload":
		return urnPrefix + "invalid-upload"
	case "validation_error":
		return urnPrefix + "validation-error"
	case "invalid_id":
		return urnPrefix + "invalid-id"
	case "invalid_path_parameter":
		return urnPrefix + "invalid-path-parameter"
	case "invalid_task_id":
		return urnPrefix + "invalid-task-id"
	case "invalid_scope":
		return urnPrefix + "invalid-scope"
	case "invalid_config":
		return urnPrefix + "invalid-config"
	case "invalid_resource_path":
		return urnPrefix + "invalid-resource-path"
	case "invalid_request":
		return urnPrefix + "invalid-request"
	case "upstream_error":
		return urnPrefix + "upstream-error"
	case "unsupported_format":
		return urnPrefix + "unsupported-format"
	case "parse_failed":
		return urnPrefix + "parse-failed"
	case "bad_gateway":
		return urnPrefix + "bad-gateway"
	case "gateway_timeout":
		return urnPrefix + "gateway-timeout"
	case "quick_translate_timeout":
		return urnPrefix + "quick-translate-timeout"
	case "quick_translate_busy":
		return urnPrefix + "quick-translate-busy"
	case "preview_busy":
		return urnPrefix + "preview-busy"
	case "preview_conflict":
		return urnPrefix + "preview-conflict"
	case "preview_token_expired":
		return urnPrefix + "preview-token-expired"
	case "preview_token_invalid":
		return urnPrefix + "preview-token-invalid"
	case "preview_timeout":
		return urnPrefix + "preview-timeout"
	case "in_use":
		return urnPrefix + "in-use"
	case "no_translate_round":
		return urnPrefix + "no-translate-round"
	case "no_translated_segments":
		return urnPrefix + "no-translated-segments"
	case "issue_not_found":
		return urnPrefix + "issue-not-found"
	case "file_not_found":
		return urnPrefix + "file-not-found"
	case "client_closed":
		return urnPrefix + "client-closed"
	case "client_closed_request":
		return urnPrefix + "client-closed-request"
	case "internal_error":
		return urnPrefix + "internal-error"
	case "internal":
		return urnPrefix + "internal"
	case "openapi_error":
		return urnPrefix + "openapi-error"
	default:
		// Fallback: 将 snake_case 转为 kebab-case
		return urnPrefix + strings.ReplaceAll(title, "_", "-")
	}
}

// writeProblemWithType 写入 RFC 7807 Problem 响应,支持显式指定 type(URN)。
// 新代码优先调用 writeProblemWithType 以支持细粒度错误类型(如认证 4 种细分)。
func (s *Server) writeProblemWithType(w http.ResponseWriter, r *http.Request, status int, ptype, title, detail string, extraAttrs ...slog.Attr) {
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
			slog.String("type", ptype),
			slog.String("title", title),
			slog.String("detail", detail),
		}
		attrs = append(attrs, extraAttrs...)
		s.logger.LogAttrs(r.Context(), level, msg, attrs...)
	}

	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	problem := problemDetails{
		ErrorCode: title,
		Type:      ptype,
		Title:     title,
		Status:    status,
		Detail:    detail,
		Instance:  requestID,
	}
	if identity, ok := r.Context().Value(storageProblemIdentityKey{}).(*service.StorageOperationError); ok {
		problem.TaskID, problem.OperationID, problem.CheckID = identity.TaskID, identity.OperationID, identity.CheckID
	}
	_ = json.NewEncoder(w).Encode(problem)
}

// writeProblem 写入 RFC 7807 Problem 响应,type 由 title 自动派生为 URN。
// 所有现有调用点无需改动即可获得类型化的 problem 响应。
func (s *Server) writeProblem(w http.ResponseWriter, r *http.Request, status int, title, detail string, extraAttrs ...slog.Attr) {
	s.writeProblemWithType(w, r, status, urnForTitle(title), title, detail, extraAttrs...)
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
	case errors.Is(err, event.ErrHistoryUnavailable):
		s.writeProblem(w, r, http.StatusServiceUnavailable, "history_unavailable", "任务事件历史暂不可用")
	case errors.Is(err, service.ErrTaskHistoryNotFound), errors.Is(err, service.ErrJobNotFound), errors.Is(err, service.ErrSyncTaskNotFound), errors.Is(err, service.ErrProjectNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "任务记录或项目不存在")
	case errors.Is(err, service.ErrTaskNotTerminal):
		s.writeProblem(w, r, http.StatusConflict, "task_not_terminal", "任务尚未结束，无法删除记录")
	case errors.Is(err, tasklife.ErrBusy):
		s.writeProblem(w, r, http.StatusConflict, "task_busy", "任务仍在执行或收尾，请稍后重试")
	case errors.Is(err, service.ErrTaskCleanupDeferred):
		s.writeProblem(w, r, http.StatusServiceUnavailable, "task_cleanup_deferred", "清理预算已用尽，任务记录尚未删除，请稍后重试")
	case errors.Is(err, service.ErrSettingsConflict):
		s.writeProblem(w, r, http.StatusConflict, "settings_conflict", "设置已被修改，请重新读取并核对当前草稿")
	case errors.Is(err, service.ErrStorageMaintenance):
		s.writeStorageError(w, r, err)
	case errors.Is(err, service.ErrExecutionPlanNotFound), errors.Is(err, service.ErrExecutionProfileNotFound),
		errors.Is(err, service.ErrTranslationPromptTemplateNotFound), errors.Is(err, service.ErrBootstrapPromptTemplateNotFound),
		errors.Is(err, service.ErrPrunePromptTemplateNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "配置不存在或不可访问")
	case errors.Is(err, service.ErrExecutionPlanConfigInvalid):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_config", err.Error())
	case errors.Is(err, service.ErrOrganizationNameExists):
		s.writeProblem(w, r, http.StatusConflict, "organization_name_exists", "组织名称已存在")
	case errors.Is(err, service.ErrOrganizationSlugExists):
		s.writeProblem(w, r, http.StatusConflict, "organization_slug_exists", "组织 slug 已存在")
	case errors.Is(err, service.ErrMembershipExists):
		s.writeProblem(w, r, http.StatusConflict, "membership_exists", "组织成员已存在")
	case errors.Is(err, service.ErrOwnerRequired):
		s.writeProblem(w, r, http.StatusConflict, "owner_required", "组织必须保留至少一位 owner")
	case errors.Is(err, service.ErrOrganizationNotFound), errors.Is(err, service.ErrMembershipNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, service.ErrCurrentPasswordMismatch):
		s.writeProblemWithType(w, r, http.StatusBadRequest, urnPrefix+"current-password-mismatch", "current_password_mismatch", "当前密码不正确")
	case errors.Is(err, service.ErrInvalidInput):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求参数不合法")
	case errors.Is(err, service.ErrInvalidCredentials):
		s.writeProblemWithType(w, r, http.StatusUnauthorized, urnPrefix+"invalid-credentials", "unauthorized", "用户名或密码错误")
	case errors.Is(err, service.ErrSettingsUnavailable):
		s.writeProblemWithType(w, r, http.StatusServiceUnavailable, urnPrefix+"settings-unavailable", "unavailable", "系统设置暂不可用")
	case errors.Is(err, service.ErrTokenExpired):
		s.writeProblemWithType(w, r, http.StatusUnauthorized, urnPrefix+"token-expired", "unauthorized", "Token 已过期，请重新登录")
	case errors.Is(err, service.ErrTokenInvalid):
		s.writeProblemWithType(w, r, http.StatusUnauthorized, urnPrefix+"token-invalid", "unauthorized", "Token 无效或已失效")
	case errors.Is(err, service.ErrRefreshTokenRevoked):
		s.writeProblemWithType(w, r, http.StatusUnauthorized, urnPrefix+"refresh-token-revoked", "unauthorized", "登录已失效，请重新登录")
	case errors.Is(err, service.ErrUserInactive):
		s.writeProblemWithType(w, r, http.StatusUnauthorized, urnPrefix+"user-inactive", "unauthorized", "用户已被禁用")
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
	if code := service.StorageErrorCode(err); strings.HasPrefix(code, "storage_") && code != "storage_unavailable" {
		s.writeStorageError(w, r, err)
		return
	}
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
