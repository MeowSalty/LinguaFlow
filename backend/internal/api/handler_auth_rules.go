package api

import (
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func (s *Server) GetAuthConnectionAttributes(w http.ResponseWriter, r *http.Request, id string, p GetAuthConnectionAttributesParams) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	in := service.AuthConnectionCandidateInput{ExpectedRevision: p.ExpectedRevision}
	out, err := s.externalAuth.ConnectionAttributes(r.Context(), *actor, id, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) PreviewAuthConnectionAttributes(w http.ResponseWriter, r *http.Request, id string) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	var in service.AuthConnectionCandidateInput
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.ConnectionAttributes(r.Context(), *actor, id, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) PreviewAuthConnectionRules(w http.ResponseWriter, r *http.Request, id string) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	var in service.AuthRulesPreviewInput
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.PreviewConnectionRules(r.Context(), *actor, id, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
