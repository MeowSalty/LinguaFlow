package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

func (s *Server) handleAnalyzeGlossarySyncImpact(w http.ResponseWriter, r *http.Request, projectId int, entryId int) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var input service.GlossarySyncImpactInput
	if !s.decodeJSON(w, r, &input) {
		return
	}

	result, err := s.glossarySyncSvc.AnalyzeSyncImpact(r.Context(), authUser.User.ID, projectId, entryId, input)
	if err != nil {
		s.writeGlossarySyncServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleExecuteGlossarySyncUpdate(w http.ResponseWriter, r *http.Request, projectId int, entryId int) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var input service.GlossarySyncExecuteInput
	if !s.decodeJSON(w, r, &input) {
		return
	}

	taskInfo, err := s.glossarySyncSvc.SubmitSyncTask(r.Context(), authUser.User.ID, projectId, entryId, input)
	if err != nil {
		s.writeGlossarySyncServiceError(w, r, err)
		return
	}

	// Persistence is acceptance. The dispatcher discovers pending work and retries
	// delivery independently of the request context and in-memory queue capacity.
	if s.dispatcher != nil {
		s.dispatcher.Notify("sync")
	}

	writeJSON(w, http.StatusAccepted, convertSyncTaskInfoToExecuteResponse(taskInfo))
}

func (s *Server) handleGetGlossarySyncTaskStatus(w http.ResponseWriter, r *http.Request, projectId int, taskId string) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	taskID, err := strconv.Atoi(taskId)
	if err != nil || taskID <= 0 {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_task_id", "任务 ID 格式不正确")
		return
	}

	task, err := s.glossarySyncSvc.GetSyncTaskStatus(r.Context(), authUser.User.ID, projectId, taskID)
	if err != nil {
		s.writeGlossarySyncServiceError(w, r, err)
		return
	}

	response := convertSyncTaskToStatusResponse(task)
	response.CanDelete = s.canDeleteHistory(r.Context(), authUser.User.ID, "glossary_sync", task.ID, task.ProjectID, task.Status)
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleCancelGlossarySyncTask(w http.ResponseWriter, r *http.Request, projectId int, taskId string) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	taskID, err := strconv.Atoi(taskId)
	if err != nil || taskID <= 0 {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_task_id", "任务 ID 格式不正确")
		return
	}

	task, err := s.glossarySyncSvc.CancelSyncTask(r.Context(), authUser.User.ID, projectId, taskID)
	if err != nil {
		s.writeGlossarySyncServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, convertSyncTaskToCancelResponse(task))
}

func (s *Server) writeGlossarySyncServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrSyncTaskStateConflict):
		s.writeProblem(w, r, http.StatusConflict, "sync_task_state_conflict", "已完成或失败的任务不能取消")
	case errors.Is(err, service.ErrSyncTaskNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "同步任务不存在")
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrProjectNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "项目不存在")
	case errors.Is(err, service.ErrGlossaryEntryNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "术语条目不存在")
	case errors.Is(err, service.ErrInvalidInput):
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "请求参数不合法")
	case errors.Is(err, service.ErrNoAffectedSegments):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "未找到受影响的段落")
	case ent.IsNotFound(err):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "同步任务不存在")
	default:
		s.writeServiceError(w, r, err)
	}
}

// convertSyncTaskToStatusResponse 将 ent.SyncTask 转换为 OpenAPI 规范的响应格式
func convertSyncTaskToStatusResponse(task *ent.SyncTask) GlossarySyncTaskStatusResponse {
	resp := GlossarySyncTaskStatusResponse{
		TaskId:      strconv.Itoa(task.ID),
		Status:      GlossarySyncTaskStatusResponseStatus(task.Status),
		Processed:   task.ProcessedSegments,
		Total:       task.TotalSegments,
		CancelledAt: timeutil.NormalizePtr(task.CancelledAt),
		FinishedAt:  timeutil.NormalizePtr(task.FinishedAt),
		Error:       nilIfEmpty(task.Error),
	}

	if task.Result != "" {
		var result struct {
			Resources    *[]GlossarySyncExecuteResourceResult `json:"resources,omitempty"`
			TotalSkipped *int                                 `json:"total_skipped,omitempty"`
			TotalUpdated *int                                 `json:"total_updated,omitempty"`
		}
		if err := json.Unmarshal([]byte(task.Result), &result); err == nil {
			resp.Result = &result
		}
	}

	return resp
}

// convertSyncTaskToCancelResponse 将 ent.SyncTask 转换为取消操作的响应格式
func convertSyncTaskToCancelResponse(task *ent.SyncTask) GlossarySyncTaskCancelResponse {
	return GlossarySyncTaskCancelResponse{
		TaskId: strconv.Itoa(task.ID),
		Status: GlossarySyncTaskCancelResponseStatus(task.Status),
	}
}

// convertSyncTaskInfoToExecuteResponse 将 SyncTaskInfo 转换为提交操作的响应格式
func convertSyncTaskInfoToExecuteResponse(info *service.SyncTaskInfo) GlossarySyncExecuteResponse {
	return GlossarySyncExecuteResponse{
		TaskId:    strconv.Itoa(info.TaskID),
		Status:    GlossarySyncExecuteResponseStatus(info.Status),
		StatusUrl: info.StatusURL,
	}
}

// nilIfEmpty 将空字符串转为 nil，用于匹配 OpenAPI 规范中 omitempty 的可选字段
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
