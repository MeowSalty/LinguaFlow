package authoidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authhttp"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOIDCRejectsInvalidClaimsAndSignature(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claims map[string]any
	}{
		{"issuer", map[string]any{"iss": "https://wrong.example.test"}},
		{"audience", map[string]any{"aud": "another-client"}},
		{"azp", map[string]any{"azp": "another-client"}},
		{"multiple audiences require azp", map[string]any{"aud": []string{"client-1", "other"}}},
		{"expired", map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}},
		{"future issued", map[string]any{"iat": time.Now().Add(time.Hour).Unix()}},
		{"empty subject", map[string]any{"sub": ""}},
		{"signature", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProvider(t)
			defer p.close()
			p.claimsOverride = tc.claims
			if tc.name == "signature" {
				var err error
				p.wrongSigningKey, err = rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := testClient(t).ExchangeAndVerify(context.Background(), testConfig(p), ExchangeRequest{RedirectURL: "http://localhost/callback", Code: "code-1", CodeVerifier: strings.Repeat("v", 43), ExpectedNonce: p.nonce})
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("invalid token accepted: %v", err)
			}
		})
	}
}

func TestOIDCOutboundTimeoutSizeAndRedirect(t *testing.T) {
	for _, name := range []string{"timeout", "oversize"} {
		t.Run(name, func(t *testing.T) {
			p := newTestProvider(t)
			defer p.close()
			c := testClient(t)
			if name == "timeout" {
				p.tokenDelay = 200 * time.Millisecond
				c.deployment.RequestTimeout = 50 * time.Millisecond
			}
			if name == "oversize" {
				p.claimsOverride = map[string]any{"name": strings.Repeat("private-profile-marker", 1000)}
				c.transport.Deployment.MaxResponseBytes = 2048
			}
			_, err := c.ExchangeAndVerify(context.Background(), testConfig(p), ExchangeRequest{RedirectURL: "http://localhost/callback", Code: "code-1", CodeVerifier: strings.Repeat("v", 43), ExpectedNonce: p.nonce})
			want := ErrUnavailable
			if name == "oversize" {
				want = ErrInvalidToken
			}
			if !errors.Is(err, want) || strings.Contains(err.Error(), "private-profile-marker") {
				t.Fatalf("unsafe upstream error: %v", err)
			}
		})
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer provider.Close()
	if _, err := testClient(t).Check(context.Background(), Config{Issuer: provider.URL, ClientID: "client-1"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("redirect accepted: %v", err)
	}
	if redirected.Load() != 0 {
		t.Fatal("provider redirect was followed")
	}
	policy := &authhttp.Transport{}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "0.0.0.0", "224.0.0.1", "::1", "fe80::1", "ff02::1"} {
		if policy.Allowed("public-name.example.test", net.ParseIP(ip)) {
			t.Fatalf("unsafe default destination allowed: %s", ip)
		}
	}
}
