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

func parseCursorPagination(w http.ResponseWriter, r *http.Request, defaultLimit, maxLimit int) (cursorPageRequest, bool) {
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
			writeProblem(w, http.StatusBadRequest, "invalid_query_parameter", "limit 必须是有效正整数且不超过上限")
			return cursorPageRequest{}, false
		}
		limit = v
	}
	afterID := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			writeProblem(w, http.StatusBadRequest, "invalid_query_parameter", "cursor 必须是有效非负整数")
			return cursorPageRequest{}, false
		}
		afterID = v
	}
	return cursorPageRequest{AfterID: afterID, Limit: limit}, true
}

func formatCursor(cursor int) string {
	if cursor <= 0 {
		return ""
	}
	return strconv.Itoa(cursor)
}
