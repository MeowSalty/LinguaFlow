package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// decodeStrictObject preserves absent fields while rejecting null, unknown
// properties and trailing JSON. Presence-sensitive callers use RawMessage.
func decodeStrictObject(data []byte, target any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("expected JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func (s *Server) decodeStrictBody(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Body == nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体必须是 JSON 对象")
		return false
	}
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || decodeStrictObject(data, target) != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体必须是单个 JSON 对象，且字段和类型合法")
		return false
	}
	return true
}

func (s *Server) DeleteJobHistory(w http.ResponseWriter, r *http.Request, jobID JobId) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, _ := authUserFromContext(r.Context())
		if jobID <= 0 {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "任务 ID 必须是正整数")
			return
		}
		err := s.taskHistory.Delete(r.Context(), actor.User.ID, service.HistoryTarget{Kind: "translation", ID: strconv.Itoa(jobID)})
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(w, r)
}

func (s *Server) DeleteGlossarySyncTaskHistory(w http.ResponseWriter, r *http.Request, projectID ProjectId, taskID string) {
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, _ := authUserFromContext(r.Context())
		if !validHistoryID(taskID) || projectID <= 0 {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_input", "任务和项目 ID 必须是正整数")
			return
		}
		err := s.taskHistory.Delete(r.Context(), actor.User.ID, service.HistoryTarget{Kind: "glossary_sync", ID: taskID, ProjectID: projectID})
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(w, r)
}

func validHistoryID(raw string) bool {
	id, err := strconv.Atoi(raw)
	return err == nil && id > 0 && strconv.Itoa(id) == raw
}

func (s *Server) BatchDeleteTaskHistory(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(http.HandlerFunc(s.handleBatchDeleteTaskHistory)).ServeHTTP(w, r)
}

func (s *Server) handleBatchDeleteTaskHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []service.HistoryTarget `json:"items"`
	}
	if !s.decodeStrictBody(w, r, &req) {
		return
	}
	if len(req.Items) == 0 || len(req.Items) > 100 {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "必须显式选择 1 至 100 个任务")
		return
	}
	for _, target := range req.Items {
		if target.Kind != "translation" && target.Kind != "glossary_sync" || !validHistoryID(target.ID) || target.ProjectID <= 0 {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "任务类型、ID 或项目 ID 不合法")
			return
		}
	}
	actor, _ := authUserFromContext(r.Context())
	items := s.taskHistory.BatchDelete(r.Context(), actor.User.ID, req.Items)
	writeJSON(w, http.StatusOK, struct {
		Items []service.HistoryDeleteResult `json:"items"`
	}{Items: items})
}

func (s *Server) PreviewTaskRetention(w http.ResponseWriter, r *http.Request) {
	s.requireAdmin(http.HandlerFunc(s.handlePreviewTaskRetention)).ServeHTTP(w, r)
}

func (s *Server) handlePreviewTaskRetention(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RetentionDays *int `json:"retention_days"`
	}
	if !s.decodeStrictBody(w, r, &req) {
		return
	}
	if req.RetentionDays == nil || *req.RetentionDays < 1 || *req.RetentionDays > 3650 {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "retention_days 必须是 1 至 3650 的整数")
		return
	}
	preview, err := s.taskHistory.Preview(r.Context(), *req.RetentionDays)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) GetTaskRetentionStatus(w http.ResponseWriter, r *http.Request) {
	s.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, err := s.taskHistory.Status(r.Context())
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	})).ServeHTTP(w, r)
}

func (s *Server) canDeleteHistory(ctx context.Context, actorID int, kind string, id, projectID int, status string) bool {
	return s.taskHistory != nil && s.taskHistory.CanDelete(ctx, actorID, kind, id, projectID, status)
}
