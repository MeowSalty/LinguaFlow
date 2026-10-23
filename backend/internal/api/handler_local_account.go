package api

import (
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func (s *Server) GetLocalAccount(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	out, err := s.externalAuth.GetLocalAccount(r.Context(), *actor)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) UpdateLocalAccount(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	var in service.LocalAccountPatchInput
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.UpdateLocalAccount(r.Context(), *actor, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetLocalAccountAttributes(w http.ResponseWriter, r *http.Request, params GetLocalAccountAttributesParams) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	out, err := s.externalAuth.LocalAccountAttributes(r.Context(), *actor, params.ExpectedRevision)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) PreviewLocalAccount(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	var in service.LocalAccountPreviewInput
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.PreviewLocalAccount(r.Context(), *actor, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) GetLocalAccountImpact(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	var in service.LocalAccountCandidateInput
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.LocalAccountImpact(r.Context(), *actor, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
