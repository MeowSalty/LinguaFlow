package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

const (
	authTestSecret = "test-secret"
	authTestIssuer = "test-issuer"
)

func authTestServer(t *testing.T) (*Server, *ent.Client, *ent.User) {
	t.Helper()

	client := newTestEntClient(t)
	user, err := client.User.Create().
		SetUsername("testuser").
		SetPasswordHash("$2a$10$dummy").
		SetEmail("test@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	s := &Server{
		serverCfg: &config.ServerConfig{ServiceName: "test"},
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		entClient: client,
		authService: service.NewAuthService(client, service.AuthConfig{
			Secret: []byte(authTestSecret),
			Issuer: authTestIssuer,
		}, service.NewAdminService(client)),
	}
	return s, client, user
}

func requireAuthRequest(s *Server, rawToken string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if rawToken != "" {
		req.Header.Set("Authorization", "Bearer "+rawToken)
	}

	w := httptest.NewRecorder()
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, req)
	return w
}

func authTestToken(t *testing.T, user *ent.User, expiresAt time.Time, secret []byte) string {
	t.Helper()

	now := time.Now()
	claims := &service.AccessTokenClaims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    authTestIssuer,
			Subject:   fmt.Sprintf("user:%d", user.ID),
			IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	rawToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return rawToken
}

func TestRequireAuth(t *testing.T) {
	tests := []struct {
		name      string
		typeURN   string
		detail    string
		makeToken func(t *testing.T, client *ent.Client, user *ent.User) string
	}{
		{
			name:    "NoToken",
			typeURN: "urn:linguaflow:token-missing",
			detail:  "未提供认证凭据",
			makeToken: func(_ *testing.T, _ *ent.Client, _ *ent.User) string {
				return ""
			},
		},
		{
			name:    "ExpiredToken",
			typeURN: "urn:linguaflow:token-expired",
			detail:  "Token 已过期，请重新登录",
			makeToken: func(t *testing.T, _ *ent.Client, user *ent.User) string {
				return authTestToken(t, user, time.Now().Add(-time.Hour), []byte(authTestSecret))
			},
		},
		{
			name:    "InvalidToken",
			typeURN: "urn:linguaflow:token-invalid",
			detail:  "Token 无效或已失效",
			makeToken: func(t *testing.T, _ *ent.Client, user *ent.User) string {
				return authTestToken(t, user, time.Now().Add(time.Hour), []byte("wrong-secret"))
			},
		},
		{
			name:    "InactiveUser",
			typeURN: "urn:linguaflow:user-inactive",
			detail:  "用户已被禁用",
			makeToken: func(t *testing.T, client *ent.Client, user *ent.User) string {
				inactiveUser, err := client.User.UpdateOneID(user.ID).SetActive(false).Save(context.Background())
				if err != nil {
					t.Fatalf("deactivate user: %v", err)
				}
				return authTestToken(t, inactiveUser, time.Now().Add(time.Hour), []byte(authTestSecret))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, client, user := authTestServer(t)
			w := requireAuthRequest(s, tt.makeToken(t, client, user))

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
			}

			var problem struct {
				Type   string `json:"type"`
				Title  string `json:"title"`
				Detail string `json:"detail"`
				Status int    `json:"status"`
			}
			if err := json.NewDecoder(w.Body).Decode(&problem); err != nil {
				t.Fatalf("decode problem: %v", err)
			}
			if problem.Status != http.StatusUnauthorized {
				t.Errorf("problem status = %d, want %d", problem.Status, http.StatusUnauthorized)
			}
			if problem.Type != tt.typeURN {
				t.Errorf("type = %q, want %q", problem.Type, tt.typeURN)
			}
			if problem.Title != "unauthorized" {
				t.Errorf("title = %q, want unauthorized", problem.Title)
			}
			if problem.Detail != tt.detail {
				t.Errorf("detail = %q, want %q", problem.Detail, tt.detail)
			}
		})
	}
}
