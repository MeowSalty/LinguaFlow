package credential

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestEndpointBindingRejectsOriginAndPathEscape(t *testing.T) {
	ep, err := NormalizeEndpoint("openai", "HTTPS://API.OPENAI.COM:443/v1")
	if err != nil {
		t.Fatal(err)
	}
	if ep != "https://api.openai.com/v1/" {
		t.Fatalf("endpoint=%q", ep)
	}
	for _, test := range []struct {
		raw   string
		allow bool
	}{
		{"https://api.openai.com/v1/chat/completions", true},
		{"https://api.openai.com:443/v1/models?x=1", true},
		{"https://other.example/v1/chat/completions", false},
		{"http://api.openai.com/v1/chat/completions", false},
		{"https://api.openai.com/v10/chat/completions", false},
		{"https://api.openai.com/v1/../outside", false},
		{"https://api.openai.com/v1/%2e%2e/outside", false},
		{"https://api.openai.com/v1/%252e%252e/outside", false},
		{"https://api.openai.com/v1/%5c..%5coutside", false},
		{"https://user:secret@api.openai.com/v1/models", false},
	} {
		u, err := url.Parse(test.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := endpointContains(ep, u); got != test.allow {
			t.Errorf("allowed %s=%v", test.raw, got)
		}
	}
	for _, raw := range []string{"https://key:secret@example.com/", "https://example.com/?api_key=x", "https://example.com/#token", "file:///tmp/key"} {
		if _, err := NormalizeEndpoint("openai", raw); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", raw)
		}
	}
}
func TestGuardChecksEveryAttemptAndBlocksRedirect(t *testing.T) {
	var calls atomic.Int64
	var external atomic.Int64
	out := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { external.Add(1); w.WriteHeader(200) }))
	defer out.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, out.URL, 307)
			return
		}
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	registry := NewMemory()
	b, err := registry.Register("openai", upstream.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	client, err := GuardClient(nil, registry, b, 0, "openai", upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(upstream.URL + "/call")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := client.Get(upstream.URL + "/redirect"); !errors.Is(err, ErrEndpoint) {
		t.Fatalf("redirect: %v", err)
	}
	if external.Load() != 0 {
		t.Fatal("credential sent outside bound endpoint")
	}
	if err := registry.Revoke(b); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(upstream.URL + "/retry"); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked retry: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("revoked request reached upstream: %d", calls.Load())
	}
	if _, err := registry.Resolve(context.Background(), b, "openai", upstream.URL); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked resolution: %v", err)
	}
}
