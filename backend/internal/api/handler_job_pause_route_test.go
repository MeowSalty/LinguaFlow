package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRouter_JobPauseResume_RequireAuth 路由级鉴权回归：pause/resume 端点
// 曾缺失 requireAuth 包装导致恒 401（handler 直接依赖 authUserFromContext，
// 但 ServerInterface 方法未包装、全局中间件无认证）。本测试走 newRouter
// 完整挂载链验证：
//  1. 无凭据：requireAuth 拦截返回 401；
//  2. 有效凭据：通过认证层到达 handler（非数字 jobId 触发 400 参数校验
//     而非 401，且不触碰 jobSvc 依赖）。
func TestRouter_JobPauseResume_RequireAuth(t *testing.T) {
	s, _, user := authTestServer(t)
	router := s.newRouter()

	// 无凭据：两个端点都应被认证层拦截。
	for _, path := range []string{"/api/v1/jobs/123/pause", "/api/v1/jobs/123/resume"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("POST %s 无凭据 = %d, want 401（认证拦截）", path, w.Code)
		}
	}

	// 有效凭据：认证通过后到达 handler——非数字 jobId 走参数校验 400，
	// 证明不再被 401 挡在门外（也不会因 jobSvc 缺失而 panic）。
	token := authTestToken(t, user, time.Now().Add(time.Hour), []byte(authTestSecret))
	for _, path := range []string{"/api/v1/jobs/not-a-number/pause", "/api/v1/jobs/not-a-number/resume"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code == http.StatusUnauthorized {
			t.Errorf("POST %s 有效凭据 = 401, want 非 401（认证层应放行）", path)
		}
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST %s 有效凭据 = %d, want 400（非数字 jobId 参数校验）", path, w.Code)
		}
	}
}
