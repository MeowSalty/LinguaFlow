package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
)

func (s *Server) handleListJobEvents(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := s.parseIntParam(w, r, chi.URLParam(r, "jobId"), "jobId")
	if !ok {
		return
	}
	if err := s.jobSvc.CheckJobAccess(r.Context(), authUser.User.ID, jobID); err != nil {
		s.writeJobServiceError(w, r, err)
		return
	}

	limit, ok := s.parseLimitParam(w, r, 50, 100)
	if !ok {
		return
	}

	rawBefore := strings.TrimSpace(r.URL.Query().Get("before_seq"))
	rawAfter := strings.TrimSpace(r.URL.Query().Get("after_seq"))

	if rawBefore != "" {
		beforeSeq, valid := parseBeforeSeq(rawBefore)
		if !valid {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "before_seq 必须是有效正整数")
			return
		}
		events, nextBeforeSeq, hasMore := s.eventBroker.ListHistoryBefore(r.Context(), jobID, beforeSeq, limit)
		items := make([]JobEvent, 0, len(events))
		for _, evt := range events {
			items = append(items, jobEventFromEvent(evt))
		}
		resp := JobEventListResponse{Items: items}
		if hasMore {
			n := nextBeforeSeq
			resp.NextBeforeSeq = &n
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	if rawAfter != "" {
		afterSeq, valid := parseAfterSeq(rawAfter)
		if !valid {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "after_seq 必须是有效非负整数")
			return
		}
		events, nextAfterSeq, hasMore := s.eventBroker.ListHistory(r.Context(), jobID, afterSeq, limit)
		items := make([]JobEvent, 0, len(events))
		for _, evt := range events {
			items = append(items, jobEventFromEvent(evt))
		}
		resp := JobEventListResponse{Items: items}
		if hasMore {
			n := nextAfterSeq
			resp.NextAfterSeq = &n
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	events, nextBeforeSeq, hasMore := s.eventBroker.ListHistoryBefore(r.Context(), jobID, 0, limit)
	items := make([]JobEvent, 0, len(events))
	for _, evt := range events {
		items = append(items, jobEventFromEvent(evt))
	}
	resp := JobEventListResponse{Items: items}
	if hasMore {
		n := nextBeforeSeq
		resp.NextBeforeSeq = &n
	}
	writeJSON(w, http.StatusOK, resp)
}

func jobEventFromEvent(evt event.Event) JobEvent {
	item := JobEvent{
		CreatedAt: evt.CreatedAt,
		JobId:     evt.JobID,
		Level:     evt.Level,
		Message:   evt.Message,
		Seq:       evt.Seq,
		Type:      evt.Type,
	}
	if evt.Metadata != nil {
		m := evt.Metadata
		item.Metadata = &m
	}
	if evt.Stage != "" {
		stage := evt.Stage
		item.Stage = &stage
	}
	return item
}

func parseAfterSeq(raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

func parseBeforeSeq(raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 {
		return 0, false
	}
	return v, true
}
