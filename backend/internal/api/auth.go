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
		user, ok := s.resolveAuthUser(r)
		if !ok {
			s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "缺少 Bearer Token")
			return
		}
		ctx := context.WithValue(r.Context(), authContextKey{}, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authUser, ok := authUserFromContext(r.Context())
		if !ok || authUser.User.Role != service.SystemRoleAdmin {
			s.writeProblem(w, r, http.StatusForbidden, "forbidden", "需要管理员权限")
			return
		}
		next.ServeHTTP(w, r)
	}))
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

// resolveAuthUser 从请求中提取认证用户。
// 支持 Authorization 头和 access_token 查询参数（用于无法设置自定义头的 SSE EventSource）。
func (s *Server) resolveAuthUser(r *http.Request) (authenticatedUser, bool) {
	if user, ok := s.localAuthUser(); ok {
		return user, true
	}

	// 优先从已有 context 中获取（来自 requireAuth 中间件）
	if user, ok := authUserFromContext(r.Context()); ok {
		return user, true
	}

	// 尝试 Authorization 头
	rawToken := bearerToken(r.Header.Get("Authorization"))
	if rawToken == "" {
		// 回退到查询参数
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
