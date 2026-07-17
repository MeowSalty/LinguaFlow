package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type updateMeRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type organizationRequest struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

type orgMemberRequest struct {
	Username string `json:"username,omitempty"`
	Role     string `json:"role"`
}

type userResponse struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role"`
	Active      bool   `json:"active"`
}

type organizationResponse struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
}

type orgMembershipResponse struct {
	ID   int          `json:"id"`
	Role string       `json:"role"`
	User userResponse `json:"user"`
}

func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	account, err := s.userService.GetMe(r.Context(), authUser.User.ID)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(account))
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	var req updateMeRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	updated, err := s.userService.UpdateMe(r.Context(), authUser.User.ID, service.UpdateProfileInput{
		DisplayName: req.DisplayName,
		Email:       req.Email,
	})
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(updated))
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	var req changePasswordRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if err := s.userService.ChangeMyPassword(r.Context(), authUser.User.ID, req.CurrentPassword, req.NewPassword); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	orgs, err := s.userService.ListOrganizationsForUser(r.Context(), authUser.User.ID)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	resp := make([]organizationResponse, 0, len(orgs))
	for _, org := range orgs {
		resp = append(resp, toOrganizationResponse(org))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp})
}

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	var req organizationRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	org, err := s.userService.CreateOrganization(r.Context(), authUser.User.ID, service.CreateOrganizationInput{
		Name:        req.Name,
		Slug:        req.Slug,
		DisplayName: req.DisplayName,
		Description: req.Description,
	})
	if err != nil {
		s.writeUserServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toOrganizationResponse(org))
}

func (s *Server) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	orgID, ok := s.parseIntParam(w, r, chi.URLParam(r, "orgId"), "orgId")
	if !ok {
		return
	}
	org, err := s.userService.GetOrganization(r.Context(), authUser.User.ID, orgID)
	if err != nil {
		s.writeUserServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrganizationResponse(org))
}

func (s *Server) handleUpdateOrg(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	orgID, ok := s.parseIntParam(w, r, chi.URLParam(r, "orgId"), "orgId")
	if !ok {
		return
	}
	var req organizationRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	org, err := s.userService.UpdateOrganization(r.Context(), authUser.User.ID, orgID, service.CreateOrganizationInput{
		Name:        req.Name,
		Slug:        req.Slug,
		DisplayName: req.DisplayName,
		Description: req.Description,
	})
	if err != nil {
		s.writeUserServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrganizationResponse(org))
}

func (s *Server) handleListOrgMembers(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	orgID, ok := s.parseIntParam(w, r, chi.URLParam(r, "orgId"), "orgId")
	if !ok {
		return
	}
	members, err := s.userService.ListMembers(r.Context(), authUser.User.ID, orgID)
	if err != nil {
		s.writeUserServiceError(w, r, err)
		return
	}
	resp := make([]orgMembershipResponse, 0, len(members))
	for _, membership := range members {
		resp = append(resp, toOrgMembershipResponse(membership))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp})
}

func (s *Server) handleAddOrgMember(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	orgID, ok := s.parseIntParam(w, r, chi.URLParam(r, "orgId"), "orgId")
	if !ok {
		return
	}
	var req orgMemberRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	membership, err := s.userService.AddMember(r.Context(), authUser.User.ID, orgID, service.AddOrgMemberInput{
		Username: req.Username,
		Role:     req.Role,
	})
	if err != nil {
		s.writeUserServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toOrgMembershipResponse(membership))
}

func (s *Server) handleUpdateOrgMember(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	orgID, ok := s.parseIntParam(w, r, chi.URLParam(r, "orgId"), "orgId")
	if !ok {
		return
	}
	memberUserID, ok := s.parseIntParam(w, r, chi.URLParam(r, "userId"), "userId")
	if !ok {
		return
	}
	var req orgMemberRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	membership, err := s.userService.UpdateMemberRole(r.Context(), authUser.User.ID, orgID, memberUserID, service.UpdateOrgMemberRoleInput{Role: req.Role})
	if err != nil {
		s.writeUserServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrgMembershipResponse(membership))
}

func (s *Server) handleDeleteOrgMember(w http.ResponseWriter, r *http.Request) {
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	orgID, ok := s.parseIntParam(w, r, chi.URLParam(r, "orgId"), "orgId")
	if !ok {
		return
	}
	memberUserID, ok := s.parseIntParam(w, r, chi.URLParam(r, "userId"), "userId")
	if !ok {
		return
	}
	if err := s.userService.RemoveMember(r.Context(), authUser.User.ID, orgID, memberUserID); err != nil {
		s.writeUserServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeUserServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrForbidden):
		s.writeProblem(w, r, http.StatusForbidden, "forbidden", "没有权限执行该操作")
	case errors.Is(err, service.ErrOrganizationNotFound), errors.Is(err, service.ErrMembershipNotFound):
		s.writeProblem(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, service.ErrOrganizationExists), errors.Is(err, service.ErrOwnerRequired), errors.Is(err, service.ErrUserExists):
		s.writeProblem(w, r, http.StatusConflict, "conflict", err.Error())
	default:
		s.writeServiceError(w, r, err)
	}
}

func toUserResponse(account *ent.User) userResponse {
	return userResponse{
		ID:          account.ID,
		Username:    account.Username,
		Email:       account.Email,
		DisplayName: account.DisplayName,
		Role:        account.Role,
		Active:      account.Active,
	}
}

func toOrganizationResponse(org *ent.Organization) organizationResponse {
	return organizationResponse{
		ID:          org.ID,
		Name:        org.Name,
		Slug:        org.Slug,
		DisplayName: org.DisplayName,
		Description: org.Description,
	}
}

func toOrgMembershipResponse(m *ent.OrgMembership) orgMembershipResponse {
	resp := orgMembershipResponse{ID: m.ID, Role: m.Role}
	if m.Edges.User != nil {
		resp.User = toUserResponse(m.Edges.User)
	}
	return resp
}
