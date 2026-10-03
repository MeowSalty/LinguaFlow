package api

import "net/http"

func (s *Server) GetStorageDiagnostics(w http.ResponseWriter, r *http.Request, params GetStorageDiagnosticsParams) {
	s.storageRequest(w, r, true, func(w http.ResponseWriter, r *http.Request, actor int) {
		cursor, limit := 0, 50
		if params.Cursor != nil {
			cursor = *params.Cursor
		}
		if params.Limit != nil {
			limit = *params.Limit
		}
		result, err := s.storageSvc.Diagnostics(r.Context(), actor, cursor, limit)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
}
