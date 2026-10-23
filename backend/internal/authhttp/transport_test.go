package authhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authprotocol"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestURLPolicy(t *testing.T) {
	for _, tt := range []struct {
		raw           string
		allowLoopback bool
		want          bool
	}{
		{"https://identity.example.test/token", false, true},
		{"http://identity.example.test/token", true, false},
		{"http://127.0.0.1/token", false, false},
		{"http://127.0.0.1/token", true, true},
		{"http://localhost/token", true, true},
		{"http://[::1]/token", true, true},
		{"https://user:password@identity.example.test/token", false, false},
		{"https://identity.example.test/token#fragment", false, false},
		{"//identity.example.test/token", false, false},
		{"/token", false, false},
		{"file:///token", false, false},
		{"https:///token", false, false},
		{"https://identity.example.test/%zz", false, false},
	} {
		t.Run(tt.raw, func(t *testing.T) {
			transport := New(config.AuthDeploymentConfig{AllowHTTPLoopback: tt.allowLoopback})
			if got := transport.ValidRawURL(tt.raw); got != tt.want {
				t.Fatalf("ValidRawURL(%q)=%v; want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestAddressPolicy(t *testing.T) {
	transport := New(config.AuthDeploymentConfig{})
	for _, raw := range []string{"127.0.0.1", "::1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "fe80::1", "fc00::1", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "::ffff:127.0.0.1"} {
		if transport.Allowed("identity.example.test", net.ParseIP(raw)) {
			t.Errorf("private or non-unicast destination %s allowed without opt-in", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !transport.Allowed("identity.example.test", net.ParseIP(raw)) {
			t.Errorf("public address %s rejected", raw)
		}
	}
	transport.Deployment.AllowedHosts = []string{" IDENTITY.example.test "}
	if !transport.Allowed("identity.example.test", net.ParseIP("127.0.0.1")) {
		t.Error("explicit private host exception rejected")
	}
	if transport.Allowed("other.example.test", net.ParseIP("8.8.8.8")) {
		t.Error("host allowlist did not restrict unrelated public destination")
	}
	transport.Deployment.AllowedHosts = nil
	transport.Deployment.AllowedCIDRs = []string{"10.1.0.0/16"}
	if !transport.Allowed("identity.example.test", net.ParseIP("10.1.2.3")) || transport.Allowed("identity.example.test", net.ParseIP("10.2.3.4")) {
		t.Error("CIDR exception does not match its configured range")
	}
}

func TestTransportBlocksSSRFBeforeConnecting(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	transport := New(config.AuthDeploymentConfig{AllowHTTPLoopback: true, RequestTimeout: time.Second, MaxResponseBytes: 16})
	defer transport.Base.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(req); !errors.Is(err, authprotocol.ErrUnavailable) {
		t.Fatalf("loopback without an allowlist error=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("SSRF guard connected to loopback before rejecting it")
	}
	transport.Deployment.AllowedHosts = []string{"127.0.0.1"}
	res, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("explicitly allowed loopback request failed: %v", err)
	}
	defer res.Body.Close()
	if _, err := io.ReadAll(res.Body); err != nil || calls.Load() != 1 {
		t.Fatalf("allowed loopback calls=%d error=%v", calls.Load(), err)
	}
	if _, err := transport.Dial(context.Background(), "tcp", "invalid-address"); !errors.Is(err, authprotocol.ErrUnavailable) {
		t.Fatalf("invalid dial address error=%v", err)
	}
}

func TestResponseLimitIncludesChunkedBodies(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		for _, size := range []int{0, 31, 32, 33, 128} {
			t.Run(fmt.Sprintf("chunked=%t/size=%d", chunked, size), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if chunked {
						w.(http.Flusher).Flush()
					}
					_, _ = io.WriteString(w, strings.Repeat("x", size))
				}))
				defer server.Close()
				transport := New(config.AuthDeploymentConfig{AllowHTTPLoopback: true, AllowedHosts: []string{"127.0.0.1"}, RequestTimeout: time.Second, MaxResponseBytes: 32})
				defer transport.Base.CloseIdleConnections()
				client := &http.Client{Transport: transport, Timeout: time.Second}
				res, err := client.Get(server.URL)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				body, err := io.ReadAll(res.Body)
				if size <= 32 {
					if err != nil || len(body) != size {
						t.Fatalf("body size=%d err=%v; want %d", len(body), err, size)
					}
				} else if !errors.Is(err, authprotocol.ErrUnavailable) || len(body) > 32 {
					t.Fatalf("oversized response returned size=%d error=%v", len(body), err)
				}
			})
		}
	}
}

func TestTransportGateAllowsRequest(t *testing.T) {
	var serverCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serverCalls.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	gate := NewGate()
	transport := New(config.AuthDeploymentConfig{AllowHTTPLoopback: true, AllowedHosts: []string{"127.0.0.1"}, RequestTimeout: time.Second, MaxResponseBytes: 32})
	defer transport.Base.CloseIdleConnections()
	ctx := WithGate(context.Background(), gate, "conn-1", func(context.Context) error { return nil })
	client := &http.Client{Transport: transport, Timeout: time.Second}
	res, err := client.Do(mustRequest(t, http.MethodGet, server.URL+"/", ctx))
	if err != nil {
		t.Fatalf("gated request failed: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if string(body) != "ok" || serverCalls.Load() != 1 {
		t.Fatalf("gated response body=%q calls=%d", body, serverCalls.Load())
	}
}

func TestTransportGateRejectsBlockedConnection(t *testing.T) {
	gate := NewGate()
	unblock, err := gate.Block(context.Background(), "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	defer unblock()
	transport := New(config.AuthDeploymentConfig{AllowHTTPLoopback: true, AllowedHosts: []string{"127.0.0.1"}, RequestTimeout: time.Second, MaxResponseBytes: 32})
	defer transport.Base.CloseIdleConnections()
	ctx := WithGate(context.Background(), gate, "conn-1", func(context.Context) error { return nil })
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.RoundTrip(req.WithContext(ctx))
	if !errors.Is(err, ErrGateBlocked) {
		t.Fatalf("expected ErrGateBlocked; got %v", err)
	}
}

func TestTransportGateCheckRejected(t *testing.T) {
	gate := NewGate()
	checkErr := errors.New("connection is disabled")
	transport := New(config.AuthDeploymentConfig{AllowHTTPLoopback: true, AllowedHosts: []string{"127.0.0.1"}, RequestTimeout: time.Second, MaxResponseBytes: 32})
	defer transport.Base.CloseIdleConnections()
	ctx := WithGate(context.Background(), gate, "conn-1", func(context.Context) error { return checkErr })
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.RoundTrip(req.WithContext(ctx))
	if !errors.Is(err, checkErr) {
		t.Fatalf("expected check error; got %v", err)
	}
}

func TestTransportGateReleasesOnBodyClose(t *testing.T) {
	var serverCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serverCalls.Add(1)
		_, _ = io.WriteString(w, "chunk-1")
		w.(http.Flusher).Flush()
		time.Sleep(10 * time.Millisecond)
		_, _ = io.WriteString(w, "chunk-2")
	}))
	defer server.Close()
	gate := NewGate()
	transport := New(config.AuthDeploymentConfig{AllowHTTPLoopback: true, AllowedHosts: []string{"127.0.0.1"}, RequestTimeout: time.Second, MaxResponseBytes: 128})
	defer transport.Base.CloseIdleConnections()
	ctx := WithGate(context.Background(), gate, "conn-1", func(context.Context) error { return nil })
	res, err := (&http.Client{Transport: transport, Timeout: time.Second}).Do(mustRequest(t, http.MethodGet, server.URL+"/", ctx))
	if err != nil {
		t.Fatal(err)
	}
	// Close the body before EOF.
	_ = res.Body.Close()
	// Block should succeed immediately since the activity was released.
	ctx2, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unblock, blockErr := gate.Block(ctx2, "conn-1")
	if blockErr != nil {
		t.Fatalf("Block after body close did not drain: %v", blockErr)
	}
	unblock()
}

func TestTransportGateReleasesOnBodyEOF(t *testing.T) {
	var serverCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serverCalls.Add(1)
		_, _ = io.WriteString(w, "short")
	}))
	defer server.Close()
	gate := NewGate()
	transport := New(config.AuthDeploymentConfig{AllowHTTPLoopback: true, AllowedHosts: []string{"127.0.0.1"}, RequestTimeout: time.Second, MaxResponseBytes: 128})
	defer transport.Base.CloseIdleConnections()
	ctx := WithGate(context.Background(), gate, "conn-1", func(context.Context) error { return nil })
	res, err := (&http.Client{Transport: transport, Timeout: time.Second}).Do(mustRequest(t, http.MethodGet, server.URL+"/", ctx))
	if err != nil {
		t.Fatal(err)
	}
	// Read to EOF (read returns 5 bytes + EOF).
	_, err = io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body error: %v", err)
	}
	// Block should succeed immediately since the activity was released on EOF.
	ctx2, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unblock, blockErr := gate.Block(ctx2, "conn-1")
	if blockErr != nil {
		t.Fatalf("Block after body EOF did not drain: %v", blockErr)
	}
	unblock()
}

func TestTransportGateReleasesOnTransportError(t *testing.T) {
	gate := NewGate()
	checkCalled := make(chan struct{})
	transport := New(config.AuthDeploymentConfig{AllowHTTPLoopback: true, AllowedHosts: []string{"127.0.0.1"}, RequestTimeout: time.Second, MaxResponseBytes: 32})
	defer transport.Base.CloseIdleConnections()
	ctx := WithGate(context.Background(), gate, "conn-1", func(context.Context) error {
		close(checkCalled)
		return nil
	})
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = transport.RoundTrip(req.WithContext(ctx))
	// Block should succeed immediately since the activity was released on error.
	ctx2, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unblock, blockErr := gate.Block(ctx2, "conn-1")
	if blockErr != nil {
		t.Fatalf("Block after transport error did not drain: %v", blockErr)
	}
	unblock()
}

func TestTransportGateCancellationPropagates(t *testing.T) {
	releaseReceived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = io.WriteString(w, "late")
	}))
	defer server.Close()
	gate := NewGate()
	transport := New(config.AuthDeploymentConfig{AllowHTTPLoopback: true, AllowedHosts: []string{"127.0.0.1"}, RequestTimeout: 5 * time.Second, MaxResponseBytes: 128})
	defer transport.Base.CloseIdleConnections()
	baseCtx, baseCancel := context.WithCancel(context.Background())
	ctx := WithGate(baseCtx, gate, "conn-1", func(context.Context) error { return nil })
	go func() {
		res, _ := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(mustRequest(t, http.MethodGet, server.URL+"/", ctx))
		if res != nil {
			_ = res.Body.Close()
		}
		close(releaseReceived)
	}()
	// Cancel the base context, which should propagate to the activity context.
	time.Sleep(10 * time.Millisecond)
	baseCancel()
	// Block should succeed since all activities were cancelled and released.
	blockCtx, blockCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer blockCancel()
	unblock, blockErr := gate.Block(blockCtx, "conn-1")
	if blockErr != nil {
		t.Fatalf("Block after cancellation did not drain: %v", blockErr)
	}
	unblock()
	<-releaseReceived
}

func mustRequest(t *testing.T, method, url string, ctx context.Context) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req.WithContext(ctx)
}
