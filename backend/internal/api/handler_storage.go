package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/exportartifact"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
)

func (s *Server) storageRequest(w http.ResponseWriter, r *http.Request, admin bool, handle func(http.ResponseWriter, *http.Request, int)) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authUserFromContext(r.Context())
		if !ok {
			s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
			return
		}
		if s.storageSvc == nil || s.storageConnections == nil {
			s.writeProblem(w, r, http.StatusServiceUnavailable, "storage_unavailable", "文件存储尚未就绪")
			return
		}
		handle(w, r, auth.User.ID)
	})
	if admin {
		s.requireAdmin(h).ServeHTTP(w, r)
	} else {
		s.requireAuth(h).ServeHTTP(w, r)
	}
}

// 存储写入要求字段名精确、generation 值必须显式提供；
// 不允许缺失或为 null 的 generation 被静默当作合法的零代 CAS。
func (s *Server) decodeStorageJSON(w http.ResponseWriter, r *http.Request, out any, required ...string) bool {
	defer r.Body.Close()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体无效或过大")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体必须是 JSON 对象")
		return false
	}
	allowed := map[string]bool{}
	typ := reflect.TypeOf(out).Elem()
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			allowed[name] = true
		}
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] || fields[name] != nil {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求包含未知或重复字段")
			return false
		}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求字段无效")
			return false
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) && name != "expires_at" {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求字段不能为 null")
			return false
		}
		fields[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体无效")
		return false
	}
	if _, err = decoder.Token(); err != io.EOF {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求只能包含一个 JSON 对象")
		return false
	}
	for _, name := range required {
		v, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "缺少必填字段 "+name)
			return false
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求字段类型无效")
		return false
	}
	return true
}

func (s *Server) writeStorageError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, detail := http.StatusServiceUnavailable, "storage_unavailable", "存储暂不可用，请查看任务状态后重试"
	switch {
	case errors.Is(err, service.ErrForbidden):
		status, code, detail = http.StatusForbidden, "forbidden", "没有权限执行此操作"
	case errors.Is(err, service.ErrProjectNotFound), errors.Is(err, service.ErrResourceNotFound), ent.IsNotFound(err):
		status, code, detail = http.StatusNotFound, "not_found", "资源不存在或不可访问"
	case errors.Is(err, service.ErrInvalidInput), errors.Is(err, service.ErrResourcePathInvalid), errors.Is(err, storageauth.ErrInvalid):
		status, code, detail = http.StatusBadRequest, "invalid_input", "请求参数不合法"
	case errors.Is(err, service.ErrStorageConflict), errors.Is(err, service.ErrSourceRevisionConflict), errors.Is(err, service.ErrStorageIdempotency):
		status, code, detail = http.StatusConflict, "storage_generation_conflict", "内容或存储状态已变化，请刷新后重试"
	case errors.Is(err, service.ErrRepairMismatch):
		status, code, detail = http.StatusConflict, "repair_content_mismatch", "修复文件与已登记原件不一致"
	case errors.Is(err, service.ErrStorageMaintenance):
		status, code, detail = http.StatusConflict, "storage_maintenance", "项目或存储正在维护，暂时不能执行此操作"
	case errors.Is(err, service.ErrStoragePolicy):
		status, code, detail = http.StatusForbidden, "storage_policy_violation", "当前存储政策不允许此操作"
	case errors.Is(err, storage.ErrLimit), errors.Is(err, service.ErrStorageTooLarge):
		status, code, detail = http.StatusRequestEntityTooLarge, "storage_quota_exceeded", "存储或处理容量已达上限"
	case errors.Is(err, storage.ErrNotFound):
		status, code, detail = http.StatusConflict, "source_missing", "原件缺失，需要修复或重新连接"
	case errors.Is(err, storage.ErrCorrupt):
		status, code, detail = http.StatusConflict, "source_corrupt", "原件校验失败，需要修复"
	case errors.Is(err, storage.ErrPermission):
		status, code, detail = http.StatusForbidden, "storage_permission_denied", "存储授权或管理状态不允许此操作"
	case errors.Is(err, storage.ErrAuthRequired):
		status, code, detail = http.StatusConflict, "storage_auth_required", "请重新授权存储连接"
	case errors.Is(err, service.ErrStorageCrypto):
		status, code, detail = http.StatusConflict, "storage_crypto_unavailable", "当前存储授权无法解密，请重新授权或联系管理员"
	case errors.Is(err, storage.ErrUnsupported):
		status, code, detail = http.StatusConflict, "storage_capability_unsupported", "存储服务缺少所需能力"
	case errors.Is(err, service.ErrUnsupportedFormat), errors.Is(err, service.ErrParseFailed):
		status, code, detail = http.StatusBadRequest, "storage_parse_failed", "文件格式不受支持或无法解析"
	case errors.Is(err, context.Canceled), errors.Is(err, service.ErrStorageCancelled):
		status, code, detail = http.StatusConflict, "storage_cancelled", "操作已取消"
	case errors.Is(err, context.DeadlineExceeded):
		status, code, detail = http.StatusGatewayTimeout, "storage_timeout", "存储操作超时，已保留任务状态供对账"
	}
	// 绝不把提供方返回的错误原文附进响应：其中可能包含密钥、签名 URL 或其他机密。
	s.writeProblem(w, r, status, code, detail)
}

func (s *Server) storageTaskResponse(r *http.Request, t *ent.StorageTask) StorageTask {
	actions := []string{}
	if auth, ok := authUserFromContext(r.Context()); ok {
		actions = s.storageSvc.TaskActions(r.Context(), auth.User.ID, t)
	}
	return StorageTask{Id: t.ID, OperationId: t.OperationID, Kind: t.Kind, Status: StorageTaskStatus(t.Status), Phase: t.Phase, CleanupStatus: StorageTaskCleanupStatus(t.CleanupStatus), ErrorCode: &t.ErrorCode, NextRetryAt: t.NextRetryAt, AllowedActions: actions, ResultResourceId: t.ResultResourceID, ResultRevisionId: t.ResultRevisionID, ResultArtifactId: t.ResultArtifactID}
}
func (s *Server) storageTaskResult(w http.ResponseWriter, r *http.Request, t *ent.StorageTask, err error, status int) {
	if err != nil {
		s.writeStorageError(w, r, err)
		return
	}
	writeJSON(w, status, s.storageTaskResponse(r, t))
}

func (s *Server) GetStoragePolicy(w http.ResponseWriter, r *http.Request) {
	s.storageRequest(w, r, true, func(w http.ResponseWriter, r *http.Request, _ int) {
		p, err := s.storageSvc.Policy(r.Context())
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})
}
func (s *Server) SetStoragePolicy(w http.ResponseWriter, r *http.Request) {
	s.storageRequest(w, r, true, func(w http.ResponseWriter, r *http.Request, actor int) {
		var p service.StoragePolicy
		if !s.decodeStorageJSON(w, r, &p, "mode", "default_choice", "generation", "logical_limit_bytes") {
			return
		}
		if s.serverCfg != nil && !s.serverCfg.Storage.Enabled && p.Mode != "site_only" {
			s.writeStorageError(w, r, service.ErrStoragePolicy)
			return
		}
		out, err := s.storageSvc.SetPolicy(r.Context(), actor, p)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}

func (s *Server) listStorageConnections(w http.ResponseWriter, r *http.Request, scope string, owner int, admin bool) {
	s.storageRequest(w, r, admin, func(w http.ResponseWriter, r *http.Request, actor int) {
		if scope == "user" {
			owner = actor
		}
		rows, err := s.storageConnections.List(r.Context(), actor, scope, owner)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	})
}
func (s *Server) ListStorageConnections(w http.ResponseWriter, r *http.Request) {
	s.listStorageConnections(w, r, "user", 0, false)
}
func (s *Server) ListOrgStorageConnections(w http.ResponseWriter, r *http.Request, orgID int) {
	s.listStorageConnections(w, r, "org", orgID, false)
}
func (s *Server) ListSiteStorageConnections(w http.ResponseWriter, r *http.Request) {
	s.listStorageConnections(w, r, "site", 0, true)
}
func (s *Server) createStorageConnection(w http.ResponseWriter, r *http.Request, scope string, owner int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		if scope == "user" {
			owner = actor
		}
		var input service.CreateStorageConnectionInput
		if !s.decodeStorageJSON(w, r, &input, "name", "endpoint", "region") {
			return
		}
		if input.Scope != "" && input.Scope != scope || input.OwnerID != 0 && input.OwnerID != owner {
			s.writeStorageError(w, r, service.ErrForbidden)
			return
		}
		input.Scope = scope
		input.OwnerID = owner
		row, err := s.storageConnections.Create(r.Context(), actor, input)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, row)
	})
}
func (s *Server) CreateStorageConnection(w http.ResponseWriter, r *http.Request) {
	s.createStorageConnection(w, r, "user", 0)
}
func (s *Server) CreateOrgStorageConnection(w http.ResponseWriter, r *http.Request, orgID int) {
	s.createStorageConnection(w, r, "org", orgID)
}
func (s *Server) ListStorageConnectionSpaces(w http.ResponseWriter, r *http.Request, id int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		rows, err := s.storageConnections.Spaces(r.Context(), actor, id)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	})
}
func (s *Server) CreateStorageSpace(w http.ResponseWriter, r *http.Request, id int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		var input service.CreateStorageSpaceInput
		if !s.decodeStorageJSON(w, r, &input, "name", "bucket", "prefix", "capacity_bytes") {
			return
		}
		if input.CapacityBytes <= 0 {
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		row, err := s.storageConnections.CreateSpace(r.Context(), actor, id, input)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, row)
	})
}
func (s *Server) SetStorageConnectionState(w http.ResponseWriter, r *http.Request, id int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		var input StorageStateRequest
		if !s.decodeStorageJSON(w, r, &input, "status", "expected_generation") {
			return
		}
		row, err := s.storageConnections.SetStatus(r.Context(), actor, id, string(input.Status), input.ExpectedGeneration)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, row)
	})
}
func (s *Server) SetStorageSpaceState(w http.ResponseWriter, r *http.Request, id int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		var input StorageStateRequest
		if !s.decodeStorageJSON(w, r, &input, "status", "expected_generation") {
			return
		}
		row, err := s.storageConnections.SetSpaceStatus(r.Context(), actor, id, string(input.Status), input.ExpectedGeneration)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, row)
	})
}
func (s *Server) AuthorizeStorage(w http.ResponseWriter, r *http.Request, id int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		var input struct {
			AccessKeyID                  string     `json:"access_key_id"`
			SecretAccessKey              string     `json:"secret_access_key"`
			SessionToken                 string     `json:"session_token"`
			WriteCheck                   bool       `json:"write_check"`
			ExpiresAt                    *time.Time `json:"expires_at"`
			ExpectedManagementGeneration int64      `json:"expected_management_generation"`
		}
		if !s.decodeStorageJSON(w, r, &input, "access_key_id", "secret_access_key", "write_check", "expected_management_generation") {
			return
		}
		row, err := s.storageConnections.Authorize(r.Context(), actor, id, service.AuthorizeStorageInput{Payload: storageauth.S3Payload{Version: 1, AccessKeyID: input.AccessKeyID, SecretAccessKey: input.SecretAccessKey, SessionToken: input.SessionToken}, WriteCheck: input.WriteCheck, ExpiresAt: input.ExpiresAt, ExpectedManagementGeneration: input.ExpectedManagementGeneration})
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, row)
	})
}
func (s *Server) RevokeStorageAuthorization(w http.ResponseWriter, r *http.Request, id int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		var input StorageStateRequest
		if !s.decodeStorageJSON(w, r, &input, "status", "expected_generation") {
			return
		}
		if string(input.Status) != "disabled" {
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		row, err := s.storageConnections.Revoke(r.Context(), actor, id, input.ExpectedGeneration)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, row)
	})
}
func (s *Server) CheckStorageConnection(w http.ResponseWriter, r *http.Request, id int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		var input struct {
			WriteCheck         bool  `json:"write_check"`
			ExpectedGeneration int64 `json:"expected_generation"`
		}
		if !s.decodeStorageJSON(w, r, &input, "write_check", "expected_generation") {
			return
		}
		row, err := s.storageConnections.Check(r.Context(), actor, id, input.WriteCheck, input.ExpectedGeneration)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, row)
	})
}

func (s *Server) GetProjectStorage(w http.ResponseWriter, r *http.Request, projectID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		sp, err := s.storageSvc.SpaceForProject(r.Context(), actor, projectID)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": sp.ID, "connection_id": sp.ConnectionID, "name": sp.Name, "status": sp.Status, "verified": sp.Verified, "versioned": sp.Versioned, "management_generation": sp.ManagementGeneration, "capacity_bytes": sp.CapacityBytes, "reserved_bytes": sp.ReservedBytes, "candidate_bytes": sp.CandidateBytes, "live_bytes": sp.LiveBytes, "pending_delete_bytes": sp.PendingDeleteBytes})
	})
}
func (s *Server) BindProjectStorage(w http.ResponseWriter, r *http.Request, projectID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		var input StorageBindingRequest
		if !s.decodeStorageJSON(w, r, &input, "space_id", "expected_generation") {
			return
		}
		if err := s.storageSvc.Bind(r.Context(), actor, projectID, input.SpaceId, input.ExpectedGeneration); err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
func (s *Server) MigrateProjectStorage(w http.ResponseWriter, r *http.Request, projectID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		var input StorageBindingRequest
		if !s.decodeStorageJSON(w, r, &input, "space_id", "expected_generation") {
			return
		}
		key := ""
		if input.IdempotencyKey != nil {
			key = *input.IdempotencyKey
		}
		task, err := s.storageSvc.StartMigration(r.Context(), actor, projectID, input.SpaceId, input.ExpectedGeneration, key)
		s.storageTaskResult(w, r, task, err, http.StatusAccepted)
	})
}

func (s *Server) ListStorageTasks(w http.ResponseWriter, r *http.Request, projectID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		rows, err := s.storageSvc.ListTasks(r.Context(), actor, projectID)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		items := make([]StorageTask, 0, len(rows))
		for _, row := range rows {
			items = append(items, s.storageTaskResponse(r, row))
		}
		writeJSON(w, http.StatusOK, StorageTaskList{Items: items})
	})
}
func (s *Server) GetStorageTask(w http.ResponseWriter, r *http.Request, projectID, taskID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		task, err := s.storageSvc.Task(r.Context(), actor, projectID, taskID)
		s.storageTaskResult(w, r, task, err, http.StatusOK)
	})
}
func storageValue[T any](value *T) (zero T) {
	if value != nil {
		return *value
	}
	return zero
}
func (s *Server) CreateStorageIntent(w http.ResponseWriter, r *http.Request, projectID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		var input StorageIntent
		if !s.decodeStorageJSON(w, r, &input, "kind", "size", "idempotency_key", "storage_generation") {
			return
		}
		if !input.Kind.Valid() || strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 200 {
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		in := service.StorageIntent{Kind: string(input.Kind), IdempotencyKey: input.IdempotencyKey, Path: storageValue(input.Path), ResourceID: storageValue(input.ResourceId), SourceRevisionID: storageValue(input.SourceRevisionId), TargetSpaceID: storageValue(input.TargetSpaceId), Size: input.Size, SourceGeneration: storageValue(input.SourceGeneration), TranslationGeneration: storageValue(input.TranslationGeneration), StorageGeneration: input.StorageGeneration, LocationGeneration: storageValue(input.LocationGeneration)}
		in.RequireStorageGeneration = true
		if in.StorageGeneration < 0 || in.SourceGeneration < 0 || in.TranslationGeneration < 0 || in.LocationGeneration < 0 || in.Kind == "source_update" && (input.SourceGeneration == nil || input.TranslationGeneration == nil) || in.Kind == "repair" && input.LocationGeneration == nil {
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		if in.Kind == "upload" && in.Path == "" || in.Kind != "upload" && in.ResourceID <= 0 || in.Kind == "repair" && in.SourceRevisionID <= 0 {
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		task, err := s.storageSvc.Begin(r.Context(), actor, projectID, in)
		s.storageTaskResult(w, r, task, err, http.StatusAccepted)
	})
}

func (s *Server) storageBody(w http.ResponseWriter, r *http.Request) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/octet-stream" {
		s.writeProblem(w, r, http.StatusUnsupportedMediaType, "invalid_content_type", "请发送 application/octet-stream 文件内容")
		return false
	}
	if r.ContentLength < 0 {
		s.writeProblem(w, r, http.StatusLengthRequired, "content_length_required", "必须提供文件字节长度")
		return false
	}
	limit := int64(100 << 20)
	if s.serverCfg != nil {
		limit = s.serverCfg.Storage.Limits.MaxFileBytes
	}
	if limit <= 0 || r.ContentLength > limit {
		s.writeStorageError(w, r, service.ErrStorageTooLarge)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit+1)
	return true
}
func (s *Server) ReceiveStorageContent(w http.ResponseWriter, r *http.Request, projectID, taskID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		if !s.storageBody(w, r) {
			return
		}
		defer r.Body.Close()
		task, err := s.storageSvc.Receive(r.Context(), actor, projectID, taskID, r.Body, r.ContentLength)
		s.storageTaskResult(w, r, task, err, http.StatusAccepted)
	})
}
func (s *Server) CancelStorageTask(w http.ResponseWriter, r *http.Request, projectID, taskID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		task, err := s.storageSvc.Cancel(r.Context(), actor, projectID, taskID)
		s.storageTaskResult(w, r, task, err, http.StatusOK)
	})
}
func (s *Server) RetryStorageTask(w http.ResponseWriter, r *http.Request, projectID, taskID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		task, err := s.storageSvc.Retry(r.Context(), actor, projectID, taskID)
		s.storageTaskResult(w, r, task, err, http.StatusAccepted)
	})
}

func (s *Server) ListSourceVersions(w http.ResponseWriter, r *http.Request, projectID, resourceID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		rows, err := s.resourceSvc.Versions(r.Context(), actor, projectID, resourceID)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	})
}
func (s *Server) storageIdempotency(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "必须提供有效的 Idempotency-Key")
		return "", false
	}
	return key, true
}
func (s *Server) PreviewSourceUpdate(w http.ResponseWriter, r *http.Request, projectID, resourceID int, _ PreviewSourceUpdateParams) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		if !s.storageBody(w, r) {
			return
		}
		key, ok := s.storageIdempotency(w, r)
		if !ok {
			return
		}
		defer r.Body.Close()
		preview, err := s.resourceSvc.PreviewSourceUpdate(r.Context(), actor, projectID, resourceID, service.UploadedFile{Reader: r.Body, Size: r.ContentLength, IdempotencyKey: key})
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, preview)
	})
}
func (s *Server) CommitSourceUpdate(w http.ResponseWriter, r *http.Request, projectID, resourceID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		var input SourceUpdateCommit
		if !s.decodeStorageJSON(w, r, &input, "task_id", "expected_source_generation", "expected_translation_generation") {
			return
		}
		if _, _, err := s.resourceSvc.CommitSourceUpdate(r.Context(), actor, projectID, resourceID, input.TaskId, input.ExpectedSourceGeneration, input.ExpectedTranslationGeneration); err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		task, err := s.storageSvc.Task(r.Context(), actor, projectID, input.TaskId)
		s.storageTaskResult(w, r, task, err, http.StatusOK)
	})
}

func (s *Server) ListExportArtifacts(w http.ResponseWriter, r *http.Request, projectID, resourceID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		rows, err := s.resourceSvc.ListExports(r.Context(), actor, projectID, resourceID)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		out := make([]ExportArtifact, 0, len(rows))
		for _, a := range rows {
			out = append(out, ExportArtifact{Id: a.ID, SourceRevisionId: a.SourceRevisionID, Status: ExportArtifactStatus(a.Status), Rebuildable: a.SnapshotBlobID != nil && a.Status != exportartifact.StatusDeleted, RendererVersion: a.RendererVersion, Filename: a.Filename})
		}
		writeJSON(w, http.StatusOK, ExportArtifactList{Items: out})
	})
}
func (s *Server) CreateExportArtifact(w http.ResponseWriter, r *http.Request, projectID, resourceID int, _ CreateExportArtifactParams) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		key, ok := s.storageIdempotency(w, r)
		if !ok {
			return
		}
		task, err := s.resourceSvc.CreateExport(r.Context(), actor, projectID, resourceID, key)
		s.storageTaskResult(w, r, task, err, http.StatusAccepted)
	})
}
func (s *Server) RebuildExportArtifact(w http.ResponseWriter, r *http.Request, projectID, artifactID int, _ RebuildExportArtifactParams) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		key, ok := s.storageIdempotency(w, r)
		if !ok {
			return
		}
		task, err := s.resourceSvc.RebuildExport(r.Context(), actor, projectID, artifactID, key)
		s.storageTaskResult(w, r, task, err, http.StatusAccepted)
	})
}
func (s *Server) DeleteExportArtifact(w http.ResponseWriter, r *http.Request, projectID, artifactID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		if err := s.resourceSvc.DeleteExport(r.Context(), actor, projectID, artifactID); err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
func (s *Server) DownloadExportArtifact(w http.ResponseWriter, r *http.Request, projectID, artifactID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		f, name, err := s.resourceSvc.DownloadExport(r.Context(), actor, projectID, artifactID)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(name)}))
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)
	})
}
