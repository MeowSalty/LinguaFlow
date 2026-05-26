package api

import (
	"net/http"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type authRequestRegister struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

type authRequestLogin struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type authRequestRefresh struct {
	RefreshToken string `json:"refresh_token"`
}

type authRequestLogout struct {
	RefreshToken string `json:"refresh_token"`
}

type authUserResponse struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role"`
	Active      bool   `json:"active"`
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
	var req authRequestRegister
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.authService.Register(r.Context(), service.RegisterInput{
		Username:    req.Username,
		Password:    req.Password,
		Email:       req.Email,
		DisplayName: req.DisplayName,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newAuthSessionResponse(session))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req authRequestLogin
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.authService.Login(r.Context(), service.LoginInput{
		Username: req.Username,
		Password: req.Password,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthSessionResponse(session))
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req authRequestRefresh
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := s.authService.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthSessionResponse(session))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req authRequestLogout
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, ok := authUserFromContext(r.Context()); !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "认证失败")
		return
	}
	if err := s.authService.Logout(r.Context(), req.RefreshToken); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func newAuthSessionResponse(session *service.Session) authSessionResponse {
	return authSessionResponse{
		AccessToken:      session.AccessToken,
		RefreshToken:     session.RefreshToken,
		TokenType:        "Bearer",
		ExpiresAt:        session.AccessExpiresAt.UTC().Format(time.RFC3339),
		RefreshExpiresAt: session.RefreshExpiresAt.UTC().Format(time.RFC3339),
		User: authUserResponse{
			ID:          session.User.ID,
			Username:    session.User.Username,
			Email:       session.User.Email,
			DisplayName: session.User.DisplayName,
			Role:        session.User.Role,
			Active:      session.User.Active,
		},
	}
}
