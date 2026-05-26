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

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
