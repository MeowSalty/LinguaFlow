package authoidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

type testProvider struct {
	server          *httptest.Server
	key             *rsa.PrivateKey
	nonce           string
	authTime        time.Time
	userinfoSubject string
	tokenError      bool
	claimsOverride  map[string]any
	tokenDelay      time.Duration
	wrongSigningKey *rsa.PrivateKey
}

func newTestProvider(t *testing.T) *testProvider {
	t.Helper()
	p := &testProvider{nonce: "nonce-1", authTime: time.Now().Add(-time.Minute), userinfoSubject: "subject-1"}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p.key = key
	mux := http.NewServeMux()
	p.server = httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"issuer": p.server.URL, "authorization_endpoint": p.server.URL + "/authorize", "token_endpoint": p.server.URL + "/token",
			"jwks_uri": p.server.URL + "/jwks", "userinfo_endpoint": p.server.URL + "/userinfo", "response_types_supported": []string{"code"},
			"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"},
			"scopes_supported": []string{"openid", "profile"}, "code_challenge_methods_supported": []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		e := big.NewInt(int64(p.key.PublicKey.E)).Bytes()
		writeJSON(t, w, map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(p.key.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if p.tokenDelay > 0 {
			select {
			case <-time.After(p.tokenDelay):
			case <-r.Context().Done():
				return
			}
		}
		if p.tokenError {
			http.Error(w, "private-token-error-secret", http.StatusBadGateway)
			return
		}
		if user, pass, ok := r.BasicAuth(); !ok || user != "client-1" || pass != "secret-1" {
			t.Errorf("missing client_secret_basic: %q %q %v", user, pass, ok)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("code_verifier") != strings.Repeat("v", 43) {
			t.Errorf("code_verifier=%q", r.Form.Get("code_verifier"))
		}
		now := time.Now()
		claims := jwt.MapClaims{"iss": p.server.URL, "sub": "subject-1", "aud": "client-1", "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "auth_time": p.authTime.Unix(), "nonce": p.nonce}
		for key, value := range p.claimsOverride {
			claims[key] = value
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "test-key"
		key := p.key
		if p.wrongSigningKey != nil {
			key = p.wrongSigningKey
		}
		raw, err := token.SignedString(key)
		if err != nil {
			t.Error(err)
			return
		}
		writeJSON(t, w, map[string]any{"access_token": "access-1", "token_type": "Bearer", "id_token": raw, "expires_in": 300})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"sub": p.userinfoSubject, "name": "Test User", "preferred_username": "tester", "email": "tester@example.test"})
	})
	return p
}

func (p *testProvider) close() { p.server.Close() }
func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func testClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(config.AuthDeploymentConfig{AllowHTTPLoopback: true, AllowedCIDRs: []string{"127.0.0.0/8"}, RequestTimeout: 2 * time.Second, MaxResponseBytes: 1 << 20, RateLimitPerMinute: 60})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testConfig(p *testProvider) Config {
	return Config{Issuer: p.server.URL, ClientID: "client-1", ClientSecret: "secret-1", ClientAuthMethod: "basic", UserInfoEnabled: true}
}

func TestCheckBuildAuthorizationAndExchange(t *testing.T) {
	p := newTestProvider(t)
	defer p.close()
	c := testClient(t)
	cfg := testConfig(p)
	meta, err := c.Check(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.SupportsPKCES256 || meta.AuthStyle != "basic" || meta.UserInfoEndpoint == "" {
		t.Fatalf("metadata=%+v", meta)
	}
	auth, err := c.BuildAuthorization(context.Background(), cfg, AuthorizationRequest{RedirectURL: "http://localhost/callback", State: "state-1", Nonce: p.nonce, CodeVerifier: strings.Repeat("v", 43), Fresh: true})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("state") != "state-1" || q.Get("nonce") != p.nonce || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != auth.CodeChallenge || q.Get("max_age") != "0" {
		t.Fatalf("authorization query=%v", q)
	}
	identity, err := c.ExchangeAndVerify(context.Background(), cfg, ExchangeRequest{RedirectURL: "http://localhost/callback", Code: "code-1", CodeVerifier: strings.Repeat("v", 43), ExpectedNonce: p.nonce, RequireFresh: true, MaxAge: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Issuer != p.server.URL || identity.Subject != "subject-1" || identity.DisplayName != "Test User" || identity.Email != "tester@example.test" {
		t.Fatalf("identity=%+v", identity)
	}
}

func TestExchangeRejectsNonceFreshnessAndUserInfoSubject(t *testing.T) {
	p := newTestProvider(t)
	defer p.close()
	c := testClient(t)
	cfg := testConfig(p)
	request := ExchangeRequest{RedirectURL: "http://localhost/callback", Code: "code-1", CodeVerifier: strings.Repeat("v", 43), ExpectedNonce: "wrong"}
	if _, err := c.ExchangeAndVerify(context.Background(), cfg, request); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("nonce error=%v", err)
	}
	request.ExpectedNonce = p.nonce
	request.RequireFresh = true
	request.MaxAge = 30 * time.Second
	if _, err := c.ExchangeAndVerify(context.Background(), cfg, request); !errors.Is(err, ErrFreshAuth) {
		t.Fatalf("freshness error=%v", err)
	}
	request.RequireFresh = false
	p.userinfoSubject = "other"
	if _, err := c.ExchangeAndVerify(context.Background(), cfg, request); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("userinfo subject error=%v", err)
	}
}

func TestProviderFailuresAreRedacted(t *testing.T) {
	p := newTestProvider(t)
	defer p.close()
	c := testClient(t)
	cfg := testConfig(p)
	p.tokenError = true
	_, err := c.ExchangeAndVerify(context.Background(), cfg, ExchangeRequest{RedirectURL: "http://localhost/callback", Code: "code-1", CodeVerifier: strings.Repeat("v", 43), ExpectedNonce: p.nonce})
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private-token-error-secret") {
		t.Fatalf("unsafe error=%v", err)
	}
}

func TestNetworkPolicyBlocksPrivateProviderWithoutExplicitAllowlist(t *testing.T) {
	p := newTestProvider(t)
	defer p.close()
	c, err := New(config.AuthDeploymentConfig{AllowHTTPLoopback: true, RequestTimeout: time.Second, MaxResponseBytes: 1 << 20, RateLimitPerMinute: 60})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Check(context.Background(), testConfig(p)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("private provider error=%v", err)
	}
}

func TestInvalidVerifierAndHTTPPolicy(t *testing.T) {
	p := newTestProvider(t)
	defer p.close()
	c := testClient(t)
	cfg := testConfig(p)
	if _, err := c.BuildAuthorization(context.Background(), cfg, AuthorizationRequest{RedirectURL: "http://localhost/callback", State: "s", Nonce: "n", CodeVerifier: "short"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("verifier error=%v", err)
	}
	strict, err := New(config.AuthDeploymentConfig{RequestTimeout: time.Second, MaxResponseBytes: 1 << 20, RateLimitPerMinute: 60, AllowedCIDRs: []string{"127.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict.Check(context.Background(), cfg); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("http policy error=%v", err)
	}
}
