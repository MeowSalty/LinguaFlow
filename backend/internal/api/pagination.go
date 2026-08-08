package api

import (
	"net/http"
	"strconv"
	"strings"
)

type cursorPageRequest struct {
	AfterID int
	Limit   int
}

func (s *Server) parseCursorPagination(w http.ResponseWriter, r *http.Request, defaultLimit, maxLimit int) (cursorPageRequest, bool) {
	limit, ok := s.parseLimitParam(w, r, defaultLimit, maxLimit)
	if !ok {
		return cursorPageRequest{}, false
	}
	afterID := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "cursor 必须是有效非负整数")
			return cursorPageRequest{}, false
		}
		afterID = v
	}
	return cursorPageRequest{AfterID: afterID, Limit: limit}, true
}

// parseLimitParam parses the optional "limit" query parameter with shared
// semantics across paginated endpoints. When absent it returns defaultLimit
// (which itself falls back to 50); present values must be positive and at most
// maxLimit (which falls back to 100). This is the single source of truth for
// limit validation so bounds cannot drift across handlers.
func (s *Server) parseLimitParam(w http.ResponseWriter, r *http.Request, defaultLimit, maxLimit int) (int, bool) {
	if defaultLimit <= 0 {
		defaultLimit = 50
	}
	if maxLimit <= 0 {
		maxLimit = 100
	}
	limit := defaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 || v > maxLimit {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_query_parameter", "limit 必须是有效正整数且不超过上限")
			return 0, false
		}
		limit = v
	}
	return limit, true
}

func formatCursor(cursor int) string {
	if cursor <= 0 {
		return ""
	}
	return strconv.Itoa(cursor)
}
