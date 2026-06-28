package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type authContextKey struct{}

type authenticatedUser struct {
	User   *ent.User
	Claims *service.AccessTokenClaims
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawToken := bearerToken(r.Header.Get("Authorization"))
		if rawToken == "" {
			writeProblem(w, http.StatusUnauthorized, "unauthorized", "缺少 Bearer Token")
			return
		}
		account, claims, err := s.authService.ResolveUserFromAccessToken(r.Context(), rawToken)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		ctx := context.WithValue(r.Context(), authContextKey{}, authenticatedUser{User: account, Claims: claims})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func authUserFromContext(ctx context.Context) (authenticatedUser, bool) {
	v, ok := ctx.Value(authContextKey{}).(authenticatedUser)
	return v, ok
}

// authHandleFunc 将需要认证的 http.HandlerFunc 包装为 chi 路由可用的 http.HandlerFunc。
// 等效于 s.requireAuth(http.HandlerFunc(fn)) 但返回的是函数而非 Handler。
func (s *Server) authHandleFunc(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.requireAuth(http.HandlerFunc(fn)).ServeHTTP(w, r)
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

// resolveAuthUser extracts the authenticated user from the request.
// Supports both Authorization header and access_token query parameter
// (for SSE EventSource which cannot set custom headers).
func (s *Server) resolveAuthUser(r *http.Request) (authenticatedUser, bool) {
	// Try existing context first (from requireAuth middleware)
	if user, ok := authUserFromContext(r.Context()); ok {
		return user, true
	}

	// Try Authorization header
	rawToken := bearerToken(r.Header.Get("Authorization"))
	if rawToken == "" {
		// Fall back to query parameter
		rawToken = r.URL.Query().Get("access_token")
	}
	if rawToken == "" {
		return authenticatedUser{}, false
	}

	account, claims, err := s.authService.ResolveUserFromAccessToken(r.Context(), rawToken)
	if err != nil {
		return authenticatedUser{}, false
	}
	return authenticatedUser{User: account, Claims: claims}, true
}
