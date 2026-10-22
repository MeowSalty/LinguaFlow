package api

import (
	"encoding/json"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/getkin/kin-openapi/openapi3"
	"io"
	"net/http"
	"sync"
)

var retirementDocument struct {
	sync.Once
	doc *openapi3.T
	err error
}

// Validate the nested, required generations as well as duplicate/unknown keys.
// Decoding into int64 alone would silently accept an omitted zero generation.
func (s *Server) decodeRetirementJSON(w http.ResponseWriter, r *http.Request, out any, name string) bool {
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		s.writeStorageError(w, r, service.ErrStorageTooLarge)
		return false
	}
	if err = authrules.ValidateJSON(data); err != nil {
		s.writeStorageError(w, r, service.ErrInvalidInput)
		return false
	}
	retirementDocument.Do(func() { retirementDocument.doc, retirementDocument.err = GetSwagger() })
	if retirementDocument.err != nil {
		s.writeStorageError(w, r, service.ErrInvalidInput)
		return false
	}
	ref := retirementDocument.doc.Components.Schemas[name]
	if ref == nil || ref.Value == nil {
		s.writeStorageError(w, r, service.ErrInvalidInput)
		return false
	}
	var value any
	if err = json.Unmarshal(data, &value); err == nil {
		err = ref.Value.VisitJSON(value)
	}
	if err == nil {
		err = json.Unmarshal(data, out)
	}
	if err != nil {
		s.writeStorageError(w, r, service.ErrInvalidInput)
		return false
	}
	return true
}
