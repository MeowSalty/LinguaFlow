package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

var sseEventTypeReplacer = strings.NewReplacer("\r", "", "\n", "")

func (s *Server) handleTranslationJobStream(w http.ResponseWriter, r *http.Request) {
	authUser, ok := s.resolveAuthUser(r)
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := s.parseIntParam(w, r, chi.URLParam(r, "translationJobId"), "translationJobId")
	if !ok {
		return
	}
	if err := s.translationJobSvc.CheckJobAccess(r.Context(), authUser.User.ID, jobID); err != nil {
		s.writeTranslationJobServiceError(w, r, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeProblem(w, r, http.StatusInternalServerError, "internal_error", "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch := s.eventBroker.Subscribe(jobID)
	defer s.eventBroker.Unsubscribe(jobID, ch)

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// 从 ring buffer 回放历史事件
	var lastSeq int64
	var afterSeq int64
	if lastEventIDStr := r.Header.Get("Last-Event-ID"); lastEventIDStr != "" {
		// 重连：从上次断开的位置继续
		afterSeq, _ = strconv.ParseInt(lastEventIDStr, 10, 64)
	}
	replayed := s.eventBroker.Replay(jobID, afterSeq)
	for _, evt := range replayed {
		lastSeq = evt.Seq
		data, err := json.Marshal(evt)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", evt.Seq, sseEventTypeReplacer.Replace(evt.Type), string(data))
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if evt.Seq <= lastSeq {
				continue
			}
			lastSeq = evt.Seq
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", lastSeq, sseEventTypeReplacer.Replace(evt.Type), string(data))
			flusher.Flush()
		case <-time.After(30 * time.Second):
			fmt.Fprintf(w, ": keepalive %d\n\n", time.Now().Unix())
			flusher.Flush()
		}
	}
}
