package authoauth2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authprotocol"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func testConfig(base string) Config {
	return Config{
		Authority: "provider-stable-authority", AuthorizationEndpoint: base + "/authorize",
		TokenEndpoint: base + "/token", ProfileEndpoint: base + "/profile",
		ClientID: "client:id +", ClientSecret: "secret: /+", ClientAuthMethod: "client_secret_basic",
		Scopes: []string{"profile", "read:user"}, SubjectMapping: SubjectMapping{Pointer: "/id", Type: "string"},
	}
}

func testClient(t *testing.T, options ...func(*config.AuthDeploymentConfig)) *Client {
	t.Helper()
	d := config.AuthDeploymentConfig{AllowHTTPLoopback: true, AllowedHosts: []string{"127.0.0.1"}, RequestTimeout: time.Second, MaxResponseBytes: 1 << 20}
	for _, option := range options {
		option(&d)
	}
	c, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.transport.Base.CloseIdleConnections)
	return c
}

func testExchange() authprotocol.ExchangeRequest {
	return authprotocol.ExchangeRequest{Code: "one-use-code +", CodeVerifier: strings.Repeat("v", 43), RedirectURL: "https://app.example.test/callback"}
}

func writeResponse(w http.ResponseWriter, status int, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = fmt.Fprint(w, body)
}

func TestAuthorizationCodePKCEAndState(t *testing.T) {
	c := testClient(t)
	cfg := testConfig("https://identity.example.test")
	cfg.AuthorizationEndpoint += "?tenant=workspace"
	r := authprotocol.AuthorizationRequest{State: "opaque state +&", CodeVerifier: strings.Repeat("v", 43), RedirectURL: "https://app.example.test/callback", Nonce: "not-an-oidc-request"}
	auth, err := c.BuildAuthorization(context.Background(), cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(r.CodeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	want := url.Values{
		"tenant": {"workspace"}, "response_type": {"code"}, "client_id": {cfg.ClientID},
		"redirect_uri": {r.RedirectURL}, "state": {r.State}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "scope": {"profile read:user"},
	}
	if !reflect.DeepEqual(u.Query(), want) || auth.CodeChallenge != challenge {
		t.Fatalf("authorization parameters = %v, challenge = %q; want %v", u.Query(), auth.CodeChallenge, want)
	}
}

func TestClientAuthenticationAndProfileBearer(t *testing.T) {
	for _, method := range []string{"client_secret_basic", "client_secret_post"} {
		t.Run(method, func(t *testing.T) {
			var tokenCalls, profileCalls atomic.Int32
			var cfg Config
			r := testExchange()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("Accept") != "application/json" {
					t.Error("missing JSON Accept header")
				}
				switch req.URL.Path {
				case "/token":
					tokenCalls.Add(1)
					if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
						t.Errorf("token request method/content type = %s / %s", req.Method, req.Header.Get("Content-Type"))
					}
					if err := req.ParseForm(); err != nil {
						t.Error(err)
					}
					want := url.Values{"grant_type": {"authorization_code"}, "code": {r.Code}, "redirect_uri": {r.RedirectURL}, "code_verifier": {r.CodeVerifier}}
					if method == "client_secret_basic" {
						user, pass, ok := req.BasicAuth()
						if !ok || user != url.QueryEscape(cfg.ClientID) || pass != url.QueryEscape(cfg.ClientSecret) {
							t.Errorf("Basic credentials were not form-encoded independently")
						}
					} else {
						want.Set("client_id", cfg.ClientID)
						want.Set("client_secret", cfg.ClientSecret)
						if req.Header.Get("Authorization") != "" {
							t.Error("client_secret_post also sent Authorization")
						}
					}
					if !reflect.DeepEqual(req.PostForm, want) || req.URL.RawQuery != "" {
						t.Errorf("token parameters = %v, query = %s; want %v", req.PostForm, req.URL.RawQuery, want)
					}
					writeResponse(w, http.StatusOK, "application/json; charset=utf-8", `{"access_token":"opaque-access-token","token_type":"bEaReR","id_token":"ignored"}`)
				case "/profile":
					profileCalls.Add(1)
					if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer opaque-access-token" || req.URL.RawQuery != "" {
						t.Errorf("profile request did not use a Bearer header exclusively")
					}
					writeResponse(w, http.StatusOK, "application/json", `{"id":"stable-user","name":"profile-name"}`)
				default:
					t.Errorf("unexpected request: %s", req.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			cfg = testConfig(server.URL)
			cfg.ClientAuthMethod = method
			identity, err := testClient(t).ExchangeAndVerify(context.Background(), cfg, r)
			if err != nil {
				t.Fatal(err)
			}
			if identity.Protocol != "oauth2" || identity.Authority != cfg.Authority || identity.Subject != "stable-user" || !identity.AuthTime.IsZero() {
				t.Fatalf("identity = %+v", identity)
			}
			if len(identity.Sources) != 1 || string(identity.Sources["oauth2_profile"]) != `{"id":"stable-user","name":"profile-name"}` {
				t.Fatalf("unexpected identity sources: %v", identity.Sources)
			}
			if tokenCalls.Load() != 1 || profileCalls.Load() != 1 {
				t.Fatalf("request counts: token=%d profile=%d", tokenCalls.Load(), profileCalls.Load())
			}
		})
	}
}

func TestTokenResponseValidation(t *testing.T) {
	tests := []struct {
		name, body, contentType string
		status                  int
		want                    error
	}{
		{"oauth error with token", `{"error":"invalid_grant","access_token":"secret","token_type":"Bearer"}`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"duplicate access token", `{"access_token":"first","access_token":"second","token_type":"Bearer"}`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"duplicate nested property", `{"access_token":"secret","token_type":"Bearer","extra":{"id":1,"id":2}}`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"non JSON media type", `{"access_token":"secret","token_type":"Bearer"}`, "text/plain", 200, authprotocol.ErrInvalidToken},
		{"malformed JSON", `{"access_token":`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"trailing JSON", `{"access_token":"secret","token_type":"Bearer"}{}`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"JSON array", `[]`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"JSON null", `null`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"missing token", `{"token_type":"Bearer"}`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"numeric token", `{"access_token":123,"token_type":"Bearer"}`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"empty token", `{"access_token":"","token_type":"Bearer"}`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"header injection", `{"access_token":"a\r\nb","token_type":"Bearer"}`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"missing token type", `{"access_token":"secret"}`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"unsupported token type", `{"access_token":"secret","token_type":"MAC"}`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"numeric token type", `{"access_token":"secret","token_type":123}`, "application/json", 200, authprotocol.ErrInvalidToken},
		{"client error", `{"error":"secret-provider-error"}`, "application/json", 400, authprotocol.ErrInvalidToken},
		{"rate limit", "secret-provider-error", "text/plain", 429, authprotocol.ErrUnavailable},
		{"server error", "secret-provider-error", "text/plain", 503, authprotocol.ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var profileCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/token" {
					profileCalls.Add(1)
				}
				writeResponse(w, tt.status, tt.contentType, tt.body)
			}))
			defer server.Close()
			_, err := testClient(t).ExchangeAndVerify(context.Background(), testConfig(server.URL), testExchange())
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v; want %v", err, tt.want)
			}
			if profileCalls.Load() != 0 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid token response leaked or triggered profile request: calls=%d err=%v", profileCalls.Load(), err)
			}
		})
	}
}

func TestProfileSubjectMapping(t *testing.T) {
	tests := []struct {
		name, body, pointer, kind, want string
	}{
		{"string preserves leading zeros", `{"id":"0009007199254740993"}`, "/id", "string", "0009007199254740993"},
		{"string preserves whitespace and case", `{"id":" User ABC "}`, "/id", "string", " User ABC "},
		{"string preserves Unicode", `{"id":"用户-α"}`, "/id", "string", "用户-α"},
		{"integer beyond float precision", `{"id":9007199254740993}`, "/id", "integer", "9007199254740993"},
		{"integer beyond int64", `{"id":1234567890123456789012345678901234567890}`, "/id", "integer", "1234567890123456789012345678901234567890"},
		{"negative integer", `{"id":-9007199254740993}`, "/id", "integer", "-9007199254740993"},
		{"canonical negative zero", `{"id":-0}`, "/id", "integer", "0"},
		{"escaped pointer", `{"a/b":{"~key":[{"id":"nested"}]}}`, "/a~1b/~0key/0/id", "string", "nested"},
		{"string rejects number", `{"id":123}`, "/id", "string", ""},
		{"integer rejects string", `{"id":"123"}`, "/id", "integer", ""},
		{"integer rejects decimal", `{"id":123.0}`, "/id", "integer", ""},
		{"integer rejects exponent", `{"id":1e3}`, "/id", "integer", ""},
		{"empty string", `{"id":""}`, "/id", "string", ""},
		{"missing subject", `{"name":"user"}`, "/id", "string", ""},
		{"null subject", `{"id":null}`, "/id", "string", ""},
		{"boolean subject", `{"id":true}`, "/id", "string", ""},
		{"object subject", `{"id":{}}`, "/id", "string", ""},
		{"duplicate subject", `{"id":"first","id":"second"}`, "/id", "string", ""},
		{"duplicate escaped subject", `{"id":"first","\u0069d":"second"}`, "/id", "string", ""},
		{"profile trailing JSON", `{"id":"user"}{}`, "/id", "string", ""},
		{"profile array", `[{"id":"user"}]`, "/0/id", "string", ""},
		{"oversized string subject", `{"id":"` + strings.Repeat("x", 8193) + `"}`, "/id", "string", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := tt.body
				if r.URL.Path == "/token" {
					body = `{"access_token":"opaque-access-token","token_type":"Bearer"}`
				}
				writeResponse(w, http.StatusOK, "application/json", body)
			}))
			defer server.Close()
			cfg := testConfig(server.URL)
			cfg.SubjectMapping = SubjectMapping{Pointer: tt.pointer, Type: tt.kind}
			identity, err := testClient(t).ExchangeAndVerify(context.Background(), cfg, testExchange())
			if tt.want == "" {
				if !errors.Is(err, authprotocol.ErrInvalidToken) {
					t.Fatalf("identity=%+v error=%v; want invalid response", identity, err)
				}
			} else if err != nil || identity.Subject != tt.want || string(identity.Sources["oauth2_profile"]) != tt.body {
				t.Fatalf("subject=%q error=%v; want %q with unchanged source", identity.Subject, err, tt.want)
			}
		})
	}
}

func TestExchangeNetworkBoundaries(t *testing.T) {
	for _, stage := range []string{"token", "profile"} {
		for _, failure := range []string{"redirect", "timeout", "response size", "non JSON"} {
			t.Run(stage+"/"+failure, func(t *testing.T) {
				var redirected atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					redirected.Add(1)
					writeResponse(w, http.StatusOK, "application/json", `{"id":"redirected"}`)
				}))
				defer target.Close()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/"+stage {
						switch failure {
						case "redirect":
							http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
						case "timeout":
							select {
							case <-r.Context().Done():
							case <-time.After(400 * time.Millisecond):
							}
						case "response size":
							writeResponse(w, http.StatusOK, "application/json", `{"id":"`+strings.Repeat("x", 512)+`"}`)
						case "non JSON":
							writeResponse(w, http.StatusOK, "text/plain", `{"id":"user"}`)
						}
						return
					}
					writeResponse(w, http.StatusOK, "application/json", `{"access_token":"opaque-access-token","token_type":"Bearer"}`)
				}))
				defer server.Close()
				client := testClient(t, func(d *config.AuthDeploymentConfig) {
					d.RequestTimeout = 100 * time.Millisecond
					d.MaxResponseBytes = 128
				})
				_, err := client.ExchangeAndVerify(context.Background(), testConfig(server.URL), testExchange())
				want := authprotocol.ErrUnavailable
				if failure == "redirect" || failure == "non JSON" {
					want = authprotocol.ErrInvalidToken
				}
				if !errors.Is(err, want) || redirected.Load() != 0 {
					t.Fatalf("error=%v redirected=%d; want %v with no redirected request", err, redirected.Load(), want)
				}
			})
		}
	}
}

func TestOAuthConfigurationRejectsProtocolOverride(t *testing.T) {
	c := testClient(t)
	for _, name := range []string{"response_type", "client_id", "client_secret", "redirect_uri", "scope", "state", "code", "code_verifier", "code_challenge", "code_challenge_method", "grant_type", "access_token"} {
		for _, endpoint := range []string{"authorization", "token", "profile"} {
			t.Run(endpoint+"/"+name, func(t *testing.T) {
				cfg := testConfig("https://identity.example.test")
				value := "https://identity.example.test/endpoint?" + name + "=override"
				switch endpoint {
				case "authorization":
					cfg.AuthorizationEndpoint = value
				case "token":
					cfg.TokenEndpoint = value
				case "profile":
					cfg.ProfileEndpoint = value
				}
				if _, err := c.Check(context.Background(), cfg); !errors.Is(err, authprotocol.ErrInvalidConfig) {
					t.Fatalf("error=%v; want invalid config", err)
				}
			})
		}
	}
}

func TestOAuthRejectsFreshAuthenticationAndInvalidPKCE(t *testing.T) {
	c := testClient(t)
	cfg := testConfig("https://identity.example.test")
	r := testExchange()
	r.RequireFresh = true
	if _, err := c.ExchangeAndVerify(context.Background(), cfg, r); !errors.Is(err, authprotocol.ErrFreshAuth) {
		t.Fatalf("fresh authentication error=%v", err)
	}
	for _, verifier := range []string{"", strings.Repeat("v", 42), strings.Repeat("v", 129), strings.Repeat("v", 42) + "+"} {
		if _, err := c.BuildAuthorization(context.Background(), cfg, authprotocol.AuthorizationRequest{State: "state", RedirectURL: r.RedirectURL, CodeVerifier: verifier}); !errors.Is(err, authprotocol.ErrInvalidConfig) {
			t.Fatalf("verifier=%q error=%v", verifier, err)
		}
	}
	for _, d := range []config.AuthDeploymentConfig{{RequestTimeout: -1}, {MaxResponseBytes: -1}} {
		if _, err := New(d); !errors.Is(err, authprotocol.ErrInvalidConfig) {
			t.Fatalf("invalid deployment error=%v", err)
		}
	}
}
