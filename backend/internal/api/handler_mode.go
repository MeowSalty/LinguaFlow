package api

import "net/http"

func (s *Server) handleMode(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"mode": s.mode})
}
