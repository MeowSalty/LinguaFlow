package authoidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/golang-jwt/jwt/v5"
)

func TestOIDCVerifiedAttributeSources(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
		status             int
		invalid            bool
	}{
		{"valid", `{"sub":"subject-1","department":"engineering","groups":["ops"]}`, "", 200, false},
		{"missing", `{"sub":"subject-1"}`, "", 200, false},
		{"ordinary wrong type", `{"sub":"subject-1","name":12}`, "", 200, false},
		{"other subject", `{"sub":"other","department":"engineering"}`, "", 200, true},
		{"duplicate subject", `{"sub":"other","sub":"subject-1"}`, "", 200, true},
		{"duplicate attribute", `{"sub":"subject-1","department":"sales","department":"engineering"}`, "", 200, true},
		{"temporary", `private-failure`, "source_unavailable", 503, false},
		{"truncated", `{"sub":"subject-1"}`, "source_unavailable", 200, false},
		{"rate limited", `private-failure`, "source_unavailable", 429, false},
		{"scope", `private-failure`, "scope_missing", 403, false},
		{"redirect", ``, "", 302, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProvider(t)
			defer p.close()
			p.claimsOverride = map[string]any{"department": "engineering", "name": 17, "email_verified": true, "roles": []string{"admin"}}
			original := p.server.Config.Handler
			p.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/userinfo" {
					original.ServeHTTP(w, r)
					return
				}
				if r.Header.Get("Authorization") != "Bearer access-1" {
					t.Error("missing UserInfo bearer")
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.name == "truncated" {
					w.Header().Set("Content-Length", "1000")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			identity, err := testClient(t).ExchangeAndVerify(context.Background(), testConfig(p), ExchangeRequest{RedirectURL: "http://localhost/callback", Code: "code-1", CodeVerifier: strings.Repeat("v", 43), ExpectedNonce: p.nonce})
			if tc.invalid {
				if !errors.Is(err, ErrInvalidToken) || len(identity.Sources) != 0 {
					t.Fatalf("invalid identity released sources: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if identity.Subject != "subject-1" || len(identity.Sources["oidc_id_token"]) == 0 || identity.SourceErrors["oidc_userinfo"] != tc.reason {
				t.Fatalf("unexpected verified identity: %+v", identity)
			}
			if tc.reason != "" && len(identity.Sources["oidc_userinfo"]) != 0 {
				t.Fatal("failure produced UserInfo evidence")
			}
			f := authrules.Field{FieldID: "custom.department", Label: "Department", Type: "string", Source: "oidc_userinfo", Pointer: "/department"}
			attrs, e := authrules.Extract("oidc", []authrules.Field{f}, identity.Sources, identity.SourceErrors, time.Now().UTC())
			if e != nil {
				t.Fatal(e)
			}
			if attrs["external.name"].Status != "unknown" {
				t.Fatal("wrong ordinary type did not remain unknown")
			}
			if tc.reason != "" && attrs[f.FieldID].Reason != tc.reason {
				t.Fatal("source reason lost")
			}
		})
	}
}

func TestOIDCDuplicateSignedClaimsRejected(t *testing.T) {
	p := newTestProvider(t)
	defer p.close()
	original := p.server.Config.Handler
	p.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			original.ServeHTTP(w, r)
			return
		}
		rr := httptest.NewRecorder()
		original.ServeHTTP(rr, r)
		var response map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Error(err)
			return
		}
		parts := strings.Split(response["id_token"].(string), ".")
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Error(err)
			return
		}
		payload = append(payload[:len(payload)-1], []byte(`,"sub":"subject-1"}`)...)
		input := parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload)
		signature, err := jwt.SigningMethodRS256.Sign(input, p.key)
		if err != nil {
			t.Error(err)
			return
		}
		response["id_token"] = input + "." + base64.RawURLEncoding.EncodeToString(signature)
		writeJSON(t, w, response)
	})
	identity, err := testClient(t).ExchangeAndVerify(context.Background(), testConfig(p), ExchangeRequest{RedirectURL: "http://localhost/callback", Code: "code-1", CodeVerifier: strings.Repeat("v", 43), ExpectedNonce: p.nonce})
	if !errors.Is(err, ErrInvalidToken) || len(identity.Sources) != 0 {
		t.Fatalf("ambiguous signed claims accepted: %v", err)
	}
}

func TestOIDCUserInfoTimeoutRetainsOnlyVerifiedToken(t *testing.T) {
	p := newTestProvider(t)
	defer p.close()
	original := p.server.Config.Handler
	p.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/userinfo" {
			original.ServeHTTP(w, r)
			return
		}
		<-r.Context().Done()
	})
	c := testClient(t)
	c.deployment.RequestTimeout = 100 * time.Millisecond
	identity, err := c.ExchangeAndVerify(context.Background(), testConfig(p), ExchangeRequest{RedirectURL: "http://localhost/callback", Code: "code-1", CodeVerifier: strings.Repeat("v", 43), ExpectedNonce: p.nonce})
	if err != nil || identity.Subject != "subject-1" || identity.SourceErrors["oidc_userinfo"] != "source_unavailable" || len(identity.Sources["oidc_userinfo"]) != 0 {
		t.Fatalf("timeout identity: %+v %v", identity, err)
	}
}
