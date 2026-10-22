package api

import (
	"errors"
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

type authUserResponse struct {
	ID          int     `json:"id"`
	Username    string  `json:"username"`
	Email       *string `json:"email"`
	DisplayName string  `json:"display_name,omitempty"`
	Role        string  `json:"role"`
	Active      bool    `json:"active"`
}

type authSessionResponse struct {
	AccessToken      string           `json:"access_token"`
	RefreshToken     string           `json:"refresh_token"`
	TokenType        string           `json:"token_type"`
	ExpiresAt        string           `json:"expires_at"`
	RefreshExpiresAt string           `json:"refresh_expires_at"`
	User             authUserResponse `json:"user"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fields, ok := s.decodeAccountFields(w, r, "username", "password", "email", "display_name")
	if !ok {
		return
	}
	if fields["username"] == nil || fields["password"] == nil {
		s.writeServiceError(w, r, service.ErrInvalidInput)
		return
	}
	session, err := s.authService.Register(r.Context(), service.RegisterInput{
		Username:    *fields["username"],
		Password:    *fields["password"],
		Email:       organizationStringField(fields["email"]),
		DisplayName: organizationStringField(fields["display_name"]),
	})
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newAuthSessionResponse(session))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fields, ok := s.decodeAccountFields(w, r, "identifier", "password")
	if !ok {
		return
	}
	if fields["identifier"] == nil || fields["password"] == nil {
		s.writeServiceError(w, r, service.ErrInvalidInput)
		return
	}
	if !s.passwordAttemptAllowed(w, r, *fields["identifier"]) {
		return
	}
	session, err := s.authService.Login(r.Context(), service.LoginInput{
		Identifier: *fields["identifier"],
		Password:   *fields["password"],
	})
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) || errors.Is(err, service.ErrInvalidCredentials) || errors.Is(err, service.ErrUserInactive) {
			err = service.ErrInvalidCredentials
		}
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthSessionResponse(session))
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fields, ok := s.decodeAccountFields(w, r, "refresh_token")
	if !ok {
		return
	}
	if fields["refresh_token"] == nil {
		s.writeServiceError(w, r, service.ErrInvalidInput)
		return
	}
	session, err := s.authService.Refresh(r.Context(), *fields["refresh_token"])
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthSessionResponse(session))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	fields, ok := s.decodeAccountFields(w, r, "refresh_token")
	if !ok {
		return
	}
	if fields["refresh_token"] == nil {
		s.writeServiceError(w, r, service.ErrInvalidInput)
		return
	}
	authUser, ok := authUserFromContext(r.Context())
	if !ok {
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	if err := s.authService.Logout(r.Context(), authUser.User.ID, *fields["refresh_token"]); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func newAuthSessionResponse(session *service.Session) authSessionResponse {
	return authSessionResponse{
		AccessToken:      session.AccessToken,
		RefreshToken:     session.RefreshToken,
		TokenType:        "Bearer",
		ExpiresAt:        timeutil.Format(session.AccessExpiresAt),
		RefreshExpiresAt: timeutil.Format(session.RefreshExpiresAt),
		User: authUserResponse{
			ID:          session.User.ID,
			Username:    session.User.Username,
			Email:       nullableAccountEmail(session.User.Email),
			DisplayName: session.User.DisplayName,
			Role:        session.User.Role,
			Active:      session.User.Active,
		},
	}
}

func (s *Server) handleAuthCapabilities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.isLocal() {
		writeJSON(w, http.StatusOK, service.AuthCapabilities{Mode: "local", Connections: []service.PublicAuthConnectionCapability{}, LocalAccount: service.LocalAccountCapabilities{Signup: service.SignupPolicyRequirements{EmailRequirement: "optional", PasswordRequirement: "required"}, SignupRequirements: []string{"local_password"}}})
		return
	}
	capabilities, err := s.authService.Capabilities(r.Context())
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, capabilities)
}
