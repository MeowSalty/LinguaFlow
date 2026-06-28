package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleTranslationJobStream(w http.ResponseWriter, r *http.Request) {
	authUser, ok := s.resolveAuthUser(r)
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	jobID, ok := parseIntParam(w, chi.URLParam(r, "translationJobId"), "translationJobId")
	if !ok {
		return
	}
	if err := s.translationJobSvc.CheckJobAccess(r.Context(), authUser.User.ID, jobID); err != nil {
		writeTranslationJobServiceError(w, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, http.StatusInternalServerError, "internal_error", "streaming not supported")
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

	lastEventID := 0

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			lastEventID++
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", lastEventID, evt.Type, string(data))
			flusher.Flush()
		case <-time.After(30 * time.Second):
			fmt.Fprintf(w, ": keepalive %d\n\n", time.Now().Unix())
			flusher.Flush()
		}
	}
}
