package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func (s *Server) handleAnalyzeGlossarySyncImpact(w http.ResponseWriter, r *http.Request, projectId int, entryId int) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var input service.GlossarySyncImpactInput
	if !decodeJSON(w, r, &input) {
		return
	}

	result, err := s.glossarySyncSvc.AnalyzeSyncImpact(r.Context(), authUser.User.ID, projectId, entryId, input)
	if err != nil {
		writeGlossarySyncServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleExecuteGlossarySyncUpdate(w http.ResponseWriter, r *http.Request, projectId int, entryId int) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	var input service.GlossarySyncExecuteInput
	if !decodeJSON(w, r, &input) {
		return
	}

	taskInfo, err := s.glossarySyncSvc.SubmitSyncTask(r.Context(), authUser.User.ID, projectId, entryId, input)
	if err != nil {
		writeGlossarySyncServiceError(w, err)
		return
	}

	// 将任务入队，通知 SyncTaskRunner 处理
	if s.dispatcher != nil {
		if err := s.dispatcher.Enqueue(r.Context(), "sync", taskInfo.TaskID); err != nil {
			writeServiceError(w, err)
			return
		}
	}

	writeJSON(w, http.StatusAccepted, convertSyncTaskInfoToExecuteResponse(taskInfo))
}

func (s *Server) handleGetGlossarySyncTaskStatus(w http.ResponseWriter, r *http.Request, projectId int, taskId string) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	taskID, err := strconv.Atoi(taskId)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_task_id", "任务 ID 格式不正确")
		return
	}

	task, err := s.glossarySyncSvc.GetSyncTaskStatus(r.Context(), authUser.User.ID, projectId, taskID)
	if err != nil {
		writeGlossarySyncServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, convertSyncTaskToStatusResponse(task))
}

func (s *Server) handleCancelGlossarySyncTask(w http.ResponseWriter, r *http.Request, projectId int, taskId string) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}

	taskID, err := strconv.Atoi(taskId)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_task_id", "任务 ID 格式不正确")
		return
	}

	task, err := s.glossarySyncSvc.CancelSyncTask(r.Context(), authUser.User.ID, projectId, taskID)
	if err != nil {
		writeGlossarySyncServiceError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, convertSyncTaskToCancelResponse(task))
}

func writeGlossarySyncServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		writeProblem(w, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrProjectNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "项目不存在")
	case errors.Is(err, service.ErrGlossaryEntryNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "术语条目不存在")
	case errors.Is(err, service.ErrInvalidInput):
		writeProblem(w, http.StatusBadRequest, "invalid_input", "请求参数不合法")
	case errors.Is(err, service.ErrNoAffectedSegments):
		writeProblem(w, http.StatusNotFound, "not_found", "未找到受影响的段落")
	case ent.IsNotFound(err):
		writeProblem(w, http.StatusNotFound, "not_found", "同步任务不存在")
	default:
		writeServiceError(w, err)
	}
}

// convertSyncTaskToStatusResponse 将 ent.SyncTask 转换为 OpenAPI 规范的响应格式
func convertSyncTaskToStatusResponse(task *ent.SyncTask) GlossarySyncTaskStatusResponse {
	resp := GlossarySyncTaskStatusResponse{
		TaskId:      strconv.Itoa(task.ID),
		Status:      GlossarySyncTaskStatusResponseStatus(task.Status),
		Processed:   task.ProcessedSegments,
		Total:       task.TotalSegments,
		CancelledAt: task.CancelledAt,
		Error:       nilIfEmpty(task.Error),
	}

	if task.Result != "" && task.Status == service.SyncTaskStatusCompleted {
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
		Status: GlossarySyncTaskCancelResponseStatusCancelled,
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
