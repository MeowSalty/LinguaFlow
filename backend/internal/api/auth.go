package api

import (
	"context"
	"errors"
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

// errNoToken 表示请求未携带任何凭据(Authorization 头与 access_token 查询参数均缺失)。
var errNoToken = errors.New("no token provided")

// writeAuthProblem 将认证失败原因写入 401 响应,detail 区分
// 未提供凭据 / Token 过期 / Token 无效 / 用户被禁用,便于用户与排障者定位;
// 非认证语义的意外错误(如 DB 故障)交给 writeServiceError 兜底,不伪装成 401。
func (s *Server) writeAuthProblem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNoToken):
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "未提供认证凭据")
	case errors.Is(err, service.ErrTokenExpired):
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "Token 已过期，请重新登录")
	case errors.Is(err, service.ErrTokenInvalid):
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "Token 无效或已失效")
	case errors.Is(err, service.ErrUserInactive):
		s.writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "用户已被禁用")
	default:
		s.writeServiceError(w, r, err)
	}
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.resolveAuthUser(r)
		if err != nil {
			s.writeAuthProblem(w, r, err)
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
// 支持 Authorization 头和 access_token 查询参数(用于无法设置自定义头的 SSE EventSource)。
// 失败时返回错误:errNoToken 表示未提供凭据;其余为 service 层 sentinel error
// (ErrTokenExpired / ErrTokenInvalid / ErrUserInactive),供调用方分流处理。
func (s *Server) resolveAuthUser(r *http.Request) (authenticatedUser, error) {
	if user, ok := s.localAuthUser(); ok {
		return user, nil
	}

	// 优先从已有 context 中获取(来自 requireAuth 中间件)
	if user, ok := authUserFromContext(r.Context()); ok {
		return user, nil
	}

	// 尝试 Authorization 头
	rawToken := bearerToken(r.Header.Get("Authorization"))
	if rawToken == "" {
		// 回退到查询参数
		rawToken = r.URL.Query().Get("access_token")
	}
	if rawToken == "" {
		return authenticatedUser{}, errNoToken
	}

	account, claims, err := s.authService.ResolveUserFromAccessToken(r.Context(), rawToken)
	if err != nil {
		return authenticatedUser{}, err
	}
	return authenticatedUser{User: account, Claims: claims}, nil
}
