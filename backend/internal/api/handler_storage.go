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
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
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
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeStorageError(w, r, service.ErrStorageTooLarge)
			return false
		}
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求体无效或过大")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求体必须是 JSON 对象")
		return false
	}
	allowed := map[string]bool{}
	typ := reflect.TypeOf(out).Elem()
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" && typ.Field(i).Tag.Get("readOnly") != "true" {
			allowed[name] = true
		}
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] || fields[name] != nil {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求包含未知或重复字段")
			return false
		}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求字段无效")
			return false
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) && name != "expires_at" {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求字段不能为 null")
			return false
		}
		fields[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求体无效")
		return false
	}
	if _, err = decoder.Token(); err != io.EOF {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求只能包含一个 JSON 对象")
		return false
	}
	for _, name := range required {
		v, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "缺少必填字段 "+name)
			return false
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求字段类型无效")
		return false
	}
	return true
}

func (s *Server) writeStorageError(w http.ResponseWriter, r *http.Request, err error) {
	code := service.StorageErrorCode(err)
	status, detail := http.StatusServiceUnavailable, "存储暂不可用，请查看原操作状态"
	switch code {
	case "source_revision_conflict", "storage_generation_conflict", "storage_idempotency_conflict":
		status, detail = http.StatusConflict, "请求与原操作或当前基线不一致，请核对原操作"
	case "storage_operation_in_progress":
		status, detail = http.StatusConflict, "原操作仍在执行，请查询原身份或稍后重放"
	case "storage_intent_expired", "storage_cancelled", "storage_maintenance":
		status, detail = http.StatusConflict, "当前任务或维护状态不允许执行该操作"
	case "source_missing", "source_corrupt", "repair_content_mismatch":
		status, detail = http.StatusConflict, "原件缺失、校验失败或修复文件与登记原件不一致"
	case "storage_auth_required", "storage_crypto_unavailable", "storage_capability_unsupported":
		status, detail = http.StatusConflict, "存储授权或能力不满足要求"
	case "storage_permission_denied", "storage_policy_violation":
		status, detail = http.StatusForbidden, "存储权限或政策不允许该操作"
	case "storage_quota_exceeded":
		status, detail = http.StatusConflict, "可用存储额度不足"
	case "storage_payload_too_large":
		status, detail = http.StatusRequestEntityTooLarge, "文件或处理预算超过允许大小"
	case "invalid_input", "storage_parse_failed":
		status, detail = http.StatusBadRequest, "请求组合不合法或文件无法解析"
	case "storage_timeout":
		status, detail = http.StatusGatewayTimeout, "存储操作超时，请先查询原操作状态"
	}
	switch {
	case errors.Is(err, service.ErrForbidden):
		status, code, detail = http.StatusForbidden, "forbidden", "没有权限执行此操作"
	case errors.Is(err, service.ErrProjectNotFound), errors.Is(err, service.ErrResourceNotFound), ent.IsNotFound(err):
		status, code, detail = http.StatusNotFound, "not_found", "资源不存在或不可访问"
	}
	var operation *service.StorageOperationError
	if errors.As(err, &operation) {
		r = r.WithContext(context.WithValue(r.Context(), storageProblemIdentityKey{}, operation))
	}
	s.writeProblem(w, r, status, code, detail)
}

func (s *Server) storageTaskResponse(r *http.Request, t *ent.StorageTask) StorageTask {
	actions := []string{}
	if auth, ok := authUserFromContext(r.Context()); ok {
		actions = s.storageSvc.TaskActions(r.Context(), auth.User.ID, t)
	}
	out := StorageTask{Id: t.ID, ProjectId: t.ProjectID, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, ExpiresAt: t.Deadline, OperationId: t.OperationID, Kind: t.Kind, Status: StorageTaskStatus(t.Status), Phase: t.Phase, CleanupStatus: StorageTaskCleanupStatus(t.CleanupStatus), ErrorCode: &t.ErrorCode, NextRetryAt: t.NextRetryAt, AllowedActions: actions, ResultResourceId: t.ResultResourceID, ResultRevisionId: t.ResultRevisionID, ResultArtifactId: t.ResultArtifactID,
		ResourceId: t.ResourceID, SourceRevisionId: t.SourceRevisionID, TargetSpaceId: t.TargetSpaceID, ExpectedStorageGeneration: &t.ExpectedStorageGeneration, ExpectedSourceGeneration: &t.ExpectedSourceGeneration, ExpectedTranslationGeneration: &t.ExpectedTranslationGeneration, ExpectedLocationGeneration: &t.ExpectedLocationGeneration}
	if t.ContractVersion > 0 {
		out.InputSize = &t.InputSize
		if t.InputSha256 != "" {
			out.InputSha256 = &t.InputSha256
		}
	}
	if auth, ok := authUserFromContext(r.Context()); ok && len(t.SourcePlan) > 0 {
		if preview, err := s.resourceSvc.SourcePreviewForTask(r.Context(), auth.User.ID, t.ProjectID, t.ID); err == nil {
			if data, e := json.Marshal(preview); e == nil {
				var dto SourceUpdatePreview
				if json.Unmarshal(data, &dto) == nil {
					out.SourcePreview = &dto
				}
			}
		}
	}
	return out
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
		var input StorageConnectionStateRequest
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
		var input StorageSpaceStateRequest
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
		row, check, err := s.storageConnections.AuthorizeWithCheck(r.Context(), actor, id, service.AuthorizeStorageInput{Payload: storageauth.S3Payload{Version: 1, AccessKeyID: input.AccessKeyID, SecretAccessKey: input.SecretAccessKey, SessionToken: input.SessionToken}, WriteCheck: input.WriteCheck, ExpiresAt: input.ExpiresAt, ExpectedManagementGeneration: input.ExpectedManagementGeneration})
		if err != nil {
			if check != nil {
				err = &service.StorageOperationError{Err: err, CheckID: check.CheckID}
			}
			s.writeStorageError(w, r, err)
			return
		}
		s.writeConnectionCheckResult(w, row, check)
	})
}
func (s *Server) RevokeStorageAuthorization(w http.ResponseWriter, r *http.Request, id int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		var input StorageRevokeRequest
		if !s.decodeStorageJSON(w, r, &input, "expected_generation") {
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
		row, check, err := s.storageConnections.CheckWithResult(r.Context(), actor, id, input.WriteCheck, input.ExpectedGeneration)
		if err != nil {
			if check != nil {
				err = &service.StorageOperationError{Err: err, CheckID: check.CheckID}
			}
			s.writeStorageError(w, r, err)
			return
		}
		s.writeConnectionCheckResult(w, row, check)
	})
}

func (s *Server) GetProjectStorage(w http.ResponseWriter, r *http.Request, projectID int) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		sp, err := s.storageSvc.ProjectStorage(r.Context(), actor, projectID)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, sp)
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
		var input StorageMigrationRequest
		if !s.decodeStorageJSON(w, r, &input, "space_id", "expected_generation", "idempotency_key") {
			return
		}
		key := input.IdempotencyKey
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
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		_ = r.Body.Close()
		if err != nil {
			s.writeStorageError(w, r, service.ErrStorageTooLarge)
			return
		}
		var identity struct {
			Kind string `json:"kind"`
			Key  string `json:"idempotency_key"`
		}
		if json.Unmarshal(raw, &identity) != nil {
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		var dto any
		required := []string{"kind", "idempotency_key", "size", "storage_generation"}
		switch identity.Kind {
		case "upload":
			dto = &StorageUploadIntent{}
			required = append(required, "path")
		case "source_update":
			dto = &StorageSourceUpdateIntent{}
			required = append(required, "resource_id", "source_generation", "translation_generation")
		case "repair":
			dto = &StorageRepairIntent{}
			required = append(required, "resource_id", "source_revision_id", "location_generation", "target_space_id")
		default:
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if !s.decodeStorageJSON(w, r, dto, required...) {
			return
		}
		var input service.StorageIntent
		if json.Unmarshal(raw, &input) != nil || service.ValidateStorageIdempotencyKey(identity.Key) != nil {
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		input.IdempotencyKey = identity.Key
		input.RequireStorageGeneration = true
		if input.Size < 0 || input.StorageGeneration < 0 || input.SourceGeneration < 0 || input.TranslationGeneration < 0 || input.LocationGeneration < 0 ||
			input.Kind == "upload" && input.Path == "" || input.Kind != "upload" && input.ResourceID <= 0 || input.Kind == "repair" && (input.SourceRevisionID <= 0 || input.TargetSpaceID <= 0) {
			s.writeStorageError(w, r, service.ErrInvalidInput)
			return
		}
		task, err := s.storageSvc.Begin(r.Context(), actor, projectID, input)
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
	if service.ValidateStorageIdempotencyKey(key) != nil {
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

func (s *Server) ListExportArtifacts(w http.ResponseWriter, r *http.Request, projectID, resourceID int, params ListExportArtifactsParams) {
	s.storageRequest(w, r, false, func(w http.ResponseWriter, r *http.Request, actor int) {
		rows, err := s.resourceSvc.ListExportsIncludingDeleted(r.Context(), actor, projectID, resourceID, storageValue(params.IncludeDeleted))
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		out := make([]ExportArtifact, 0, len(rows))
		for _, a := range rows {
			out = append(out, ExportArtifact{Id: a.ID, SourceRevisionId: a.SourceRevisionID, Status: ExportArtifactStatus(a.Status), Rebuildable: a.Rebuildable, RendererVersion: a.RendererVersion, Filename: a.Filename, DeletionTaskId: a.DeletionTaskID})
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
